package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesourcedr"
)

func TestRunRequiresExplicitCommandAndDatabase(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("missing command accepted")
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PROVIDER", "private_key")
	t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_KEY_ID", "")
	t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PRIVATE_KEY", "")
	t.Setenv("MULTICA_ROLE_SOURCE_DR_AWS_KMS_KEY_ID", "")
	t.Setenv("MULTICA_ROLE_SOURCE_DR_SIGNING_PUBLIC_KEY", "")
	err := run(context.Background(), []string{"backup", "--allow-unsigned-manifest", "--output-dir", t.TempDir() + "/new"})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("backup error = %v", err)
	}
}

func TestBoundedOutput(t *testing.T) {
	if got := boundedOutput([]byte(strings.Repeat("x", 5000))); len(got) != 2048 {
		t.Fatalf("bounded output length = %d", len(got))
	}
	if got := boundedOutput([]byte("\n  pg_dump: safe diagnostic  \n")); got != "pg_dump: safe diagnostic" {
		t.Fatalf("bounded output = %q", got)
	}
}

func TestPGDumpArgsUseDatabaseUserWithoutPassword(t *testing.T) {
	args, password, err := buildPGDumpArgs("postgres://dr_operator:do-not-leak@database.example/restore?sslmode=require&pool_max_conns=40", "snapshot-1", "/private/database.dump")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"--username=dr_operator", "--dbname=postgres://dr_operator@database.example/restore?sslmode=require", "--snapshot=snapshot-1", "--file=/private/database.dump"} {
		if !strings.Contains(joined, required) {
			t.Errorf("pg_dump args omit %s: %s", required, joined)
		}
	}
	if strings.Contains(joined, "do-not-leak") || strings.Contains(joined, "pool_max_conns") {
		t.Fatal("pg_dump arguments expose the database password")
	}
	if password != "do-not-leak" {
		t.Fatal("pg_dump password was not separated from process arguments")
	}
	environment := replaceEnvironmentValue([]string{"PATH=/bin", "PGPASSWORD=stale"}, "PGPASSWORD", password)
	if strings.Join(environment, "|") != "PATH=/bin|PGPASSWORD=do-not-leak" {
		t.Fatalf("pg_dump environment = %v", environment)
	}
	if _, _, err := buildPGDumpArgs("host=database.example user=dr_operator password=do-not-leak dbname=restore", "snapshot-1", "/private/database.dump"); err == nil {
		t.Fatal("non-URL DATABASE_URL was accepted for pg_dump")
	}
}

func TestUnsignedManifestIsDevelopmentOptIn(t *testing.T) {
	if err := signBackupManifest(context.Background(), nil, false); err == nil {
		t.Fatal("nil manifest accepted for signing")
	}
	manifest := emptyValidManifest(time.Now())
	if err := signBackupManifest(context.Background(), &manifest, false); err == nil {
		t.Fatal("unsigned production backup accepted")
	}
	if err := signBackupManifest(context.Background(), &manifest, true); err != nil {
		t.Fatal(err)
	}
	if !manifest.UnsignedDevelopment {
		t.Fatal("development-only manifest was not durably marked unsafe")
	}
}

func emptyValidManifest(now time.Time) rolesourcedr.Manifest {
	const emptyDigest = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	tables := make([]rolesourcedr.TableSummary, 0, len(rolesourcedr.TableNames()))
	for _, name := range rolesourcedr.TableNames() {
		tables = append(tables, rolesourcedr.TableSummary{Name: name, Commitment: emptyDigest})
	}
	return rolesourcedr.Manifest{
		ContractVersion: rolesourcedr.ContractVersion, BackupID: "00000000-0000-4000-8000-000000000001", CreatedAt: now.UTC(),
		DatabaseMajorVersion: 17, MigrationCount: 1, MigrationRoot: emptyDigest, Tables: tables,
		Artifacts: rolesourcedr.ArtifactSummary{Commitment: emptyDigest}, DatabaseDumpDigest: emptyDigest, ArtifactArchiveDigest: emptyDigest,
	}
}

func TestRecoveryKeyringFailsClosed(t *testing.T) {
	t.Setenv("MULTICA_ROLE_SOURCE_SECRET_KEY", "")
	t.Setenv("MULTICA_ROLE_SOURCE_SECRET_KEY_ID", "unexpected")
	if _, err := loadKeyringStrict(); err == nil {
		t.Fatal("secret key id without key material accepted")
	}
	t.Setenv("MULTICA_ROLE_SOURCE_SECRET_KEY_ID", "")
	t.Setenv("MULTICA_ROLE_SOURCE_SECRET_PREVIOUS_KEYS", `{"v1":"not-base64"}`)
	if _, err := loadKeyringStrict(); err == nil {
		t.Fatal("malformed previous recovery key accepted")
	}
	t.Setenv("MULTICA_ROLE_SOURCE_SECRET_PREVIOUS_KEYS", `{"v1":"first","v1":"second"}`)
	if _, err := loadKeyringStrict(); err == nil {
		t.Fatal("duplicate previous recovery key id accepted")
	}
}

func TestGenerateSigningKeyUsesExclusiveFiles(t *testing.T) {
	privatePath := t.TempDir() + "/private.key"
	publicPath := t.TempDir() + "/public.key"
	args := []string{"--key-id", "backup-v1", "--private-key-file", privatePath, "--public-key-file", publicPath}
	if err := runGenerateSigningKey(args); err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o", privateInfo.Mode().Perm())
	}
	if err := runGenerateSigningKey(args); err == nil {
		t.Fatal("key generator overwrote existing key files")
	}
}

func TestTrustedManifestKeyMapFailsClosed(t *testing.T) {
	t.Setenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS", `{"backup-v1":"not-base64"}`)
	if _, err := loadManifestTrustStrict(); err == nil {
		t.Fatal("malformed trusted key map accepted")
	}
	publicKey := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	t.Setenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS", fmt.Sprintf(`{"backup-v1":%q,"backup-v1":%q}`, publicKey, publicKey))
	if _, err := loadManifestTrustStrict(); err == nil {
		t.Fatal("duplicate trusted signer key id accepted")
	}
	t.Setenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS", fmt.Sprintf(`{"backup-v1":%q} trailing`, publicKey))
	if _, err := loadManifestTrustStrict(); err == nil {
		t.Fatal("trusted signer map trailing data accepted")
	}
}

func TestTrustedManifestKeyMapSupportsBoundedLongRetentionRotation(t *testing.T) {
	encoded := make(map[string]string, 64)
	publicKey := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	for index := 0; index < 64; index++ {
		encoded[fmt.Sprintf("backup-%03d", index)] = publicKey
	}
	body, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS", string(body))
	trusted, err := loadManifestTrustStrict()
	if err != nil {
		t.Fatal(err)
	}
	if len(trusted) != 64 {
		t.Fatalf("trusted key count = %d", len(trusted))
	}

	for index := 64; index <= maxTrustedManifestKeys; index++ {
		encoded[fmt.Sprintf("backup-%03d", index)] = publicKey
	}
	body, err = json.Marshal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_ROLE_SOURCE_DR_TRUSTED_PUBLIC_KEYS", string(body))
	if _, err := loadManifestTrustStrict(); err == nil {
		t.Fatal("oversized trusted signer history accepted")
	}
}
