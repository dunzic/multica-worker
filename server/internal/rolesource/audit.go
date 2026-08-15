package rolesource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const AuditContractVersion = "1.0"

type AuditActor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// AuditPayload is deliberately closed and secret-free. New lifecycle events
// extend this typed digest/count vocabulary instead of accepting arbitrary
// adapter JSON that could leak configuration, paths or credentials.
type AuditPayload struct {
	OperationID              string `json:"operation_id,omitempty"`
	SnapshotDigest           string `json:"snapshot_digest,omitempty"`
	ManifestDigest           string `json:"manifest_digest,omitempty"`
	PlanDigest               string `json:"plan_digest,omitempty"`
	ReceiptDigest            string `json:"receipt_digest,omitempty"`
	AdapterKind              Kind   `json:"adapter_kind,omitempty"`
	AdapterVersion           string `json:"adapter_version,omitempty"`
	PreviousRuntimeID        string `json:"previous_runtime_id,omitempty"`
	RuntimeID                string `json:"runtime_id,omitempty"`
	PreviousState            string `json:"previous_state,omitempty"`
	State                    string `json:"state,omitempty"`
	Result                   string `json:"result,omitempty"`
	ErrorCode                string `json:"error_code,omitempty"`
	CreateCount              int    `json:"create_count,omitempty"`
	UpdateCount              int    `json:"update_count,omitempty"`
	AdoptCount               int    `json:"adopt_count,omitempty"`
	UnchangedCount           int    `json:"unchanged_count,omitempty"`
	ArchiveCount             int    `json:"archive_count,omitempty"`
	BlockedCount             int    `json:"blocked_count,omitempty"`
	DiagnosticCount          int    `json:"diagnostic_count,omitempty"`
	CancelledScanCount       int    `json:"cancelled_scan_count,omitempty"`
	CancelledTransferCount   int    `json:"cancelled_transfer_count,omitempty"`
	RetentionPolicyVersion   int64  `json:"retention_policy_version,omitempty"`
	RetentionMinimumDays     int    `json:"retention_minimum_days,omitempty"`
	RetentionKeepSucceeded   int    `json:"retention_keep_succeeded,omitempty"`
	EstimatedBytes           int64  `json:"estimated_bytes,omitempty"`
	UniquelyReclaimableBytes int64  `json:"uniquely_reclaimable_bytes,omitempty"`
}

// AuditEvent is the application-generated, hash-chained representation stored
// atomically beside the lifecycle mutation it describes.
type AuditEvent struct {
	ContractVersion     string       `json:"contract_version"`
	SourceID            string       `json:"source_id"`
	WorkspaceID         string       `json:"workspace_id"`
	Sequence            int64        `json:"sequence"`
	EventType           string       `json:"event_type"`
	Actor               AuditActor   `json:"actor"`
	PreviousEventDigest string       `json:"previous_event_digest,omitempty"`
	Payload             AuditPayload `json:"payload"`
	OccurredAt          time.Time    `json:"occurred_at"`
	EventDigest         string       `json:"event_digest"`
}

func BuildAuditEvent(sourceID, workspaceID string, sequence int64, eventType string, actor AuditActor, previousDigest string, payload AuditPayload, occurredAt time.Time) (AuditEvent, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(workspaceID) == "" || sequence <= 0 {
		return AuditEvent{}, errors.New("audit event requires source, workspace and positive sequence")
	}
	if !stableIDPattern.MatchString(eventType) {
		return AuditEvent{}, fmt.Errorf("invalid audit event type %q", eventType)
	}
	if err := validateAuditActor(actor); err != nil {
		return AuditEvent{}, err
	}
	if previousDigest != "" && !sha256Pattern.MatchString(previousDigest) {
		return AuditEvent{}, errors.New("invalid previous audit event digest")
	}
	if occurredAt.IsZero() {
		return AuditEvent{}, errors.New("audit event requires occurred_at")
	}
	if err := validateAuditPayload(payload); err != nil {
		return AuditEvent{}, err
	}
	event := AuditEvent{
		ContractVersion: AuditContractVersion,
		SourceID:        sourceID, WorkspaceID: workspaceID, Sequence: sequence,
		EventType: eventType, Actor: actor, PreviousEventDigest: previousDigest,
		// PostgreSQL TIMESTAMPTZ stores microseconds. Canonicalize before
		// hashing so a persisted event can always be independently rebuilt.
		Payload: payload, OccurredAt: occurredAt.UTC().Truncate(time.Microsecond),
	}
	digest, err := digestAuditEvent(event)
	if err != nil {
		return AuditEvent{}, err
	}
	event.EventDigest = digest
	return event, nil
}

func ValidateAuditEvent(event AuditEvent) error {
	if event.ContractVersion != AuditContractVersion {
		return fmt.Errorf("audit contract %q, expected %q", event.ContractVersion, AuditContractVersion)
	}
	digest, err := digestAuditEvent(event)
	if err != nil {
		return err
	}
	if !sha256Pattern.MatchString(event.EventDigest) || digest != event.EventDigest {
		return fmt.Errorf("audit event digest mismatch: got %q, computed %q", event.EventDigest, digest)
	}
	copy := event
	copy.EventDigest = ""
	_, err = BuildAuditEvent(copy.SourceID, copy.WorkspaceID, copy.Sequence, copy.EventType, copy.Actor, copy.PreviousEventDigest, copy.Payload, copy.OccurredAt)
	return err
}

// DecodePersistedAuditEvent reconstructs and verifies a stored hash-chain
// event. Operator and disaster-recovery tools use this instead of trusting the
// indexed columns or arbitrary JSON independently.
func DecodePersistedAuditEvent(row db.RoleSourceAuditEvent) (AuditEvent, error) {
	var payload AuditPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return AuditEvent{}, err
	}
	event := AuditEvent{
		ContractVersion: AuditContractVersion,
		SourceID:        util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Sequence: row.Sequence, EventType: row.EventType,
		Actor:               AuditActor{Type: row.ActorType, ID: auditUUIDText(row.ActorID)},
		PreviousEventDigest: auditTextValue(row.PreviousEventDigest), Payload: payload,
		OccurredAt: row.CreatedAt.Time, EventDigest: row.EventDigest,
	}
	if err := ValidateAuditEvent(event); err != nil {
		return AuditEvent{}, err
	}
	return event, nil
}

func auditUUIDText(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return util.UUIDToString(value)
}

func auditTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func validateAuditActor(actor AuditActor) error {
	switch actor.Type {
	case "user", "runtime":
		if strings.TrimSpace(actor.ID) == "" {
			return fmt.Errorf("audit actor %q requires id", actor.Type)
		}
	case "system":
		if actor.ID != "" {
			return errors.New("system audit actor must not carry id")
		}
	default:
		return fmt.Errorf("invalid audit actor type %q", actor.Type)
	}
	return nil
}

func validateAuditPayload(payload AuditPayload) error {
	if payload.AdapterKind != "" && !kindPattern.MatchString(string(payload.AdapterKind)) {
		return fmt.Errorf("invalid adapter kind %q in audit payload", payload.AdapterKind)
	}
	for name, digest := range map[string]string{
		"snapshot": payload.SnapshotDigest,
		"manifest": payload.ManifestDigest,
		"plan":     payload.PlanDigest,
		"receipt":  payload.ReceiptDigest,
	} {
		if digest != "" && !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("invalid %s digest in audit payload", name)
		}
	}
	for name, value := range map[string]string{
		"operation_id":        payload.OperationID,
		"adapter_version":     payload.AdapterVersion,
		"previous_runtime_id": payload.PreviousRuntimeID,
		"runtime_id":          payload.RuntimeID,
		"previous_state":      payload.PreviousState,
		"state":               payload.State,
		"result":              payload.Result,
		"error_code":          payload.ErrorCode,
	} {
		if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("unsafe or oversized audit payload %s", name)
		}
	}
	for name, runtimeID := range map[string]string{"previous_runtime_id": payload.PreviousRuntimeID, "runtime_id": payload.RuntimeID} {
		if runtimeID != "" {
			if _, err := uuid.Parse(runtimeID); err != nil {
				return fmt.Errorf("invalid audit payload %s", name)
			}
		}
	}
	for name, state := range map[string]string{"previous_state": payload.PreviousState, "state": payload.State} {
		if state != "" && !validRoleSourceState(state) {
			return fmt.Errorf("invalid audit payload %s", name)
		}
	}
	counts := []int{
		payload.CreateCount, payload.UpdateCount, payload.AdoptCount, payload.UnchangedCount,
		payload.ArchiveCount, payload.BlockedCount, payload.DiagnosticCount,
		payload.CancelledScanCount, payload.CancelledTransferCount,
	}
	for _, count := range counts {
		if count < 0 || count > maxNormalizedObjects {
			return errors.New("audit payload count is outside the allowed range")
		}
	}
	if payload.RetentionPolicyVersion < 0 || payload.RetentionPolicyVersion > 1_000_000_000 ||
		payload.RetentionMinimumDays < 0 || payload.RetentionMinimumDays > 3650 ||
		payload.RetentionKeepSucceeded < 0 || payload.RetentionKeepSucceeded > 100 ||
		payload.EstimatedBytes < 0 || payload.UniquelyReclaimableBytes < 0 {
		return errors.New("audit retention value is outside the allowed range")
	}
	return nil
}

func digestAuditEvent(event AuditEvent) (string, error) {
	event.EventDigest = ""
	// PostgreSQL TIMESTAMPTZ preserves an instant at microsecond precision, not
	// its original zone or extra nanoseconds. Canonicalize both properties so a
	// driver/server ScanLocation cannot change persisted verification.
	event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Microsecond)
	body, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
