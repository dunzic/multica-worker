// channel_delivery_reconcile is an offline platform-operator command. It has
// no HTTP route and cannot accept message content or call a provider directly.
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

	"github.com/multica-ai/multica/server/internal/integrations/delivery"
)

const maxOperatorFileBytes = 16 << 20

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "channel-delivery reconciliation:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: channel_delivery_reconcile <inspect|prepare|execute> [options]")
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
	deliveryID := flags.String("delivery-id", "", "ambiguous channel-delivery UUID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *deliveryID == "" || flags.NArg() != 0 {
		return errors.New("inspect requires exactly --delivery-id")
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	summary, err := delivery.InspectReconciliation(ctx, pool, *deliveryID)
	if err != nil {
		return err
	}
	return printJSON(summary)
}

func runPrepare(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	deliveryID := flags.String("delivery-id", "", "ambiguous channel-delivery UUID")
	outcome := flags.String("outcome", "", "confirmed_delivered, confirmed_not_delivered or closed_no_retry")
	reason := flags.String("reason-code", "", "bounded reconciliation reason code")
	evidenceFile := flags.String("external-evidence-file", "", "private provider or incident evidence file")
	output := flags.String("output", "", "new mode-0600 canonical authorization JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *deliveryID == "" || *outcome == "" || *reason == "" || *evidenceFile == "" || *output == "" || flags.NArg() != 0 {
		return errors.New("prepare requires --delivery-id, --outcome, --reason-code, --external-evidence-file and --output")
	}
	evidence, err := readPrivateFile(*evidenceFile)
	if err != nil {
		return fmt.Errorf("read external evidence: %w", err)
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	summary, err := delivery.InspectReconciliation(ctx, pool, *deliveryID)
	if err != nil {
		return err
	}
	auth, err := delivery.NewReconciliationAuthorization(summary, *outcome, *reason, digest(evidence), nowUTC())
	if err != nil {
		return err
	}
	body, err := delivery.CanonicalReconciliationAuthorization(auth)
	if err != nil {
		return err
	}
	if err := writeExclusive(*output, body); err != nil {
		return err
	}
	return printJSON(map[string]any{
		"status": "authorization_prepared", "authorization_id": auth.AuthorizationID,
		"delivery_id": auth.DeliveryID, "generation": auth.ExpectedGeneration,
		"outcome": auth.Outcome, "authorization_digest": digest(body), "expires_at": auth.ExpiresAt,
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
	var auth delivery.ReconciliationAuthorization
	if err := decodeStrictJSON(authBody, &auth); err != nil {
		return fmt.Errorf("decode authorization: %w", err)
	}
	canonical, err := delivery.CanonicalReconciliationAuthorization(auth)
	if err != nil {
		return fmt.Errorf("validate authorization: %w", err)
	}
	if !bytes.Equal(authBody, canonical) {
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
	keyringPath := strings.TrimSpace(os.Getenv("MULTICA_CHANNEL_DELIVERY_RECONCILIATION_PUBLIC_KEYS_FILE"))
	if keyringPath == "" {
		return errors.New("MULTICA_CHANNEL_DELIVERY_RECONCILIATION_PUBLIC_KEYS_FILE is required")
	}
	keyringBody, err := readTrustedPublicFile(keyringPath)
	if err != nil {
		return fmt.Errorf("read reconciliation public keyring: %w", err)
	}
	keyring, err := delivery.DecodeReconciliationPublicKeyring(keyringBody)
	if err != nil {
		return err
	}
	pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	receipt, err := delivery.ExecuteReconciliation(ctx, pool, delivery.ReconciliationExecuteInput{
		Authorization: auth, PublicKeys: keyring, Now: nowUTC(),
		Requester: delivery.ReconciliationSignature{KeyID: *requesterKeyID, Value: requesterSignature},
		Approver:  delivery.ReconciliationSignature{KeyID: *approverKeyID, Value: approverSignature},
	})
	if err != nil {
		return err
	}
	return printJSON(receipt)
}

func openDatabase(ctx context.Context) (*pgxpool.Pool, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required; defaults are forbidden for reconciliation")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
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

func readPrivateFile(path string) ([]byte, error)       { return readSecureFile(path, true) }
func readTrustedPublicFile(path string) ([]byte, error) { return readSecureFile(path, false) }

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
	decoder := json.NewDecoder(bytes.NewReader(body))
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
	parentInfo, err := os.Lstat(filepath.Dir(cleanPath))
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

var nowUTC = func() time.Time { return time.Now().UTC() }
