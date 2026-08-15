package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRoleSourceArtifactPurgeReceiptIsDeterministicAndSelfVerifying(t *testing.T) {
	intent := db.RoleSourceArtifactDeleteIntent{
		ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, WorkspaceID: pgtype.UUID{Bytes: uuid.New(), Valid: true},
		StorageKey: "role-source-artifacts/workspace/abcdef", ArtifactDigest: "sha256:" + repeatServiceHex("a"),
		SizeBytes: 4096, Reason: "unreachable", PurgeBackend: pgtype.Text{String: storage.PermanentPurgeBackendS3, Valid: true},
		PurgeMode: pgtype.Text{String: storage.PermanentPurgeModeVersions, Valid: true}, PurgePasses: 4,
		DeletedVersions: 3, DeletedDeleteMarkers: 2, ObservedDeletedBytes: 12288,
		PurgeAmbiguousAttempts: 2,
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
	if first.ContractVersion != roleSourceArtifactPurgeReceiptContractV2 || first.AmbiguousAttempts != 2 || first.ProviderEvidenceComplete {
		t.Fatalf("receipt ambiguity evidence = %+v", first)
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
		ContractVersion: first.ContractVersion, AmbiguousAttempts: first.AmbiguousAttempts,
		ProviderEvidenceComplete: first.ProviderEvidenceComplete,
		CompletedAt:              first.CompletedAt, ReceiptDigest: first.ReceiptDigest,
	}
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err != nil {
		t.Fatalf("verify receipt: %v", err)
	}
	receipt.ObservedDeletedBytes++
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err == nil {
		t.Fatal("mutated purge receipt commitment verified")
	}
}

func TestRoleSourceArtifactPurgeReceiptV1RemainsVerifiable(t *testing.T) {
	intentID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	workspaceID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	completedAt := time.Date(2026, 8, 15, 1, 2, 3, 456789000, time.UTC)
	commitment := roleSourceArtifactPurgeReceiptCommitmentV1{
		ContractVersion: roleSourceArtifactPurgeReceiptContractV1,
		IntentID:        util.UUIDToString(intentID), WorkspaceID: util.UUIDToString(workspaceID),
		StorageKeyDigest: "sha256:" + repeatServiceHex("c"), ArtifactDigest: "sha256:" + repeatServiceHex("d"),
		SizeBytes: 64, Reason: "unreachable", StorageBackend: storage.PermanentPurgeBackendS3,
		PurgeMode: storage.PermanentPurgeModeVersions, SuccessfulPasses: 5,
		DeletedVersions: 1, DeletedDeleteMarkers: 1, ObservedDeletedBytes: 64,
		LogicalBytesConfirmedAbsent: 64, AbsenceVerified: true,
		CompletedAt: completedAt.Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(commitment)
	if err != nil {
		t.Fatal(err)
	}
	receipt := db.RoleSourceArtifactPurgeReceipt{
		ID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, IntentID: intentID, WorkspaceID: workspaceID,
		StorageKeyDigest: commitment.StorageKeyDigest, ArtifactDigest: commitment.ArtifactDigest,
		SizeBytes: 64, Reason: "unreachable", StorageBackend: storage.PermanentPurgeBackendS3,
		PurgeMode: storage.PermanentPurgeModeVersions, SuccessfulPasses: 5,
		DeletedVersions: 1, DeletedDeleteMarkers: 1, ObservedDeletedBytes: 64,
		LogicalBytesConfirmedAbsent: 64, AbsenceVerified: true,
		CompletedAt:   pgtype.Timestamptz{Time: completedAt, Valid: true},
		ReceiptDigest: digestPurgeReceiptBytes(body), ContractVersion: roleSourceArtifactPurgeReceiptContractV1,
		ProviderEvidenceComplete: true,
	}
	if err := VerifyRoleSourceArtifactPurgeReceipt(receipt); err != nil {
		t.Fatalf("verify v1 receipt after v2 migration: %v", err)
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
