package rolesourcedr

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
)

type faultRestoreStorage struct {
	storage.Storage
	uploadCalls       map[string]int
	responseLossOnce  bool
	responseLost      bool
	cancelAfterUpload int
	cancel            context.CancelFunc
	shortConsume      bool
	uploadFailure     error
	readFailure       error
}

func (s *faultRestoreStorage) UploadStream(ctx context.Context, key string, data io.Reader, size int64, contentType, filename string) (string, error) {
	if s.uploadCalls == nil {
		s.uploadCalls = map[string]int{}
	}
	s.uploadCalls[key]++
	if s.shortConsume {
		_, _ = io.CopyN(io.Discard, data, max(1, size/2))
		return "", nil
	}
	if s.uploadFailure != nil {
		_, _ = io.Copy(io.Discard, data)
		return "", s.uploadFailure
	}
	result, err := s.Storage.(streamUploader).UploadStream(ctx, key, data, size, contentType, filename)
	if err != nil {
		return result, err
	}
	if s.cancelAfterUpload > 0 && totalCalls(s.uploadCalls) == s.cancelAfterUpload {
		s.cancel()
	}
	if s.responseLossOnce && !s.responseLost {
		s.responseLost = true
		return "", errors.New("simulated response loss")
	}
	return result, nil
}

func (s *faultRestoreStorage) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.readFailure != nil {
		return nil, s.readFailure
	}
	return s.Storage.GetReader(ctx, key)
}

func (s *faultRestoreStorage) IsObjectNotFound(err error) bool {
	classifier, ok := s.Storage.(objectNotFoundClassifier)
	return ok && classifier.IsObjectNotFound(err)
}

func totalCalls(calls map[string]int) int {
	total := 0
	for _, count := range calls {
		total += count
	}
	return total
}

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
	archiveDigest, err := FileDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestoreArtifactArchive(context.Background(), archive, archiveDigest, []ArtifactRecord{record}, restore); err != nil {
		t.Fatal(err)
	}
	if err := RestoreArtifactArchive(context.Background(), archive, archiveDigest, []ArtifactRecord{record}, restore); err != nil {
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

func TestRestoreArtifactArchivePreflightsEveryMemberBeforeMutation(t *testing.T) {
	records, bodies := archiveRecords(t, [][]byte{[]byte("first-valid-body"), []byte("second-valid-body")})
	archive := filepath.Join(t.TempDir(), "malformed.tar")
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(file)
	for index, record := range records {
		body := bodies[index]
		if index == 1 {
			body = []byte("second-broken-body")
		}
		name := record.WorkspaceID + "/" + strings.TrimPrefix(record.Digest, "sha256:")
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o400, Size: int64(len(body)), Format: tar.FormatPAX}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := FileDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	restore := newLocalRestore(t)
	err = RestoreArtifactArchive(context.Background(), archive, digest, records, restore)
	if err == nil || !strings.Contains(err.Error(), "inventory") && !strings.Contains(err.Error(), "digest") {
		t.Fatalf("malformed later member error = %v", err)
	}
	if reader, readErr := restore.GetReader(context.Background(), records[0].StorageKey); readErr == nil {
		reader.Close()
		t.Fatal("preflight failure uploaded an earlier valid member")
	}
}

func TestRestoreArtifactArchiveResolvesResponseLossByExactReadback(t *testing.T) {
	archive, digest, records, _ := buildArchiveFixture(t, [][]byte{[]byte("committed-before-response-loss")})
	base := newLocalRestore(t)
	fault := &faultRestoreStorage{Storage: base, responseLossOnce: true}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, fault); err != nil {
		t.Fatalf("response-loss reconciliation failed: %v", err)
	}
	if totalCalls(fault.uploadCalls) != 1 || !fault.responseLost {
		t.Fatalf("upload calls=%v response_lost=%v", fault.uploadCalls, fault.responseLost)
	}
	assertStoredBodies(t, base, records, [][]byte{[]byte("committed-before-response-loss")})
}

func TestRestoreArtifactArchiveInterruptedRunResumesWithoutPlaintextSpool(t *testing.T) {
	bodies := [][]byte{[]byte("body-one"), []byte("body-two"), []byte("body-three")}
	archive, digest, records, _ := buildArchiveFixture(t, bodies)
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir)
	base := newLocalRestore(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := &faultRestoreStorage{Storage: base, cancelAfterUpload: 1, cancel: cancel}
	err := RestoreArtifactArchive(ctx, archive, digest, records, first)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted restore error = %v", err)
	}
	if totalCalls(first.uploadCalls) != 1 {
		t.Fatalf("first run uploads = %v", first.uploadCalls)
	}
	if entries, err := os.ReadDir(spoolDir); err != nil || len(entries) != 0 {
		t.Fatalf("plaintext restore spool residue = %v, err=%v", entries, err)
	}
	retry := &faultRestoreStorage{Storage: base}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, retry); err != nil {
		t.Fatalf("resume restore: %v", err)
	}
	if totalCalls(retry.uploadCalls) != len(records)-1 {
		t.Fatalf("retry re-uploaded committed body or missed work: %v", retry.uploadCalls)
	}
	assertStoredBodies(t, base, records, bodies)
}

func TestRestoreArtifactArchiveRejectsShortConsumerAndReadFailure(t *testing.T) {
	archive, digest, records, _ := buildArchiveFixture(t, [][]byte{[]byte("fixed-length-body")})
	base := newLocalRestore(t)
	short := &faultRestoreStorage{Storage: base, shortConsume: true}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, short); err == nil || !strings.Contains(err.Error(), "did not consume") {
		t.Fatalf("short consumer error = %v", err)
	}

	readFailure := &faultRestoreStorage{Storage: base, readFailure: errors.New("provider unavailable")}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, readFailure); err == nil || !strings.Contains(err.Error(), "read existing artifact") || strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("read failure error = %v", err)
	}
	if totalCalls(readFailure.uploadCalls) != 0 {
		t.Fatalf("read failure triggered overwrite: %v", readFailure.uploadCalls)
	}

	uploadFailure := &faultRestoreStorage{Storage: base, uploadFailure: errors.New("provider response contains sensitive details")}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, uploadFailure); err == nil || !strings.Contains(err.Error(), "upload failed") || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("upload failure error = %v", err)
	}
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, nil); err == nil || !strings.Contains(err.Error(), "storage is unavailable") {
		t.Fatalf("nil storage error = %v", err)
	}
}

func TestRestoreArtifactArchiveSupportsLedgerValidEmptyBody(t *testing.T) {
	archive, digest, records, bodies := buildArchiveFixture(t, [][]byte{{}})
	restore := newLocalRestore(t)
	if err := RestoreArtifactArchive(context.Background(), archive, digest, records, restore); err != nil {
		t.Fatalf("restore empty artifact: %v", err)
	}
	assertStoredBodies(t, restore, records, bodies)
}

func buildArchiveFixture(t *testing.T, bodies [][]byte) (string, string, []ArtifactRecord, [][]byte) {
	t.Helper()
	records, copiedBodies := archiveRecords(t, bodies)
	sourceDir := filepath.Join(t.TempDir(), "source")
	t.Setenv("LOCAL_UPLOAD_DIR", sourceDir)
	source := storage.NewLocalStorageFromEnv()
	for index, record := range records {
		body := copiedBodies[index]
		if _, err := source.UploadStream(context.Background(), record.StorageKey, strings.NewReader(string(body)), int64(len(body)), "application/octet-stream", ""); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "artifacts.tar")
	digest, err := WriteArtifactArchive(context.Background(), archive, records, source, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	return archive, digest, records, copiedBodies
}

func archiveRecords(t *testing.T, bodies [][]byte) ([]ArtifactRecord, [][]byte) {
	t.Helper()
	workspaceID := "00000000-0000-4000-8000-000000000001"
	records := make([]ArtifactRecord, 0, len(bodies))
	copied := make([][]byte, 0, len(bodies))
	for _, original := range bodies {
		body := append([]byte(nil), original...)
		digest := sha256Text(body)
		records = append(records, ArtifactRecord{
			WorkspaceID: workspaceID,
			Digest:      digest,
			SizeBytes:   int64(len(body)),
			StorageKey:  "role-source-artifacts/" + workspaceID + "/" + strings.TrimPrefix(digest, "sha256:"),
		})
		copied = append(copied, body)
	}
	return records, copied
}

func newLocalRestore(t *testing.T) *storage.LocalStorage {
	t.Helper()
	t.Setenv("LOCAL_UPLOAD_DIR", filepath.Join(t.TempDir(), "restore"))
	return storage.NewLocalStorageFromEnv()
}

func assertStoredBodies(t *testing.T, store storage.Storage, records []ArtifactRecord, bodies [][]byte) {
	t.Helper()
	for index, record := range records {
		reader, err := store.GetReader(context.Background(), record.StorageKey)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || string(body) != string(bodies[index]) {
			t.Fatalf("stored body %d = %q, err=%v", index, body, err)
		}
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
		"outbox_apply_commitment_mismatch", "outbox_audit_mismatch", "outbox_replay_count_mismatch",
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
