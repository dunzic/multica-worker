package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	roleSourceArtifactPurgeReceiptContractV1 = "role-source-artifact-purge-receipt-v1"
	roleSourceArtifactPurgeReceiptContractV2 = "role-source-artifact-purge-receipt-v2"
)

type roleSourceArtifactPurgeReceiptCommitmentV1 struct {
	ContractVersion             string `json:"contract_version"`
	IntentID                    string `json:"intent_id"`
	WorkspaceID                 string `json:"workspace_id"`
	StorageKeyDigest            string `json:"storage_key_digest"`
	ArtifactDigest              string `json:"artifact_digest"`
	SizeBytes                   int64  `json:"size_bytes"`
	Reason                      string `json:"reason"`
	StorageBackend              string `json:"storage_backend"`
	PurgeMode                   string `json:"purge_mode"`
	SuccessfulPasses            int32  `json:"successful_passes"`
	DeletedVersions             int64  `json:"deleted_versions"`
	DeletedDeleteMarkers        int64  `json:"deleted_delete_markers"`
	ObservedDeletedBytes        int64  `json:"observed_deleted_bytes"`
	LogicalBytesConfirmedAbsent int64  `json:"logical_bytes_confirmed_absent"`
	AbsenceVerified             bool   `json:"absence_verified"`
	CompletedAt                 string `json:"completed_at"`
}

type roleSourceArtifactPurgeReceiptCommitmentV2 struct {
	ContractVersion             string `json:"contract_version"`
	IntentID                    string `json:"intent_id"`
	WorkspaceID                 string `json:"workspace_id"`
	StorageKeyDigest            string `json:"storage_key_digest"`
	ArtifactDigest              string `json:"artifact_digest"`
	SizeBytes                   int64  `json:"size_bytes"`
	Reason                      string `json:"reason"`
	StorageBackend              string `json:"storage_backend"`
	PurgeMode                   string `json:"purge_mode"`
	SuccessfulPasses            int32  `json:"successful_passes"`
	DeletedVersions             int64  `json:"deleted_versions"`
	DeletedDeleteMarkers        int64  `json:"deleted_delete_markers"`
	ObservedDeletedBytes        int64  `json:"observed_deleted_bytes"`
	LogicalBytesConfirmedAbsent int64  `json:"logical_bytes_confirmed_absent"`
	AbsenceVerified             bool   `json:"absence_verified"`
	AmbiguousAttempts           int32  `json:"ambiguous_attempts"`
	ProviderEvidenceComplete    bool   `json:"provider_evidence_complete"`
	CompletedAt                 string `json:"completed_at"`
}

func buildRoleSourceArtifactPurgeReceiptParams(
	intent db.RoleSourceArtifactDeleteIntent,
	leaseToken pgtype.UUID,
	result storage.PermanentPurgeResult,
	completedAt time.Time,
) (db.CompleteRoleSourceArtifactDeleteIntentParams, error) {
	if !intent.ID.Valid || !intent.WorkspaceID.Valid || !leaseToken.Valid {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge receipt identity is invalid")
	}
	if err := validatePermanentPurgeResult(result); err != nil {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, err
	}
	backend := result.Backend
	if intent.PurgeBackend.Valid && intent.PurgeBackend.String != backend {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge backend changed during tombstone tail")
	}
	mode := result.Mode
	if intent.PurgeMode.Valid && intent.PurgeMode.String != mode {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge mode changed during tombstone tail")
	}
	passes := int64(intent.PurgePasses) + 1
	if passes < 1 || passes > 100 {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge pass count is outside the receipt contract")
	}
	versions, ok := safePurgeEvidenceAdd(intent.DeletedVersions, result.VersionsDeleted)
	if !ok {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge version count overflow")
	}
	markers, ok := safePurgeEvidenceAdd(intent.DeletedDeleteMarkers, result.DeleteMarkersDeleted)
	if !ok {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge delete-marker count overflow")
	}
	observedBytes, ok := safePurgeEvidenceAdd(intent.ObservedDeletedBytes, result.ObservedBytesDeleted)
	if !ok {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge observed-byte count overflow")
	}
	// PostgreSQL timestamptz persists microsecond precision. Commit to the exact
	// value that the database will return; otherwise a nanosecond-bearing Go
	// timestamp produces a receipt digest that cannot be verified after the
	// INSERT round trip.
	completedAt = completedAt.UTC().Truncate(time.Microsecond)
	storageKeyDigest := digestPurgeReceiptValue(intent.StorageKey)
	ambiguousAttempts := intent.PurgeAmbiguousAttempts
	if ambiguousAttempts < 0 || ambiguousAttempts > 1_000_000 {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, errors.New("artifact purge ambiguity count is outside the receipt contract")
	}
	providerEvidenceComplete := ambiguousAttempts == 0
	commitment := roleSourceArtifactPurgeReceiptCommitmentV2{
		ContractVersion: roleSourceArtifactPurgeReceiptContractV2,
		IntentID:        util.UUIDToString(intent.ID), WorkspaceID: util.UUIDToString(intent.WorkspaceID),
		StorageKeyDigest: storageKeyDigest, ArtifactDigest: intent.ArtifactDigest,
		SizeBytes: intent.SizeBytes, Reason: intent.Reason, StorageBackend: backend, PurgeMode: mode,
		SuccessfulPasses: int32(passes), DeletedVersions: versions, DeletedDeleteMarkers: markers,
		ObservedDeletedBytes: observedBytes, LogicalBytesConfirmedAbsent: intent.SizeBytes,
		AbsenceVerified: true, AmbiguousAttempts: ambiguousAttempts,
		ProviderEvidenceComplete: providerEvidenceComplete,
		CompletedAt:              completedAt.Format(time.RFC3339Nano),
	}
	body, err := json.Marshal(commitment)
	if err != nil {
		return db.CompleteRoleSourceArtifactDeleteIntentParams{}, err
	}
	return db.CompleteRoleSourceArtifactDeleteIntentParams{
		StorageKey: intent.StorageKey, LeaseToken: leaseToken, AbsenceVerified: true,
		PurgeBackend: pgtype.Text{String: backend, Valid: true}, PurgeMode: pgtype.Text{String: mode, Valid: true},
		StorageKeyDigest: storageKeyDigest, SuccessfulPasses: int32(passes),
		PurgedVersionCount: result.VersionsDeleted, PurgedDeleteMarkerCount: result.DeleteMarkersDeleted,
		PurgedObservedBytes:  result.ObservedBytesDeleted,
		TotalDeletedVersions: versions, TotalDeletedDeleteMarkers: markers,
		TotalObservedDeletedBytes: observedBytes,
		ContractVersion:           roleSourceArtifactPurgeReceiptContractV2,
		AmbiguousAttempts:         ambiguousAttempts,
		ProviderEvidenceComplete:  providerEvidenceComplete,
		CompletedAt:               pgtype.Timestamptz{Time: completedAt, Valid: true},
		ReceiptDigest:             digestPurgeReceiptBytes(body),
	}, nil
}

// VerifyRoleSourceArtifactPurgeReceipt recomputes the content-free commitment
// before an API, DR verifier or operator report trusts persisted evidence.
func VerifyRoleSourceArtifactPurgeReceipt(receipt db.RoleSourceArtifactPurgeReceipt) error {
	if !receipt.ID.Valid || !receipt.IntentID.Valid || !receipt.WorkspaceID.Valid ||
		!receipt.CompletedAt.Valid || !receipt.AbsenceVerified ||
		receipt.LogicalBytesConfirmedAbsent != receipt.SizeBytes {
		return errors.New("artifact purge receipt shape is invalid")
	}
	result := storage.PermanentPurgeResult{
		Backend: receipt.StorageBackend, Mode: receipt.PurgeMode,
		VersionsDeleted: receipt.DeletedVersions, DeleteMarkersDeleted: receipt.DeletedDeleteMarkers,
		ObservedBytesDeleted: receipt.ObservedDeletedBytes, VerifiedAbsent: receipt.AbsenceVerified,
	}
	if err := validatePermanentPurgeResult(result); err != nil {
		return err
	}
	if receipt.AmbiguousAttempts < 0 || receipt.AmbiguousAttempts > 1_000_000 {
		return errors.New("artifact purge receipt ambiguity count is invalid")
	}
	if receipt.ProviderEvidenceComplete != (receipt.AmbiguousAttempts == 0) {
		return errors.New("artifact purge receipt evidence-completeness flag is invalid")
	}
	var commitment any
	switch receipt.ContractVersion {
	case roleSourceArtifactPurgeReceiptContractV1:
		if receipt.AmbiguousAttempts != 0 || !receipt.ProviderEvidenceComplete {
			return errors.New("artifact purge v1 receipt contains ambiguity evidence")
		}
		commitment = roleSourceArtifactPurgeReceiptCommitmentV1{
			ContractVersion: roleSourceArtifactPurgeReceiptContractV1,
			IntentID:        util.UUIDToString(receipt.IntentID), WorkspaceID: util.UUIDToString(receipt.WorkspaceID),
			StorageKeyDigest: receipt.StorageKeyDigest, ArtifactDigest: receipt.ArtifactDigest,
			SizeBytes: receipt.SizeBytes, Reason: receipt.Reason, StorageBackend: receipt.StorageBackend,
			PurgeMode: receipt.PurgeMode, SuccessfulPasses: receipt.SuccessfulPasses,
			DeletedVersions: receipt.DeletedVersions, DeletedDeleteMarkers: receipt.DeletedDeleteMarkers,
			ObservedDeletedBytes:        receipt.ObservedDeletedBytes,
			LogicalBytesConfirmedAbsent: receipt.LogicalBytesConfirmedAbsent,
			AbsenceVerified:             receipt.AbsenceVerified,
			CompletedAt:                 receipt.CompletedAt.Time.UTC().Format(time.RFC3339Nano),
		}
	case roleSourceArtifactPurgeReceiptContractV2:
		commitment = roleSourceArtifactPurgeReceiptCommitmentV2{
			ContractVersion: roleSourceArtifactPurgeReceiptContractV2,
			IntentID:        util.UUIDToString(receipt.IntentID), WorkspaceID: util.UUIDToString(receipt.WorkspaceID),
			StorageKeyDigest: receipt.StorageKeyDigest, ArtifactDigest: receipt.ArtifactDigest,
			SizeBytes: receipt.SizeBytes, Reason: receipt.Reason, StorageBackend: receipt.StorageBackend,
			PurgeMode: receipt.PurgeMode, SuccessfulPasses: receipt.SuccessfulPasses,
			DeletedVersions: receipt.DeletedVersions, DeletedDeleteMarkers: receipt.DeletedDeleteMarkers,
			ObservedDeletedBytes:        receipt.ObservedDeletedBytes,
			LogicalBytesConfirmedAbsent: receipt.LogicalBytesConfirmedAbsent,
			AbsenceVerified:             receipt.AbsenceVerified,
			AmbiguousAttempts:           receipt.AmbiguousAttempts,
			ProviderEvidenceComplete:    receipt.ProviderEvidenceComplete,
			CompletedAt:                 receipt.CompletedAt.Time.UTC().Format(time.RFC3339Nano),
		}
	default:
		return errors.New("artifact purge receipt contract version is unsupported")
	}
	body, err := json.Marshal(commitment)
	if err != nil {
		return err
	}
	if digestPurgeReceiptBytes(body) != receipt.ReceiptDigest {
		return errors.New("artifact purge receipt commitment mismatch")
	}
	return nil
}

func validatePermanentPurgeResult(result storage.PermanentPurgeResult) error {
	if !result.VerifiedAbsent {
		return errors.New("artifact permanent purge did not verify exact-key absence")
	}
	if result.VersionsDeleted < 0 || result.DeleteMarkersDeleted < 0 || result.ObservedBytesDeleted < 0 {
		return errors.New("artifact permanent purge returned negative evidence")
	}
	switch result.Backend {
	case storage.PermanentPurgeBackendLocal:
		if result.Mode != storage.PermanentPurgeModeCurrent || result.DeleteMarkersDeleted != 0 {
			return errors.New("local permanent purge returned an invalid mode or delete-marker count")
		}
	case storage.PermanentPurgeBackendS3:
		if result.Mode != storage.PermanentPurgeModeVersions {
			return errors.New("S3 permanent purge did not use all-version mode")
		}
	default:
		return errors.New("artifact permanent purge returned an unsupported backend")
	}
	return nil
}

func safePurgeEvidenceAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func digestPurgeReceiptValue(value string) string {
	return digestPurgeReceiptBytes([]byte(value))
}

func digestPurgeReceiptBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
