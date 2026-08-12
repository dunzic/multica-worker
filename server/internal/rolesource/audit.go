package rolesource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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
	OperationID     string `json:"operation_id,omitempty"`
	SnapshotDigest  string `json:"snapshot_digest,omitempty"`
	ManifestDigest  string `json:"manifest_digest,omitempty"`
	PlanDigest      string `json:"plan_digest,omitempty"`
	ReceiptDigest   string `json:"receipt_digest,omitempty"`
	AdapterKind     Kind   `json:"adapter_kind,omitempty"`
	AdapterVersion  string `json:"adapter_version,omitempty"`
	Result          string `json:"result,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	CreateCount     int    `json:"create_count,omitempty"`
	UpdateCount     int    `json:"update_count,omitempty"`
	UnchangedCount  int    `json:"unchanged_count,omitempty"`
	ArchiveCount    int    `json:"archive_count,omitempty"`
	BlockedCount    int    `json:"blocked_count,omitempty"`
	DiagnosticCount int    `json:"diagnostic_count,omitempty"`
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
		Payload: payload, OccurredAt: occurredAt.UTC(),
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
		"operation_id":    payload.OperationID,
		"adapter_version": payload.AdapterVersion,
		"result":          payload.Result,
		"error_code":      payload.ErrorCode,
	} {
		if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("unsafe or oversized audit payload %s", name)
		}
	}
	counts := []int{payload.CreateCount, payload.UpdateCount, payload.UnchangedCount, payload.ArchiveCount, payload.BlockedCount, payload.DiagnosticCount}
	for _, count := range counts {
		if count < 0 || count > maxNormalizedObjects {
			return errors.New("audit payload count is outside the allowed range")
		}
	}
	return nil
}

func digestAuditEvent(event AuditEvent) (string, error) {
	event.EventDigest = ""
	body, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
