package rolesourcedr

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
)

func TestArtifactArchiveRoundTripAndTamperDetection(t *testing.T) {
	sourceDir := filepath.Join(t.TempDir(), "source")
	t.Setenv("LOCAL_UPLOAD_DIR", sourceDir)
	source := storage.NewLocalStorageFromEnv()
	body := []byte("immutable-role-source-body")
	digest := sha256Text(body)
	record := ArtifactRecord{
		WorkspaceID: "00000000-0000-4000-8000-000000000001", Digest: digest,
		SizeBytes: int64(len(body)), StorageKey: "role-source-artifacts/00000000-0000-4000-8000-000000000001/" + strings.TrimPrefix(digest, "sha256:"),
	}
	if _, err := source.UploadStream(context.Background(), record.StorageKey, strings.NewReader(string(body)), int64(len(body)), "application/octet-stream", ""); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "artifacts.tar")
	if _, err := WriteArtifactArchive(context.Background(), archive, []ArtifactRecord{record}, source, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	restoreDir := filepath.Join(t.TempDir(), "restore")
	t.Setenv("LOCAL_UPLOAD_DIR", restoreDir)
	restore := storage.NewLocalStorageFromEnv()
	if err := RestoreArtifactArchive(context.Background(), archive, []ArtifactRecord{record}, restore); err != nil {
		t.Fatal(err)
	}
	if err := RestoreArtifactArchive(context.Background(), archive, []ArtifactRecord{record}, restore); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	reader, err := restore.GetReader(context.Background(), record.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := io.ReadAll(reader)
	reader.Close()
	if string(restored) != string(body) {
		t.Fatalf("restored body = %q", restored)
	}

	tampered := append([]byte(nil), body...)
	tampered[0] ^= 1
	if _, err := source.UploadStream(context.Background(), record.StorageKey, strings.NewReader(string(tampered)), int64(len(tampered)), "application/octet-stream", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArtifactArchive(context.Background(), filepath.Join(t.TempDir(), "bad.tar"), []ArtifactRecord{record}, source, time.Unix(1, 0)); err == nil {
		t.Fatal("tampered object was accepted into backup archive")
	}
}

func TestCommitmentIsOrderStableAndLengthFramed(t *testing.T) {
	if commitment([]string{"a", "bc"}) != commitment([]string{"bc", "a"}) {
		t.Fatal("commitment is not order stable")
	}
	if commitment([]string{"a", "bc"}) == commitment([]string{"ab", "c"}) {
		t.Fatal("commitment is not length framed")
	}
}

func TestSameDatabaseStateIgnoresBackupIdentityButNotData(t *testing.T) {
	base := Manifest{ContractVersion: ContractVersion, MigrationCount: 1, MigrationRoot: digestBytes([]byte("m")), Tables: []TableSummary{{Name: "role_source", RowCount: 1, Commitment: digestBytes([]byte("t"))}}, Artifacts: ArtifactSummary{Commitment: digestBytes(nil)}}
	copy := base
	copy.BackupID = "another"
	copy.CreatedAt = time.Now()
	copy.KeyRequirements.SecretTransferKeyIDs = []string{"expired-after-backup"}
	if !SameDatabaseState(base, copy) {
		t.Fatal("backup identity or current-time key advice changed authoritative state comparison")
	}
	copy.Tables = append([]TableSummary(nil), copy.Tables...)
	copy.Tables[0].RowCount++
	if SameDatabaseState(base, copy) {
		t.Fatal("table change was not detected")
	}
}

func TestTableCommitmentPinsDatabaseRendering(t *testing.T) {
	body := mustRead(t, "manifest.go")
	for _, setting := range []string{"TimeZone", "bytea_output", "DateStyle", "IntervalStyle"} {
		if !strings.Contains(body, "set_config('"+setting+"'") {
			t.Errorf("table commitments do not pin PostgreSQL %s rendering", setting)
		}
	}
}

func TestRelationalVerifierProtectsRestorableAndAllowsIntentionalHistoryPrune(t *testing.T) {
	for _, required := range []string{
		"current_snapshot_missing", "artifact_edge_ledger_missing", "task_pin_snapshot_missing",
		"mapping_snapshot_missing", "capability_snapshot_missing", "audit_sequence_mismatch",
		"legal_hold_snapshot_missing", "retention_candidate_snapshot_missing", "scan_source_missing",
		"approval_plan_missing", "apply_failure_approval_missing", "mapping_autopilot_target_missing",
		"active_secret_approval_missing", "runtime_attestation_observation_runtime_missing",
		"artifact_integrity_missing", "artifact_integrity_ledger_missing",
	} {
		if !strings.Contains(relationalInvariantQuery, required) {
			t.Errorf("relational verifier omits %s", required)
		}
	}
	verifyBody := mustRead(t, "verify.go")
	for _, required := range []string{"verifySourceConfigSummaries", "verifyRuntimeAttestations", "ValidateRoleSourceConfigAttestation"} {
		if !strings.Contains(verifyBody, required) {
			t.Errorf("semantic verifier omits %s", required)
		}
	}
	for _, intentionallyPrunable := range []string{"plan_target_snapshot_missing", "plan_base_snapshot_missing", "apply_snapshot_missing"} {
		if strings.Contains(relationalInvariantQuery, intentionallyPrunable) {
			t.Errorf("historical retention would make every valid restore fail: %s", intentionallyPrunable)
		}
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestTableInventoryCoversEveryRoleSourceTableMigration(t *testing.T) {
	root := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(body))
		marker := "create table role_source"
		for offset := 0; ; {
			index := strings.Index(lower[offset:], marker)
			if index < 0 {
				break
			}
			start := offset + index + len("create table ")
			end := start
			for end < len(lower) && ((lower[end] >= 'a' && lower[end] <= 'z') || lower[end] == '_') {
				end++
			}
			created[lower[start:end]] = true
			offset = end
		}
	}
	for table := range created {
		if !knownTable(table) {
			t.Errorf("DR manifest omits table %s", table)
		}
	}
	for _, table := range TableNames() {
		if !created[table] {
			t.Errorf("DR manifest names unknown table %s", table)
		}
	}
}

func TestArtifactInventoryRejectsDuplicatesAndOversize(t *testing.T) {
	record := ArtifactRecord{WorkspaceID: "00000000-0000-4000-8000-000000000001", Digest: digestBytes(nil), StorageKey: "role-source-artifacts/00000000-0000-4000-8000-000000000001/" + strings.TrimPrefix(digestBytes(nil), "sha256:")}
	if err := validateArtifactInventory([]ArtifactRecord{record, record}); err == nil {
		t.Fatal("duplicate artifact accepted")
	}
	record.SizeBytes = 1<<30 + 1
	if err := validateArtifactInventory([]ArtifactRecord{record}); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}

func sha256Text(body []byte) string {
	return digestBytes(body)
}
