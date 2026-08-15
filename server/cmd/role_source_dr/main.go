// role_source_dr creates and verifies database/object-store consistent
// role-source disaster-recovery bundles. It is operator-only and exposes no
// network endpoint.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/drlock"
	"github.com/multica-ai/multica/server/internal/rolesourcedr"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

const migrationAdvisoryLockKey int64 = 7244554146635925501

const maxManifestBytes int64 = 1 << 20

var secretKeyIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "role-source DR:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: role_source_dr <generate-signing-key|backup|restore-artifacts|verify> [options]")
	}
	switch args[0] {
	case "generate-signing-key":
		return runGenerateSigningKey(args[1:])
	case "backup":
		return runBackup(ctx, args[1:])
	case "verify":
		return runVerify(ctx, args[1:])
	case "restore-artifacts":
		return runRestoreArtifacts(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q; use backup, restore-artifacts or verify", args[0])
	}
}

func runGenerateSigningKey(args []string) error {
	flags := flag.NewFlagSet("generate-signing-key", flag.ContinueOnError)
	keyID := flags.String("key-id", "", "stable key id configured independently on backup and restore hosts")
	privatePath := flags.String("private-key-file", "", "new mode-0600 file for one-time KMS/HSM import")
	publicPath := flags.String("public-key-file", "", "new mode-0644 public key file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keyID == "" || *privatePath == "" || *publicPath == "" {
		return errors.New("--key-id, --private-key-file and --public-key-file are required")
	}
	if err := rolesourcedr.ValidateSignerKeyID(*keyID); err != nil {
		return err
	}
	publicKey, privateKey, err := rolesourcedr.GenerateSigningKey()
	if err != nil {
		return err
	}
	defer clear(privateKey)
	if err := writeTextExclusive(*privatePath, base64.StdEncoding.EncodeToString(privateKey)+"\n", 0o600); err != nil {
		return err
	}
	if err := writeTextExclusive(*publicPath, base64.StdEncoding.EncodeToString(publicKey)+"\n", 0o644); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	printJSON(map[string]any{"status": "signing_key_generated", "key_id": *keyID})
	return nil
}

func runRestoreArtifacts(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore-artifacts", flag.ContinueOnError)
	manifestPath := flags.String("manifest", "", "backup manifest.json")
	archivePath := flags.String("artifact-archive", "", "artifacts.tar")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" || *archivePath == "" {
		return errors.New("--manifest and --artifact-archive are required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	archiveDigest, err := rolesourcedr.FileDigest(*archivePath)
	if err != nil || archiveDigest != manifest.ArtifactArchiveDigest {
		return errors.New("artifact archive digest does not match manifest")
	}
	pool, _, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	guardConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer guardConn.Release()
	if _, err := guardConn.Exec(ctx, "SELECT pg_advisory_lock($1)", drlock.AdvisoryLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = guardConn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", drlock.AdvisoryLockKey)
	}()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	actual, records, err := rolesourcedr.BuildManifest(ctx, tx)
	if err != nil {
		return err
	}
	if !rolesourcedr.SameArtifactInventory(manifest, actual) {
		return errors.New("restored database does not match backup manifest; refusing object restore")
	}
	if err := rolesourcedr.RestoreArtifactArchive(ctx, *archivePath, records, rolesourcedr.StorageFromEnv()); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	printJSON(rolesourcedr.PublicSummary{Status: "artifact_restore_passed", Artifacts: manifest.Artifacts.Count, ArtifactBytes: manifest.Artifacts.SizeBytes})
	return nil
}

func runBackup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := flags.String("output-dir", "", "new private directory for database.dump, artifacts.tar and manifest.json")
	pgDump := flags.String("pg-dump", "pg_dump", "PostgreSQL 17 pg_dump executable")
	allowUnsigned := flags.Bool("allow-unsigned-manifest", false, "development-only: create an unsigned manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		return errors.New("--output-dir is required")
	}
	if err := os.Mkdir(*output, 0o700); err != nil {
		return fmt.Errorf("create new output directory: %w", err)
	}
	incompletePath := filepath.Join(*output, "INCOMPLETE")
	if err := os.WriteFile(incompletePath, []byte("role-source backup did not complete\n"), 0o600); err != nil {
		return fmt.Errorf("create incomplete-backup marker: %w", err)
	}

	pool, dbURL, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := requirePostgres17Dump(ctx, *pgDump); err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Serialize migrations first, then take the exclusive role-source DR lock.
	// Every destructive role-source path holds the shared side.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey)
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", drlock.AdvisoryLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", drlock.AdvisoryLockKey)
	}()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	var snapshotID string
	if err := tx.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&snapshotID); err != nil {
		return fmt.Errorf("export PostgreSQL snapshot: %w", err)
	}
	manifest, artifacts, err := rolesourcedr.BuildManifest(ctx, tx)
	if err != nil {
		return err
	}

	archivePath := filepath.Join(*output, "artifacts.tar")
	manifest.ArtifactArchiveDigest, err = rolesourcedr.WriteArtifactArchive(ctx, archivePath, artifacts, rolesourcedr.StorageFromEnv(), manifest.CreatedAt)
	if err != nil {
		return err
	}
	dumpPath := filepath.Join(*output, "database.dump")
	pgDumpArgs, pgDumpPassword, err := buildPGDumpArgs(dbURL, snapshotID, dumpPath)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, *pgDump, pgDumpArgs...)
	command.Env = replaceEnvironmentValue(os.Environ(), "PGPASSWORD", pgDumpPassword)
	commandOutput, err := command.CombinedOutput()
	if err != nil {
		detail := boundedOutput(commandOutput)
		if detail == "" {
			return fmt.Errorf("pg_dump failed: %w", err)
		}
		return fmt.Errorf("pg_dump failed: %w; output: %s", err, detail)
	}
	manifest.DatabaseDumpDigest, err = rolesourcedr.FileDigest(dumpPath)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("complete exported snapshot: %w", err)
	}
	if err := signBackupManifest(&manifest, *allowUnsigned); err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(*output, "manifest.json"), manifest); err != nil {
		return err
	}
	if err := os.Chmod(dumpPath, 0o600); err != nil {
		return fmt.Errorf("secure database dump: %w", err)
	}
	if err := os.Remove(incompletePath); err != nil {
		return fmt.Errorf("remove incomplete-backup marker: %w", err)
	}
	if err := syncDirectory(*output); err != nil {
		return err
	}
	printJSON(manifest.PublicSummary("backup_passed"))
	return nil
}

func buildPGDumpArgs(dbURL, snapshotID, dumpPath string) ([]string, string, error) {
	config, err := pgx.ParseConfig(dbURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse database identity for pg_dump: %w", err)
	}
	if strings.TrimSpace(config.User) == "" {
		return nil, "", errors.New("database user is required for pg_dump")
	}
	parsed, err := url.Parse(dbURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return nil, "", errors.New("role-source DR requires URL-form DATABASE_URL for pg_dump")
	}
	parsed.User = url.User(config.User)
	query := parsed.Query()
	for key := range query {
		if strings.HasPrefix(key, "pool_") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return []string{
		"--format=custom", "--no-owner", "--no-privileges",
		"--username=" + config.User,
		"--dbname=" + parsed.String(),
		"--snapshot=" + snapshotID,
		"--file=" + dumpPath,
	}, config.Password, nil
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func runVerify(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	manifestPath := flags.String("manifest", "", "backup manifest.json")
	reportPath := flags.String("report", "", "new path for redacted verification report")
	dumpPath := flags.String("database-dump", "", "optional database.dump path to verify before checking restored state")
	archivePath := flags.String("artifact-archive", "", "optional artifacts.tar path to verify before checking restored state")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" || *reportPath == "" {
		return errors.New("--manifest and --report are required")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return err
	}
	preflightFindings := []rolesourcedr.Finding{}
	if *dumpPath != "" {
		if digest, digestErr := rolesourcedr.FileDigest(*dumpPath); digestErr != nil || digest != manifest.DatabaseDumpDigest {
			preflightFindings = append(preflightFindings, rolesourcedr.Finding{Code: "database_dump_digest_mismatch", Count: 1})
		}
	}
	if *archivePath != "" {
		if digest, digestErr := rolesourcedr.FileDigest(*archivePath); digestErr != nil || digest != manifest.ArtifactArchiveDigest {
			preflightFindings = append(preflightFindings, rolesourcedr.Finding{Code: "artifact_archive_digest_mismatch", Count: 1})
		}
	}
	pool, _, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	guardConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer guardConn.Release()
	if _, err := guardConn.Exec(ctx, "SELECT pg_advisory_lock($1)", drlock.AdvisoryLockKey); err != nil {
		return err
	}
	defer func() {
		_, _ = guardConn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", drlock.AdvisoryLockKey)
	}()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	trustedManifestKeys, err := loadManifestTrustStrict()
	if err != nil {
		return err
	}
	keyring, err := loadKeyringStrict()
	if err != nil {
		return err
	}
	report, err := rolesourcedr.Verify(ctx, tx, rolesourcedr.VerificationOptions{
		Expected: manifest, TrustedManifestKeys: trustedManifestKeys, Storage: rolesourcedr.StorageFromEnv(), Keys: keyring,
	})
	if err != nil {
		return err
	}
	report.Findings = append(report.Findings, preflightFindings...)
	if len(report.Findings) > 0 {
		report.Status = "failed"
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if err := writeJSONExclusive(*reportPath, report); err != nil {
		return err
	}
	if report.Status != "passed" {
		return fmt.Errorf("verification failed with %d finding classes; see redacted report", len(report.Findings))
	}
	printJSON(rolesourcedr.PublicSummary{Status: "restore_verification_passed", Tables: report.Database.TablesChecked, Artifacts: report.Artifacts.Verified, ArtifactBytes: report.Artifacts.SizeBytes})
	return nil
}

func openDatabase(ctx context.Context) (*pgxpool.Pool, string, error) {
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return nil, "", errors.New("DATABASE_URL is required; defaults are forbidden for disaster recovery")
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, "", fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, "", fmt.Errorf("ping database: %w", err)
	}
	var version int
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&version); err != nil {
		pool.Close()
		return nil, "", err
	}
	if version != 17 {
		pool.Close()
		return nil, "", fmt.Errorf("PostgreSQL 17 is required, got major %d", version)
	}
	return pool, dbURL, nil
}

func loadKeyringStrict() (map[string]rolesourcedr.SecretOpener, error) {
	keyring := map[string]rolesourcedr.SecretOpener{}
	currentRaw := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_SECRET_KEY"))
	currentID := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_SECRET_KEY_ID"))
	if currentRaw == "" && currentID != "" {
		return nil, errors.New("role-source secret key id is configured without key material")
	}
	if currentRaw != "" {
		key, err := secretbox.LoadKey("MULTICA_ROLE_SOURCE_SECRET_KEY")
		if err != nil {
			return nil, err
		}
		defer clear(key)
		if currentID == "" {
			currentID = "v1"
		}
		if !secretKeyIDPattern.MatchString(currentID) {
			return nil, errors.New("invalid current role-source secret key id")
		}
		box, err := secretbox.New(key)
		if err != nil {
			return nil, err
		}
		keyring[currentID] = box
	}
	previousRaw := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_SECRET_PREVIOUS_KEYS"))
	if previousRaw == "" {
		return keyring, nil
	}
	if len(previousRaw) > 64<<10 {
		return nil, errors.New("previous role-source secret key map is oversized")
	}
	var previous map[string]string
	decoder := json.NewDecoder(strings.NewReader(previousRaw))
	if err := decoder.Decode(&previous); err != nil || len(previous) > 8 {
		return nil, errors.New("invalid previous role-source secret key map")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, errors.New("invalid previous role-source secret key map")
	}
	for keyID, encoded := range previous {
		if !secretKeyIDPattern.MatchString(keyID) {
			return nil, errors.New("invalid previous role-source secret key id")
		}
		if _, exists := keyring[keyID]; exists {
			return nil, errors.New("duplicated role-source secret key id")
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != secretbox.KeySize {
			clear(key)
			return nil, errors.New("invalid previous role-source secret key material")
		}
		box, err := secretbox.New(key)
		clear(key)
		if err != nil {
			return nil, err
		}
		keyring[keyID] = box
	}
	return keyring, nil
}

func readManifest(path string) (rolesourcedr.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return rolesourcedr.Manifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return rolesourcedr.Manifest{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return rolesourcedr.Manifest{}, errors.New("DR manifest must be a bounded regular file")
	}
	var manifest rolesourcedr.Manifest
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return manifest, errors.New("DR manifest contains trailing data")
	}
	trusted, err := loadManifestTrustStrict()
	if err != nil {
		return manifest, err
	}
	if err := rolesourcedr.VerifyManifestSignature(manifest, trusted, false); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func signBackupManifest(manifest *rolesourcedr.Manifest, allowUnsigned bool) error {
	encoded := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY"))
	keyID := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID"))
	if encoded == "" || keyID == "" {
		if allowUnsigned && encoded == "" && keyID == "" {
			manifest.UnsignedDevelopment = true
			return rolesourcedr.ValidateManifest(*manifest, true)
		}
		return errors.New("MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY and MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID are required")
	}
	privateKey, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		clear(privateKey)
		return errors.New("invalid base64 Ed25519 DR signing private key")
	}
	defer clear(privateKey)
	return rolesourcedr.SignManifest(manifest, keyID, ed25519.PrivateKey(privateKey))
}

func loadManifestTrustStrict() (map[string]ed25519.PublicKey, error) {
	raw := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS"))
	if raw == "" {
		return nil, errors.New("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS is required")
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("DR trusted public key map is oversized")
	}
	var encoded map[string]string
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil || len(encoded) == 0 || len(encoded) > 8 {
		return nil, errors.New("invalid DR trusted public key map")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, errors.New("invalid DR trusted public key map")
	}
	trusted := map[string]ed25519.PublicKey{}
	for keyID, value := range encoded {
		if err := rolesourcedr.ValidateSignerKeyID(keyID); err != nil {
			return nil, err
		}
		key, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid DR public key %q", keyID)
		}
		trusted[keyID] = ed25519.PublicKey(key)
	}
	return trusted, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSONExclusive(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func boundedOutput(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 2048 {
		value = value[:2048]
	}
	return value
}

func requirePostgres17Dump(ctx context.Context, executable string) error {
	output, err := exec.CommandContext(ctx, executable, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("run pg_dump --version: %w", err)
	}
	for _, field := range strings.Fields(string(output)) {
		if field == "17" || strings.HasPrefix(field, "17.") {
			return nil
		}
	}
	return fmt.Errorf("PostgreSQL 17 pg_dump is required, got %q", boundedOutput(output))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	return nil
}

func printJSON(value any) {
	body, _ := json.Marshal(value)
	fmt.Println(string(body))
}

func writeTextExclusive(path, value string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(value); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}
