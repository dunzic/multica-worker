// role_source_outbox_replay is an offline platform-operator command. It has no
// HTTP route and can only requeue an existing, verified dead outbox event; it
// never invokes role-source apply or accepts an event payload.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/rolesourcereplay"
)

const maxOperatorFileBytes = 1 << 20

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "role-source outbox replay:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: role_source_outbox_replay <inspect|prepare|execute> [options]")
	}
	switch args[0] {
	case "inspect":
		return runInspect(ctx, args[1:])
	case "prepare":
		return runPrepare(ctx, args[1:])
	case "execute":
		return runExecute(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q; use inspect, prepare or execute", args[0])
	}
}

func runInspect(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	eventID := flags.String("event-id", "", "dead role-source outbox UUID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *eventID == "" || flags.NArg() != 0 {
		return errors.New("inspect requires exactly --event-id")
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	summary, err := rolesourcereplay.Inspect(ctx, pool, *eventID)
	if err != nil {
		return err
	}
	return printJSON(summary)
}

func runPrepare(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	eventID := flags.String("event-id", "", "dead role-source outbox UUID")
	reason := flags.String("reason-code", "", "dependency_recovered, incident_recovery or delivery_reconciliation")
	incidentFile := flags.String("incident-reference-file", "", "private file containing the approved external incident reference")
	output := flags.String("output", "", "new mode-0600 canonical authorization JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *eventID == "" || *reason == "" || *incidentFile == "" || *output == "" || flags.NArg() != 0 {
		return errors.New("prepare requires --event-id, --reason-code, --incident-reference-file and --output")
	}
	incident, err := readPrivateFile(*incidentFile)
	if err != nil {
		return fmt.Errorf("read incident reference: %w", err)
	}
	incident = []byte(strings.TrimSpace(string(incident)))
	if len(incident) == 0 || len(incident) > 512 || strings.ContainsAny(string(incident), "\r\n\x00") {
		return errors.New("incident reference must be one non-empty line of at most 512 bytes")
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	summary, err := rolesourcereplay.Inspect(ctx, pool, *eventID)
	if err != nil {
		return err
	}
	auth, err := rolesourcereplay.NewAuthorization(summary, *reason, digest(incident), nowUTC())
	if err != nil {
		return err
	}
	body, err := rolesourcereplay.CanonicalAuthorization(auth)
	if err != nil {
		return err
	}
	if err := writeExclusive(*output, body); err != nil {
		return err
	}
	return printJSON(map[string]any{
		"status": "authorization_prepared", "authorization_id": auth.AuthorizationID,
		"outbox_id": auth.OutboxID, "generation": auth.ExpectedGeneration,
		"authorization_digest": digest(body), "expires_at": auth.ExpiresAt,
	})
}

func runExecute(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("execute", flag.ContinueOnError)
	authorizationFile := flags.String("authorization", "", "canonical authorization JSON created by prepare")
	requesterKeyID := flags.String("requester-key-id", "", "configured requester signing key id")
	requesterSignatureFile := flags.String("requester-signature-file", "", "private base64 Ed25519 signature file")
	approverKeyID := flags.String("approver-key-id", "", "configured approver signing key id")
	approverSignatureFile := flags.String("approver-signature-file", "", "private base64 Ed25519 signature file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *authorizationFile == "" || *requesterKeyID == "" || *requesterSignatureFile == "" || *approverKeyID == "" || *approverSignatureFile == "" || flags.NArg() != 0 {
		return errors.New("execute requires authorization, two distinct key ids and two signature files")
	}
	authBody, err := readPrivateFile(*authorizationFile)
	if err != nil {
		return fmt.Errorf("read authorization: %w", err)
	}
	var auth rolesourcereplay.Authorization
	if err := decodeStrictJSON(authBody, &auth); err != nil {
		return fmt.Errorf("decode authorization: %w", err)
	}
	canonicalAuth, err := rolesourcereplay.CanonicalAuthorization(auth)
	if err != nil {
		return fmt.Errorf("validate authorization: %w", err)
	}
	if !bytes.Equal(authBody, canonicalAuth) {
		return errors.New("authorization file is not the exact canonical JSON created by prepare")
	}
	requesterSignature, err := readSignature(*requesterSignatureFile)
	if err != nil {
		return fmt.Errorf("read requester signature: %w", err)
	}
	approverSignature, err := readSignature(*approverSignatureFile)
	if err != nil {
		return fmt.Errorf("read approver signature: %w", err)
	}
	keyringPath := strings.TrimSpace(os.Getenv("MULTICA_ROLE_SOURCE_REPLAY_PUBLIC_KEYS_FILE"))
	if keyringPath == "" {
		return errors.New("MULTICA_ROLE_SOURCE_REPLAY_PUBLIC_KEYS_FILE is required")
	}
	keyringBody, err := readTrustedPublicFile(keyringPath)
	if err != nil {
		return fmt.Errorf("read replay public keyring: %w", err)
	}
	keyring, err := rolesourcereplay.DecodePublicKeyring(keyringBody)
	if err != nil {
		return err
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	receipt, err := rolesourcereplay.Execute(ctx, pool, rolesourcereplay.ExecuteInput{
		Authorization: auth, PublicKeys: keyring, Now: nowUTC(),
		Requester: rolesourcereplay.Signature{KeyID: *requesterKeyID, Value: requesterSignature},
		Approver:  rolesourcereplay.Signature{KeyID: *approverKeyID, Value: approverSignature},
	})
	if err != nil {
		return err
	}
	return printJSON(receipt)
}

func openDatabase(ctx context.Context) (*pgxpool.Pool, error) {
	dbURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dbURL == "" {
		return nil, errors.New("DATABASE_URL is required; defaults are forbidden for replay")
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	var version int
	if err := pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&version); err != nil {
		pool.Close()
		return nil, err
	}
	if version != 17 {
		pool.Close()
		return nil, fmt.Errorf("PostgreSQL 17 is required, got major %d", version)
	}
	return pool, nil
}

func readPrivateFile(path string) ([]byte, error) {
	return readSecureFile(path, true)
}

func readTrustedPublicFile(path string) ([]byte, error) {
	return readSecureFile(path, false)
}

func readSecureFile(path string, requirePrivate bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("operator file must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maxOperatorFileBytes {
		return nil, errors.New("operator file size is outside the allowed range")
	}
	if info.Mode().Perm()&0o022 != 0 || (requirePrivate && info.Mode().Perm()&0o077 != 0) {
		return nil, errors.New("operator file permissions are too broad")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("operator file identity changed during open")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxOperatorFileBytes+1))
	if err != nil || len(body) > maxOperatorFileBytes {
		return nil, errors.New("operator file exceeds the allowed size")
	}
	return body, nil
}

func readSignature(path string) ([]byte, error) {
	body, err := readPrivateFile(path)
	if err != nil {
		return nil, err
	}
	value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil || len(value) != 64 {
		return nil, errors.New("signature must be one base64 Ed25519 signature")
	}
	return value, nil
}

func decodeStrictJSON(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func writeExclusive(path string, body []byte) error {
	cleanPath := filepath.Clean(path)
	parent := filepath.Dir(cleanPath)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return errors.New("operator output directory must be a non-symlink directory that is not group- or world-writable")
	}
	file, err := os.OpenFile(cleanPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var nowUTC = func() (value time.Time) { return time.Now().UTC() }
