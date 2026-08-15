package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type purgeReceiptPostgresStorage struct{ calls int }

func (s *purgeReceiptPostgresStorage) DeleteObject(context.Context, string) error { return nil }

func (s *purgeReceiptPostgresStorage) PurgeObjectWithResult(context.Context, string) (storage.PermanentPurgeResult, error) {
	s.calls++
	result := storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendS3, Mode: storage.PermanentPurgeModeVersions,
		VerifiedAbsent: true,
	}
	if s.calls == 1 {
		result.VersionsDeleted = 2
		result.DeleteMarkersDeleted = 1
		result.ObservedBytesDeleted = 8192
	}
	return result, nil
}

type ambiguousPurgeReceiptPostgresStorage struct{ calls int }

func (s *ambiguousPurgeReceiptPostgresStorage) DeleteObject(context.Context, string) error {
	return nil
}

func (s *ambiguousPurgeReceiptPostgresStorage) PurgeObjectWithResult(context.Context, string) (storage.PermanentPurgeResult, error) {
	s.calls++
	result := storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendS3, Mode: storage.PermanentPurgeModeVersions,
	}
	if s.calls == 1 {
		result.VersionsDeleted = 2
		result.DeleteMarkersDeleted = 1
		result.ObservedBytesDeleted = 8192
		return result, &storage.PermanentPurgeError{
			Operation: "retained-version delete", MayHaveMutated: true,
			Err: errors.New("response lost after provider mutation"),
		}
	}
	result.VerifiedAbsent = true
	return result, nil
}

func TestRoleSourceArtifactPurgeReceiptStateMachinePostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	workspaceID := uuid.New()
	intentID := uuid.New()
	storageKey := "role-source-artifacts/" + workspaceID.String() + "/" + repeatPurgeReceiptHex("a")
	artifactDigest := "sha256:" + repeatPurgeReceiptHex("a")
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source_artifact_delete_intent (
  id,workspace_id,storage_key,artifact_digest,size_bytes,reason
) VALUES ($1,$2,$3,$4,4096,'unreachable')
`, intentID, workspaceID, storageKey, artifactDigest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM role_source_artifact_delete_intent WHERE id=$1`, intentID)
	})

	objectStorage := &purgeReceiptPostgresStorage{}
	reconciler := &RoleSourceArtifactReconciler{
		Queries: db.New(pool), Storage: objectStorage, DRGuard: &roleSourceGuardProbe{},
	}
	for pass := 0; pass < len(roleSourceArtifactGCTombstoneSchedule)+1; pass++ {
		reconciler.RunOnce(ctx)
		if pass < len(roleSourceArtifactGCTombstoneSchedule) {
			if _, err := pool.Exec(ctx, `
UPDATE role_source_artifact_delete_intent
SET next_attempt_at=now()-interval '1 second'
WHERE id=$1 AND state='tombstoned'
`, intentID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if objectStorage.calls != len(roleSourceArtifactGCTombstoneSchedule)+1 {
		t.Fatalf("permanent purge calls=%d", objectStorage.calls)
	}

	var intentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_artifact_delete_intent WHERE id=$1`, intentID).Scan(&intentCount); err != nil || intentCount != 0 {
		t.Fatalf("completed intent count=%d err=%v", intentCount, err)
	}
	receipts, err := db.New(pool).ListWorkspaceRoleSourceArtifactPurgeReceipts(ctx, db.ListWorkspaceRoleSourceArtifactPurgeReceiptsParams{
		WorkspaceID: pgUUIDFromNative(workspaceID), ResultLimit: 10,
	})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("purge receipts=%d err=%v", len(receipts), err)
	}
	receipt := receipts[0]
	if receipt.IntentID.Bytes != intentID || receipt.SuccessfulPasses != 5 || receipt.DeletedVersions != 2 ||
		receipt.DeletedDeleteMarkers != 1 || receipt.ObservedDeletedBytes != 8192 ||
		receipt.LogicalBytesConfirmedAbsent != 4096 || !receipt.AbsenceVerified ||
		receipt.ContractVersion != roleSourceArtifactPurgeReceiptContractV2 || receipt.AmbiguousAttempts != 0 ||
		!receipt.ProviderEvidenceComplete {
		t.Fatalf("purge receipt = %+v", receipt)
	}
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	totals, err := db.New(pool).GetWorkspaceRoleSourceArtifactPurgeReceiptTotals(ctx, pgUUIDFromNative(workspaceID))
	if err != nil || totals.ReceiptCount != 1 || totals.LogicalBytesConfirmedAbsent != 4096 ||
		totals.ObservedDeletedBytes != 8192 || totals.DeletedVersions != 2 || totals.DeletedDeleteMarkers != 1 ||
		totals.AmbiguousAttempts != 0 || totals.IncompleteProviderEvidenceReceipts != 0 {
		t.Fatalf("purge receipt totals=%+v err=%v", totals, err)
	}

	_, err = pool.Exec(ctx, `UPDATE role_source_artifact_purge_receipt SET observed_deleted_bytes=observed_deleted_bytes WHERE intent_id=$1`, intentID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23000" {
		t.Fatalf("immutable purge receipt update err=%v", err)
	}
	_, err = pool.Exec(ctx, `DELETE FROM role_source_artifact_purge_receipt WHERE intent_id=$1`, intentID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23000" {
		t.Fatalf("immutable purge receipt delete err=%v", err)
	}
}

func TestRoleSourceArtifactPurgeReceiptAmbiguitySurvivesRetryPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	workspaceID := uuid.New()
	intentID := uuid.New()
	storageKey := "role-source-artifacts/" + workspaceID.String() + "/" + repeatPurgeReceiptHex("b")
	artifactDigest := "sha256:" + repeatPurgeReceiptHex("b")
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source_artifact_delete_intent (
  id,workspace_id,storage_key,artifact_digest,size_bytes,reason
) VALUES ($1,$2,$3,$4,4096,'unreachable')
`, intentID, workspaceID, storageKey, artifactDigest); err != nil {
		t.Fatal(err)
	}

	objectStorage := &ambiguousPurgeReceiptPostgresStorage{}
	reconciler := &RoleSourceArtifactReconciler{
		Queries: db.New(pool), Storage: objectStorage, DRGuard: &roleSourceGuardProbe{},
	}
	reconciler.RunOnce(ctx)
	var state string
	var ambiguousAttempts, successfulPasses int32
	if err := pool.QueryRow(ctx, `
SELECT state,purge_ambiguous_attempts,purge_passes
FROM role_source_artifact_delete_intent WHERE id=$1
`, intentID).Scan(&state, &ambiguousAttempts, &successfulPasses); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || ambiguousAttempts != 1 || successfulPasses != 0 {
		t.Fatalf("ambiguous release state=%s ambiguity=%d passes=%d", state, ambiguousAttempts, successfulPasses)
	}

	for pass := 0; pass < len(roleSourceArtifactGCTombstoneSchedule)+1; pass++ {
		if _, err := pool.Exec(ctx, `
UPDATE role_source_artifact_delete_intent
SET next_attempt_at=now()-interval '1 second'
WHERE id=$1 AND state IN ('pending','tombstoned')
`, intentID); err != nil {
			t.Fatal(err)
		}
		reconciler.RunOnce(ctx)
	}
	if objectStorage.calls != len(roleSourceArtifactGCTombstoneSchedule)+2 {
		t.Fatalf("permanent purge calls=%d", objectStorage.calls)
	}

	receipts, err := db.New(pool).ListWorkspaceRoleSourceArtifactPurgeReceipts(ctx, db.ListWorkspaceRoleSourceArtifactPurgeReceiptsParams{
		WorkspaceID: pgUUIDFromNative(workspaceID), ResultLimit: 10,
	})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("purge receipts=%d err=%v", len(receipts), err)
	}
	receipt := receipts[0]
	if receipt.ContractVersion != roleSourceArtifactPurgeReceiptContractV2 || receipt.AmbiguousAttempts != 1 ||
		receipt.ProviderEvidenceComplete || receipt.SuccessfulPasses != 5 || receipt.DeletedVersions != 0 ||
		receipt.DeletedDeleteMarkers != 0 || receipt.ObservedDeletedBytes != 0 ||
		receipt.LogicalBytesConfirmedAbsent != 4096 || !receipt.AbsenceVerified {
		t.Fatalf("ambiguous purge receipt = %+v", receipt)
	}
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	totals, err := db.New(pool).GetWorkspaceRoleSourceArtifactPurgeReceiptTotals(ctx, pgUUIDFromNative(workspaceID))
	if err != nil || totals.ReceiptCount != 1 || totals.AmbiguousAttempts != 1 ||
		totals.IncompleteProviderEvidenceReceipts != 1 || totals.LogicalBytesConfirmedAbsent != 4096 ||
		totals.ObservedDeletedBytes != 0 {
		t.Fatalf("ambiguous purge receipt totals=%+v err=%v", totals, err)
	}
}

func TestRoleSourceArtifactPurgeExpiredLeaseRecordsAmbiguityPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_PURGE_RECEIPT_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	workspaceID := uuid.New()
	intentID := uuid.New()
	storageKey := "role-source-artifacts/" + workspaceID.String() + "/" + repeatPurgeReceiptHex("c")
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source_artifact_delete_intent (
  id,workspace_id,storage_key,artifact_digest,size_bytes,reason
) VALUES ($1,$2,$3,$4,64,'unreachable')
`, intentID, workspaceID, storageKey, "sha256:"+repeatPurgeReceiptHex("c")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM role_source_artifact_delete_intent WHERE id=$1`, intentID)
	})

	queries := db.New(pool)
	firstToken := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	first, err := queries.ClaimNextRoleSourceArtifactDeleteIntent(ctx, db.ClaimNextRoleSourceArtifactDeleteIntentParams{
		LeaseToken: firstToken, LeaseDuration: pgInterval(time.Minute),
	})
	if err != nil || first.ID.Bytes != intentID || first.Attempt != 1 || first.PurgeAmbiguousAttempts != 0 || first.PurgeEvidenceAmbiguous {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_artifact_delete_intent SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, intentID); err != nil {
		t.Fatal(err)
	}
	secondToken := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	second, err := queries.ClaimNextRoleSourceArtifactDeleteIntent(ctx, db.ClaimNextRoleSourceArtifactDeleteIntentParams{
		LeaseToken: secondToken, LeaseDuration: pgInterval(time.Minute),
	})
	if err != nil || second.ID.Bytes != intentID || second.Attempt != 2 || second.PurgeAmbiguousAttempts != 1 || !second.PurgeEvidenceAmbiguous || second.LeaseToken.Bytes != secondToken.Bytes {
		t.Fatalf("expired-lease reclaim=%+v err=%v", second, err)
	}
}

func pgUUIDFromNative(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: true}
}

func repeatPurgeReceiptHex(value string) string {
	result := ""
	for range 64 {
		result += value
	}
	return result
}
