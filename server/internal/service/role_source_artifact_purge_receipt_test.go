package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRoleSourceArtifactPurgeReceiptIsDeterministicAndSelfVerifying(t *testing.T) {
	intent := db.RoleSourceArtifactDeleteIntent{
		ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		StorageKey: "role-source-artifacts/workspace/abcdef", ArtifactDigest: "sha256:" + repeatServiceHex("a"),
		SizeBytes: 4096, Reason: "unreachable", PurgeBackend: pgtype.Text{String: storage.PermanentPurgeBackendS3, Valid: true},
		PurgeMode: pgtype.Text{String: storage.PermanentPurgeModeVersions, Valid: true}, PurgePasses: 4,
		DeletedVersions: 3, DeletedDeleteMarkers: 2, ObservedDeletedBytes: 12288,
	}
	token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	result := storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendS3, Mode: storage.PermanentPurgeModeVersions,
		VersionsDeleted: 1, DeleteMarkersDeleted: 1, ObservedBytesDeleted: 4096, VerifiedAbsent: true,
	}
	completedAt := time.Date(2026, 8, 15, 11, 12, 13, 456789000, time.UTC)
	first, err := buildRoleSourceArtifactPurgeReceiptParams(intent, token, result, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildRoleSourceArtifactPurgeReceiptParams(intent, token, result, completedAt)
	if err != nil || first.ReceiptDigest != second.ReceiptDigest {
		t.Fatalf("receipt digest is not deterministic: first=%q second=%q err=%v", first.ReceiptDigest, second.ReceiptDigest, err)
	}
	if first.SuccessfulPasses != 5 || first.TotalDeletedVersions != 4 || first.TotalDeletedDeleteMarkers != 3 || first.TotalObservedDeletedBytes != 16384 {
		t.Fatalf("receipt aggregates = %+v", first)
	}
	if first.StorageKeyDigest == intent.StorageKey || first.StorageKeyDigest != digestPurgeReceiptValue(intent.StorageKey) {
		t.Fatalf("storage key commitment = %q", first.StorageKeyDigest)
	}

	receipt := db.RoleSourceArtifactPurgeReceipt{
		ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, IntentID: intent.ID, WorkspaceID: intent.WorkspaceID,
		StorageKeyDigest: first.StorageKeyDigest, ArtifactDigest: intent.ArtifactDigest, SizeBytes: intent.SizeBytes,
		Reason: intent.Reason, StorageBackend: result.Backend, PurgeMode: result.Mode,
		SuccessfulPasses: first.SuccessfulPasses, DeletedVersions: first.TotalDeletedVersions,
		DeletedDeleteMarkers: first.TotalDeletedDeleteMarkers, ObservedDeletedBytes: first.TotalObservedDeletedBytes,
		LogicalBytesConfirmedAbsent: intent.SizeBytes, AbsenceVerified: true,
		CompletedAt: first.CompletedAt, ReceiptDigest: first.ReceiptDigest,
	}
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err != nil {
		t.Fatalf("verify receipt: %v", err)
	}
	receipt.ObservedDeletedBytes++
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err == nil {
		t.Fatal("mutated purge receipt commitment verified")
	}
}

func TestRoleSourceArtifactPurgeReceiptRejectsUnverifiedOrChangedProvider(t *testing.T) {
	intent := db.RoleSourceArtifactDeleteIntent{
		ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		StorageKey: "role-source-artifacts/workspace/abcdef", ArtifactDigest: "sha256:" + repeatServiceHex("b"),
		SizeBytes: 1, Reason: "workspace_deleted", PurgeBackend: pgtype.Text{String: storage.PermanentPurgeBackendLocal, Valid: true},
		PurgeMode: pgtype.Text{String: storage.PermanentPurgeModeCurrent, Valid: true}, PurgePasses: 4,
	}
	token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := buildRoleSourceArtifactPurgeReceiptParams(intent, token, storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendLocal, Mode: storage.PermanentPurgeModeCurrent,
	}, time.Now()); err == nil {
		t.Fatal("unverified permanent purge produced a receipt")
	}
	if _, err := buildRoleSourceArtifactPurgeReceiptParams(intent, token, storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendS3, Mode: storage.PermanentPurgeModeVersions, VerifiedAbsent: true,
	}, time.Now()); err == nil {
		t.Fatal("provider change during tombstone tail produced a receipt")
	}
}

func repeatServiceHex(value string) string {
	result := ""
	for range 64 {
		result += value
	}
	return result
}
