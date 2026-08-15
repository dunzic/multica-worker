package rolesourcedr

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/rolesourcereplay"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type SecretOpener interface {
	OpenWithAAD([]byte, []byte) ([]byte, error)
}

type VerificationOptions struct {
	Expected            Manifest
	TrustedManifestKeys map[string]ed25519.PublicKey
	Storage             storage.Storage
	Keys                map[string]SecretOpener
	Now                 time.Time
}

func Verify(ctx context.Context, tx pgx.Tx, options VerificationOptions) (Report, error) {
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	report := Report{
		ContractVersion: ContractVersion,
		CheckedAt:       now,
		Status:          "failed",
		Findings:        []Finding{},
		Keys:            KeyVerification{RequiredKeyIDs: []string{}, AvailableKeyIDs: []string{}},
	}
	if err := VerifyManifestSignature(options.Expected, options.TrustedManifestKeys, false); err != nil {
		return report, fmt.Errorf("validate expected DR manifest: %w", err)
	}
	actual, artifacts, err := BuildManifest(ctx, tx)
	if err != nil {
		return report, err
	}
	report.Database.TablesChecked = len(actual.Tables)
	report.ManifestMatched = SameDatabaseState(options.Expected, actual)
	if !report.ManifestMatched {
		report.Findings = append(report.Findings, Finding{Code: "backup_manifest_mismatch", Count: 1})
	}
	q := db.New(tx)
	if err := verifySnapshots(ctx, tx, q, &report); err != nil {
		return report, err
	}
	if err := verifyPlans(ctx, tx, q, &report); err != nil {
		return report, err
	}
	if err := verifySourceConfigSummaries(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyRuntimeAttestations(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyReceipts(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyArtifactPurgeReceipts(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyAuditChains(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyOutboxReplayReceipts(ctx, tx, &report); err != nil {
		return report, err
	}
	if err := verifyRelationalInvariants(ctx, tx, now, &report); err != nil {
		return report, err
	}
	if err := verifyKeys(ctx, tx, options.Keys, now, &report); err != nil {
		return report, err
	}
	if err := verifyArtifacts(ctx, artifacts, options.Storage, &report); err != nil {
		return report, err
	}
	if len(report.Findings) == 0 {
		report.Status = "passed"
	}
	return report, nil
}

func verifyOutboxReplayReceipts(ctx context.Context, tx pgx.Tx, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT id,outbox_id,workspace_id,source_id,apply_id,authorization_id,generation,
       reason_code,incident_reference_digest,requester_key_id,approver_key_id,
       authorization_digest,requester_signature_digest,approver_signature_digest,
       expected_receipt_digest,previous_replay_digest,replay_digest,created_at
FROM role_source_outbox_replay ORDER BY outbox_id,generation`)
	if err != nil {
		return fmt.Errorf("read outbox replay receipts for DR verification: %w", err)
	}
	defer rows.Close()
	type chainState struct {
		generation int16
		digest     string
	}
	chains := map[string]chainState{}
	for rows.Next() {
		var row db.RoleSourceOutboxReplay
		if err := rows.Scan(&row.ID, &row.OutboxID, &row.WorkspaceID, &row.SourceID, &row.ApplyID, &row.AuthorizationID, &row.Generation,
			&row.ReasonCode, &row.IncidentReferenceDigest, &row.RequesterKeyID, &row.ApproverKeyID,
			&row.AuthorizationDigest, &row.RequesterSignatureDigest, &row.ApproverSignatureDigest,
			&row.ExpectedReceiptDigest, &row.PreviousReplayDigest, &row.ReplayDigest, &row.CreatedAt); err != nil {
			return err
		}
		outboxID := util.UUIDToString(row.OutboxID)
		state := chains[outboxID]
		receipt, decodeErr := rolesourcereplay.DecodePersistedReplayReceipt(row)
		if decodeErr != nil || receipt.Generation != state.generation+1 || receipt.PreviousReplayDigest != state.digest {
			addFinding(report, "outbox_replay_chain_invalid", 1)
		} else {
			report.Database.ReplayReceiptsValidated++
		}
		chains[outboxID] = chainState{generation: row.Generation, digest: row.ReplayDigest}
	}
	return rows.Err()
}

func verifyRuntimeAttestations(ctx context.Context, tx pgx.Tx, report *Report) error {
	for _, table := range []string{"role_source_runtime_attestation", "role_source_runtime_attestation_observation"} {
		rows, err := tx.Query(ctx, "SELECT contract_version, loaded, attestation_id, config_revision, sources FROM "+pgx.Identifier{table}.Sanitize()+" ORDER BY runtime_id, attestation_id")
		if err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		for rows.Next() {
			var contract, attestationID string
			var loaded bool
			var revision pgtype.Text
			var sourceBody []byte
			if err := rows.Scan(&contract, &loaded, &attestationID, &revision, &sourceBody); err != nil {
				rows.Close()
				return err
			}
			var sources []protocol.RoleSourceLoadedConfig
			if err := json.Unmarshal(sourceBody, &sources); err != nil || protocol.ValidateRoleSourceConfigAttestation(protocol.RoleSourceConfigAttestation{
				ContractVersion: contract, Loaded: loaded, AttestationID: attestationID, Revision: revision.String, Sources: sources,
			}) != nil {
				addFinding(report, "runtime_attestation_invalid", 1)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

func verifySourceConfigSummaries(ctx context.Context, tx pgx.Tx, report *Report) error {
	rows, err := tx.Query(ctx, `SELECT config_redacted FROM role_source ORDER BY workspace_id, id`)
	if err != nil {
		return fmt.Errorf("read source config summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return err
		}
		var summary rolesource.ConfigSummary
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&summary); err != nil || !safeConfigSummary(summary) {
			addFinding(report, "source_config_summary_invalid", 1)
		}
	}
	return rows.Err()
}

func safeConfigSummary(summary rolesource.ConfigSummary) bool {
	if len(summary.Attributes) > 64 {
		return false
	}
	for _, attribute := range summary.Attributes {
		if attribute.Name == "" || len(attribute.Name) > 100 || len(attribute.Value) > 512 || strings.ContainsAny(attribute.Name+attribute.Value, "\r\n\x00") {
			return false
		}
	}
	return true
}

func verifySnapshots(ctx context.Context, tx pgx.Tx, q *db.Queries, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT source_id, workspace_id, snapshot_digest, manifest_digest, kind,
       adapter_version, contract_version, manifest, diagnostics,
       source_evidence, reported_by_runtime_id, created_at
FROM role_source_snapshot ORDER BY workspace_id, source_id, snapshot_digest`)
	if err != nil {
		return fmt.Errorf("read snapshots for DR verification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row db.RoleSourceSnapshot
		if err := rows.Scan(&row.SourceID, &row.WorkspaceID, &row.SnapshotDigest, &row.ManifestDigest, &row.Kind,
			&row.AdapterVersion, &row.ContractVersion, &row.Manifest, &row.Diagnostics, &row.SourceEvidence,
			&row.ReportedByRuntimeID, &row.CreatedAt); err != nil {
			return err
		}
		snapshot, decodeErr := rolesource.DecodePersistedSnapshot(row)
		if decodeErr != nil {
			addFinding(report, "snapshot_digest_invalid", 1)
			continue
		}
		report.Database.SnapshotsValidated++
		refs, err := rolesource.CollectArtifactRefs(snapshot)
		if err != nil {
			addFinding(report, "snapshot_artifact_contract_invalid", 1)
			continue
		}
		edges, err := q.ListRoleSourceSnapshotArtifacts(ctx, db.ListRoleSourceSnapshotArtifactsParams{
			WorkspaceID: row.WorkspaceID, SourceID: row.SourceID, SnapshotDigest: row.SnapshotDigest,
		})
		if err != nil {
			return err
		}
		if len(edges) != len(refs) {
			addFinding(report, "snapshot_artifact_edges_incomplete", 1)
			continue
		}
		for index := range refs {
			if edges[index].ArtifactDigest != refs[index].Digest || edges[index].SizeBytes != refs[index].SizeBytes {
				addFinding(report, "snapshot_artifact_edges_conflict", 1)
				break
			}
		}
	}
	return rows.Err()
}

func verifyPlans(ctx context.Context, tx pgx.Tx, q *db.Queries, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT source_id, workspace_id, plan_digest, from_snapshot_digest,
       to_snapshot_digest, plan, created_by, created_at
FROM role_source_plan ORDER BY workspace_id, source_id, plan_digest`)
	if err != nil {
		return fmt.Errorf("read plans for DR verification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row db.RoleSourcePlan
		if err := rows.Scan(&row.SourceID, &row.WorkspaceID, &row.PlanDigest, &row.FromSnapshotDigest,
			&row.ToSnapshotDigest, &row.Plan, &row.CreatedBy, &row.CreatedAt); err != nil {
			return err
		}
		if _, err := rolesource.DecodePersistedPlan(row); err != nil {
			addFinding(report, "plan_digest_invalid", 1)
			continue
		}
		report.Database.PlansValidated++
	}
	return rows.Err()
}

func verifyReceipts(ctx context.Context, tx pgx.Tx, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT id, source_id, workspace_id, request_key, mode, snapshot_digest,
       plan_digest, status, actor_user_id, receipt_digest, receipt, error_code,
       created_at, completed_at
FROM role_source_apply WHERE status = 'succeeded'
ORDER BY workspace_id, source_id, id`)
	if err != nil {
		return fmt.Errorf("read receipts for DR verification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row db.RoleSourceApply
		if err := rows.Scan(&row.ID, &row.SourceID, &row.WorkspaceID, &row.RequestKey, &row.Mode,
			&row.SnapshotDigest, &row.PlanDigest, &row.Status, &row.ActorUserID, &row.ReceiptDigest,
			&row.Receipt, &row.ErrorCode, &row.CreatedAt, &row.CompletedAt); err != nil {
			return err
		}
		if _, err := rolesource.DecodePersistedApplyReceipt(row); err != nil {
			addFinding(report, "apply_receipt_invalid", 1)
			continue
		}
		report.Database.ReceiptsValidated++
	}
	return rows.Err()
}

func verifyArtifactPurgeReceipts(ctx context.Context, tx pgx.Tx, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT id,intent_id,workspace_id,storage_key_digest,artifact_digest,size_bytes,
       reason,storage_backend,purge_mode,successful_passes,deleted_versions,
       deleted_delete_markers,observed_deleted_bytes,logical_bytes_confirmed_absent,
       absence_verified,completed_at,receipt_digest,created_at
FROM role_source_artifact_purge_receipt
ORDER BY workspace_id,completed_at,intent_id`)
	if err != nil {
		return fmt.Errorf("read artifact purge receipts for DR verification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row db.RoleSourceArtifactPurgeReceipt
		if err := rows.Scan(&row.ID, &row.IntentID, &row.WorkspaceID, &row.StorageKeyDigest,
			&row.ArtifactDigest, &row.SizeBytes, &row.Reason, &row.StorageBackend, &row.PurgeMode,
			&row.SuccessfulPasses, &row.DeletedVersions, &row.DeletedDeleteMarkers,
			&row.ObservedDeletedBytes, &row.LogicalBytesConfirmedAbsent, &row.AbsenceVerified,
			&row.CompletedAt, &row.ReceiptDigest, &row.CreatedAt); err != nil {
			return err
		}
		if err := service.VerifyRoleSourceArtifactPurgeReceipt(row); err != nil {
			addFinding(report, "artifact_purge_receipt_invalid", 1)
			continue
		}
		report.Database.ArtifactPurgeReceiptsValidated++
	}
	return rows.Err()
}

func verifyAuditChains(ctx context.Context, tx pgx.Tx, report *Report) error {
	rows, err := tx.Query(ctx, `
SELECT source_id, workspace_id, sequence, event_type, actor_type, actor_id,
       previous_event_digest, event_digest, payload, created_at
FROM role_source_audit_event ORDER BY source_id, sequence`)
	if err != nil {
		return fmt.Errorf("read audit events for DR verification: %w", err)
	}
	defer rows.Close()
	type chainState struct {
		sequence  int64
		digest    string
		workspace string
	}
	chains := map[string]chainState{}
	for rows.Next() {
		var sourceID, workspaceID pgtype.UUID
		var sequence int64
		var eventType, actorType, eventDigest string
		var actorID pgtype.UUID
		var previous pgtype.Text
		var payloadBody []byte
		var createdAt pgtype.Timestamptz
		if err := rows.Scan(&sourceID, &workspaceID, &sequence, &eventType, &actorType, &actorID,
			&previous, &eventDigest, &payloadBody, &createdAt); err != nil {
			return err
		}
		var payload rolesource.AuditPayload
		if err := json.Unmarshal(payloadBody, &payload); err != nil {
			addFinding(report, "audit_payload_invalid", 1)
			continue
		}
		event := rolesource.AuditEvent{
			ContractVersion: rolesource.AuditContractVersion,
			SourceID:        util.UUIDToString(sourceID), WorkspaceID: util.UUIDToString(workspaceID), Sequence: sequence,
			EventType: eventType, Actor: rolesource.AuditActor{Type: actorType},
			PreviousEventDigest: previous.String, Payload: payload, OccurredAt: createdAt.Time.UTC(), EventDigest: eventDigest,
		}
		if actorID.Valid {
			event.Actor.ID = util.UUIDToString(actorID)
		}
		state := chains[event.SourceID]
		if sequence != state.sequence+1 || event.PreviousEventDigest != state.digest ||
			(state.workspace != "" && state.workspace != event.WorkspaceID) || rolesource.ValidateAuditEvent(event) != nil {
			addFinding(report, "audit_chain_invalid", 1)
		} else {
			report.Database.AuditEventsValidated++
		}
		chains[event.SourceID] = chainState{sequence: sequence, digest: eventDigest, workspace: event.WorkspaceID}
	}
	return rows.Err()
}

func verifyRelationalInvariants(ctx context.Context, tx pgx.Tx, now time.Time, report *Report) error {
	rows, err := tx.Query(ctx, relationalInvariantQuery, now)
	if err != nil {
		return fmt.Errorf("verify role-source relational invariants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var count int64
		if err := rows.Scan(&code, &count); err != nil {
			return err
		}
		if count > 0 {
			addFinding(report, code, count)
		}
	}
	return rows.Err()
}

func verifyKeys(ctx context.Context, tx pgx.Tx, keys map[string]SecretOpener, now time.Time, report *Report) error {
	required := map[string]bool{}
	for keyID := range keys {
		report.Keys.AvailableKeyIDs = append(report.Keys.AvailableKeyIDs, keyID)
	}
	sort.Strings(report.Keys.AvailableKeyIDs)
	rows, err := tx.Query(ctx, `
SELECT id, workspace_id, source_id, runtime_id, plan_digest, approval_id,
       snapshot_digest, role_id, request_key, status, public_key,
       private_key_ciphertext, key_id, claims, envelope, envelope_digest,
       claimed_by_runtime_id, lease_token, lease_expires_at, expires_at,
       created_by, created_at, claimed_at, submitted_at, consumed_at, error_code
FROM role_source_secret_transfer
WHERE status IN ('pending','claimed','submitted') AND expires_at > $1
ORDER BY key_id, id`, now)
	if err != nil {
		return fmt.Errorf("read active secret transfers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row db.RoleSourceSecretTransfer
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.SourceID, &row.RuntimeID, &row.PlanDigest, &row.ApprovalID,
			&row.SnapshotDigest, &row.RoleID, &row.RequestKey, &row.Status, &row.PublicKey,
			&row.PrivateKeyCiphertext, &row.KeyID, &row.Claims, &row.Envelope, &row.EnvelopeDigest,
			&row.ClaimedByRuntimeID, &row.LeaseToken, &row.LeaseExpiresAt, &row.ExpiresAt,
			&row.CreatedBy, &row.CreatedAt, &row.ClaimedAt, &row.SubmittedAt, &row.ConsumedAt, &row.ErrorCode); err != nil {
			return err
		}
		if rolesource.ValidatePersistedSecretTransferIdentity(row) != nil {
			addFinding(report, "secret_transfer_claims_invalid", 1)
			continue
		}
		if row.Status == "submitted" {
			sum := sha256.Sum256(row.Envelope)
			if !row.EnvelopeDigest.Valid || row.EnvelopeDigest.String != "sha256:"+hex.EncodeToString(sum[:]) {
				addFinding(report, "secret_transfer_envelope_invalid", 1)
				continue
			}
		}
		required[row.KeyID] = true
		box, ok := keys[row.KeyID]
		if !ok {
			addFinding(report, "secret_transfer_key_missing", 1)
			continue
		}
		plaintext, err := box.OpenWithAAD(row.PrivateKeyCiphertext, row.Claims)
		if err != nil || len(plaintext) != 32 {
			clear(plaintext)
			addFinding(report, "secret_transfer_ciphertext_invalid", 1)
			continue
		}
		clear(plaintext)
		report.Keys.DecryptableTransfers++
	}
	for keyID := range required {
		report.Keys.RequiredKeyIDs = append(report.Keys.RequiredKeyIDs, keyID)
	}
	sort.Strings(report.Keys.RequiredKeyIDs)
	return rows.Err()
}

func verifyArtifacts(ctx context.Context, artifacts []ArtifactRecord, store storage.Storage, report *Report) error {
	report.Artifacts.Expected = int64(len(artifacts))
	if len(artifacts) > 0 && store == nil {
		addFinding(report, "artifact_storage_unavailable", int64(len(artifacts)))
		return nil
	}
	for _, artifact := range artifacts {
		expectedKey := "role-source-artifacts/" + artifact.WorkspaceID + "/" + strings.TrimPrefix(artifact.Digest, "sha256:")
		if artifact.StorageKey != expectedKey {
			addFinding(report, "artifact_storage_key_invalid", 1)
			continue
		}
		reader, err := store.GetReader(ctx, artifact.StorageKey)
		if err != nil {
			addFinding(report, "artifact_object_missing", 1)
			continue
		}
		digest := sha256.New()
		read, copyErr := io.Copy(digest, io.LimitReader(reader, artifact.SizeBytes+1))
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil || read != artifact.SizeBytes || "sha256:"+hex.EncodeToString(digest.Sum(nil)) != artifact.Digest {
			addFinding(report, "artifact_object_invalid", 1)
			continue
		}
		report.Artifacts.Verified++
		report.Artifacts.SizeBytes += read
	}
	return nil
}

func addFinding(report *Report, code string, count int64) {
	for index := range report.Findings {
		if report.Findings[index].Code == code {
			report.Findings[index].Count += count
			return
		}
	}
	report.Findings = append(report.Findings, Finding{Code: code, Count: count})
}

const relationalInvariantQuery = `
SELECT code, count(*)::bigint FROM (
  SELECT 'source_workspace_missing' code FROM role_source s LEFT JOIN workspace w ON w.id=s.workspace_id WHERE w.id IS NULL
  UNION ALL SELECT 'source_runtime_missing' FROM role_source s LEFT JOIN agent_runtime r ON r.id=s.runtime_id AND r.workspace_id=s.workspace_id WHERE s.state <> 'detached' AND r.id IS NULL
  UNION ALL SELECT 'current_snapshot_missing' FROM role_source s LEFT JOIN role_source_snapshot x ON x.source_id=s.id AND x.workspace_id=s.workspace_id AND x.snapshot_digest=s.current_snapshot_digest WHERE s.current_snapshot_digest IS NOT NULL AND x.source_id IS NULL
  UNION ALL SELECT 'snapshot_source_missing' FROM role_source_snapshot x LEFT JOIN role_source s ON s.id=x.source_id AND s.workspace_id=x.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'scan_source_missing' FROM role_source_scan_request r LEFT JOIN role_source s ON s.id=r.source_id AND s.workspace_id=r.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'scan_snapshot_missing' FROM role_source_scan_request r LEFT JOIN role_source_snapshot x ON x.source_id=r.source_id AND x.workspace_id=r.workspace_id AND x.snapshot_digest=r.snapshot_digest WHERE r.status='succeeded' AND x.source_id IS NULL
  UNION ALL SELECT 'plan_source_missing' FROM role_source_plan p LEFT JOIN role_source s ON s.id=p.source_id AND s.workspace_id=p.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'approval_plan_missing' FROM role_source_plan_approval a LEFT JOIN role_source_plan p ON p.source_id=a.source_id AND p.workspace_id=a.workspace_id AND p.plan_digest=a.plan_digest WHERE p.source_id IS NULL
  UNION ALL SELECT 'apply_source_missing' FROM role_source_apply a LEFT JOIN role_source s ON s.id=a.source_id AND s.workspace_id=a.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'apply_failure_source_missing' FROM role_source_apply_failure f LEFT JOIN role_source s ON s.id=f.source_id AND s.workspace_id=f.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'apply_failure_plan_missing' FROM role_source_apply_failure f LEFT JOIN role_source_plan p ON p.source_id=f.source_id AND p.workspace_id=f.workspace_id AND p.plan_digest=f.plan_digest WHERE p.source_id IS NULL
  UNION ALL SELECT 'apply_failure_approval_missing' FROM role_source_apply_failure f LEFT JOIN role_source_plan_approval a ON a.id=f.approval_id AND a.source_id=f.source_id AND a.workspace_id=f.workspace_id WHERE a.id IS NULL
  UNION ALL SELECT 'audit_source_missing' FROM role_source_audit_event a LEFT JOIN role_source s ON s.id=a.source_id AND s.workspace_id=a.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'outbox_apply_missing' FROM role_source_outbox o LEFT JOIN role_source_apply a ON a.id=o.apply_id AND a.source_id=o.source_id AND a.workspace_id=o.workspace_id AND a.status='succeeded' WHERE a.id IS NULL
  UNION ALL SELECT 'outbox_apply_commitment_mismatch' FROM role_source_outbox o JOIN role_source_apply a ON a.id=o.apply_id AND a.source_id=o.source_id AND a.workspace_id=o.workspace_id WHERE a.status<>'succeeded' OR o.mode<>a.mode OR o.snapshot_digest<>a.snapshot_digest OR o.plan_digest<>a.plan_digest OR o.receipt_digest IS DISTINCT FROM a.receipt_digest OR o.actor_type<>'user' OR o.actor_id IS DISTINCT FROM a.actor_user_id
  UNION ALL SELECT 'outbox_audit_mismatch' FROM role_source_outbox o WHERE (SELECT count(*) FROM role_source_audit_event e WHERE e.workspace_id=o.workspace_id AND e.source_id=o.source_id AND e.event_type=CASE WHEN o.mode='rollback' THEN 'rollback_succeeded' ELSE 'apply_succeeded' END AND e.actor_type='user' AND e.actor_id=o.actor_id AND e.payload->>'operation_id'=o.apply_id::text AND e.payload->>'snapshot_digest'=o.snapshot_digest AND e.payload->>'plan_digest'=o.plan_digest AND e.payload->>'receipt_digest'=o.receipt_digest AND e.payload->>'result'='succeeded')<>1
  UNION ALL SELECT 'outbox_source_missing' FROM role_source_outbox o LEFT JOIN role_source s ON s.id=o.source_id AND s.workspace_id=o.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'outbox_replay_event_missing' FROM role_source_outbox_replay r LEFT JOIN role_source_outbox o ON o.id=r.outbox_id AND o.source_id=r.source_id AND o.workspace_id=r.workspace_id AND o.apply_id=r.apply_id WHERE o.id IS NULL
  UNION ALL SELECT 'outbox_replay_receipt_mismatch' FROM role_source_outbox_replay r JOIN role_source_outbox o ON o.id=r.outbox_id WHERE r.expected_receipt_digest<>o.receipt_digest
  UNION ALL SELECT 'outbox_replay_count_mismatch' FROM role_source_outbox o LEFT JOIN (SELECT outbox_id,count(*) n,COALESCE(max(generation),0) max_generation FROM role_source_outbox_replay GROUP BY outbox_id) r ON r.outbox_id=o.id WHERE o.replay_count<>COALESCE(r.n,0) OR o.replay_count<>COALESCE(r.max_generation,0)
  UNION ALL SELECT 'artifact_edge_snapshot_missing' FROM role_source_snapshot_artifact e LEFT JOIN role_source_snapshot x ON x.source_id=e.source_id AND x.workspace_id=e.workspace_id AND x.snapshot_digest=e.snapshot_digest WHERE x.source_id IS NULL
  UNION ALL SELECT 'artifact_edge_ledger_missing' FROM role_source_snapshot_artifact e LEFT JOIN role_source_artifact a ON a.workspace_id=e.workspace_id AND a.digest=e.artifact_digest AND a.size_bytes=e.size_bytes WHERE a.digest IS NULL
  UNION ALL SELECT 'artifact_workspace_missing' FROM role_source_artifact a LEFT JOIN workspace w ON w.id=a.workspace_id WHERE w.id IS NULL
  UNION ALL SELECT 'artifact_integrity_missing' FROM role_source_artifact a LEFT JOIN role_source_artifact_integrity i ON i.workspace_id=a.workspace_id AND i.artifact_digest=a.digest AND i.storage_key=a.storage_key AND i.size_bytes=a.size_bytes WHERE i.artifact_digest IS NULL
  UNION ALL SELECT 'artifact_integrity_ledger_missing' FROM role_source_artifact_integrity i LEFT JOIN role_source_artifact a ON a.workspace_id=i.workspace_id AND a.digest=i.artifact_digest AND a.storage_key=i.storage_key AND a.size_bytes=i.size_bytes WHERE a.digest IS NULL
  UNION ALL SELECT 'task_pin_snapshot_missing' FROM role_source_task_pin p LEFT JOIN role_source_snapshot x ON x.source_id=p.source_id AND x.workspace_id=p.workspace_id AND x.snapshot_digest=p.snapshot_digest WHERE x.source_id IS NULL
  UNION ALL SELECT 'task_pin_task_missing' FROM role_source_task_pin p LEFT JOIN agent_task_queue t ON t.id=p.task_id AND t.agent_id=p.agent_id WHERE t.id IS NULL
  UNION ALL SELECT 'task_pin_agent_missing' FROM role_source_task_pin p LEFT JOIN agent a ON a.id=p.agent_id AND a.workspace_id=p.workspace_id WHERE a.id IS NULL
  UNION ALL SELECT 'mapping_snapshot_missing' FROM role_source_object_mapping m LEFT JOIN role_source_snapshot x ON x.source_id=m.source_id AND x.workspace_id=m.workspace_id AND x.snapshot_digest=m.last_snapshot_digest WHERE x.source_id IS NULL
  UNION ALL SELECT 'mapping_source_missing' FROM role_source_object_mapping m LEFT JOIN role_source s ON s.id=m.source_id AND s.workspace_id=m.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'mapping_agent_target_missing' FROM role_source_object_mapping m LEFT JOIN agent a ON a.id=m.target_id AND a.workspace_id=m.workspace_id WHERE m.target_kind='agent' AND a.id IS NULL
  UNION ALL SELECT 'mapping_skill_target_missing' FROM role_source_object_mapping m LEFT JOIN skill s ON s.id=m.target_id AND s.workspace_id=m.workspace_id WHERE m.target_kind='skill' AND s.id IS NULL
  UNION ALL SELECT 'mapping_autopilot_target_missing' FROM role_source_object_mapping m LEFT JOIN autopilot a ON a.id=m.target_id AND a.workspace_id=m.workspace_id WHERE m.target_kind='autopilot' AND a.id IS NULL
  UNION ALL SELECT 'capability_snapshot_missing' FROM role_source_capability_version c LEFT JOIN role_source_snapshot x ON x.source_id=c.source_id AND x.workspace_id=c.workspace_id AND x.snapshot_digest=c.snapshot_digest WHERE x.source_id IS NULL
  UNION ALL SELECT 'capability_source_missing' FROM role_source_capability_version c LEFT JOIN role_source s ON s.id=c.source_id AND s.workspace_id=c.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'active_secret_source_missing' FROM role_source_secret_transfer t LEFT JOIN role_source s ON s.id=t.source_id AND s.workspace_id=t.workspace_id WHERE t.status IN ('pending','claimed','submitted') AND t.expires_at > $1 AND s.id IS NULL
  UNION ALL SELECT 'active_secret_snapshot_missing' FROM role_source_secret_transfer t LEFT JOIN role_source_snapshot x ON x.source_id=t.source_id AND x.workspace_id=t.workspace_id AND x.snapshot_digest=t.snapshot_digest WHERE t.status IN ('pending','claimed','submitted') AND t.expires_at > $1 AND x.source_id IS NULL
  UNION ALL SELECT 'active_secret_plan_missing' FROM role_source_secret_transfer t LEFT JOIN role_source_plan p ON p.source_id=t.source_id AND p.workspace_id=t.workspace_id AND p.plan_digest=t.plan_digest WHERE t.status IN ('pending','claimed','submitted') AND t.expires_at > $1 AND p.source_id IS NULL
  UNION ALL SELECT 'active_secret_approval_missing' FROM role_source_secret_transfer t LEFT JOIN role_source_plan_approval a ON a.id=t.approval_id AND a.source_id=t.source_id AND a.workspace_id=t.workspace_id WHERE t.status IN ('pending','claimed','submitted') AND t.expires_at > $1 AND a.id IS NULL
  UNION ALL SELECT 'runtime_attestation_runtime_missing' FROM role_source_runtime_attestation a LEFT JOIN agent_runtime r ON r.id=a.runtime_id AND r.workspace_id=a.workspace_id WHERE r.id IS NULL
  UNION ALL SELECT 'runtime_attestation_observation_runtime_missing' FROM role_source_runtime_attestation_observation a LEFT JOIN agent_runtime r ON r.id=a.runtime_id AND r.workspace_id=a.workspace_id WHERE r.id IS NULL
  UNION ALL SELECT 'apply_plan_missing' FROM role_source_apply a LEFT JOIN role_source_plan p ON p.source_id=a.source_id AND p.workspace_id=a.workspace_id AND p.plan_digest=a.plan_digest WHERE p.source_id IS NULL
  UNION ALL SELECT 'audit_sequence_mismatch' FROM role_source s LEFT JOIN (SELECT source_id, count(*) n, COALESCE(max(sequence),0) max_sequence FROM role_source_audit_event GROUP BY source_id) a ON a.source_id=s.id WHERE s.audit_sequence <> COALESCE(a.n,0) OR s.audit_sequence <> COALESCE(a.max_sequence,0)
  UNION ALL SELECT 'legal_hold_source_missing' FROM role_source_legal_hold h LEFT JOIN role_source s ON s.id=h.source_id AND s.workspace_id=h.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'legal_hold_snapshot_missing' FROM role_source_legal_hold h LEFT JOIN role_source_snapshot x ON x.source_id=h.source_id AND x.workspace_id=h.workspace_id AND x.snapshot_digest=h.snapshot_digest WHERE h.scope='snapshot' AND x.source_id IS NULL
  UNION ALL SELECT 'legal_hold_release_orphan' FROM role_source_legal_hold_release r LEFT JOIN role_source_legal_hold h ON h.id=r.hold_id AND h.source_id=r.source_id AND h.workspace_id=r.workspace_id WHERE h.id IS NULL
  UNION ALL SELECT 'retention_policy_source_missing' FROM role_source_retention_policy p LEFT JOIN role_source s ON s.id=p.source_id AND s.workspace_id=p.workspace_id WHERE s.id IS NULL
  UNION ALL SELECT 'retention_candidate_snapshot_missing' FROM role_source_retention_candidate c LEFT JOIN role_source_snapshot x ON x.source_id=c.source_id AND x.workspace_id=c.workspace_id AND x.snapshot_digest=c.snapshot_digest WHERE c.state <> 'completed' AND x.source_id IS NULL
  UNION ALL SELECT 'artifact_delete_conflicts_with_ready_ledger' FROM role_source_artifact_delete_intent i JOIN role_source_artifact a ON a.storage_key=i.storage_key
) violations GROUP BY code ORDER BY code`
