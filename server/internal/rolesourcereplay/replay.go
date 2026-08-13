package rolesourcereplay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	AuthorizationContractVersion = "1.0"
	ReplayReceiptContractVersion = "1.0"
	AuthorizationTTL             = 15 * time.Minute
)

var ErrNotReplayable = errors.New("role source outbox event is not replayable")

type Summary struct {
	OutboxID              string    `json:"outbox_id"`
	WorkspaceID           string    `json:"workspace_id"`
	SourceID              string    `json:"source_id"`
	ApplyID               string    `json:"apply_id"`
	Mode                  string    `json:"mode"`
	Status                string    `json:"status"`
	Attempt               int16     `json:"attempt"`
	ReplayCount           int16     `json:"replay_count"`
	NextGeneration        int16     `json:"next_generation"`
	ExpectedReceiptDigest string    `json:"expected_receipt_digest"`
	LastErrorCode         string    `json:"last_error_code,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type Authorization struct {
	ContractVersion         string    `json:"contract_version"`
	AuthorizationID         string    `json:"authorization_id"`
	OutboxID                string    `json:"outbox_id"`
	ExpectedGeneration      int16     `json:"expected_generation"`
	ExpectedReceiptDigest   string    `json:"expected_receipt_digest"`
	ReasonCode              string    `json:"reason_code"`
	IncidentReferenceDigest string    `json:"incident_reference_digest"`
	RequestedAt             time.Time `json:"requested_at"`
	ExpiresAt               time.Time `json:"expires_at"`
}

type Signature struct {
	KeyID string
	Value []byte
}

type ExecuteInput struct {
	Authorization Authorization
	Requester     Signature
	Approver      Signature
	PublicKeys    map[string]ed25519.PublicKey
	Now           time.Time
}

type ReplayReceipt struct {
	ContractVersion          string    `json:"contract_version"`
	ReplayID                 string    `json:"replay_id"`
	OutboxID                 string    `json:"outbox_id"`
	WorkspaceID              string    `json:"workspace_id"`
	SourceID                 string    `json:"source_id"`
	ApplyID                  string    `json:"apply_id"`
	AuthorizationID          string    `json:"authorization_id"`
	Generation               int16     `json:"generation"`
	ReasonCode               string    `json:"reason_code"`
	IncidentReferenceDigest  string    `json:"incident_reference_digest"`
	RequesterKeyID           string    `json:"requester_key_id"`
	ApproverKeyID            string    `json:"approver_key_id"`
	AuthorizationDigest      string    `json:"authorization_digest"`
	RequesterSignatureDigest string    `json:"requester_signature_digest"`
	ApproverSignatureDigest  string    `json:"approver_signature_digest"`
	ExpectedReceiptDigest    string    `json:"expected_receipt_digest"`
	PreviousReplayDigest     string    `json:"previous_replay_digest,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
	ReplayDigest             string    `json:"replay_digest"`
}

func Inspect(ctx context.Context, pool *pgxpool.Pool, outboxIDText string) (Summary, error) {
	outboxID, err := util.ParseUUID(outboxIDText)
	if err != nil {
		return Summary{}, err
	}
	q := db.New(pool)
	row, err := q.GetRoleSourceOutboxByID(ctx, outboxID)
	if err != nil {
		return Summary{}, err
	}
	if err := validateEvidence(ctx, q, row); err != nil {
		return Summary{}, err
	}
	return summary(row), nil
}

func NewAuthorization(summary Summary, reasonCode, incidentReferenceDigest string, now time.Time) (Authorization, error) {
	if summary.Status != "dead" || summary.Attempt != 20 || summary.NextGeneration < 1 || summary.NextGeneration > 3 {
		return Authorization{}, ErrNotReplayable
	}
	if !validReason(reasonCode) || !validDigest(incidentReferenceDigest) || !validDigest(summary.ExpectedReceiptDigest) {
		return Authorization{}, errors.New("invalid replay authorization fields")
	}
	return Authorization{
		ContractVersion: AuthorizationContractVersion, AuthorizationID: uuid.NewString(), OutboxID: summary.OutboxID,
		ExpectedGeneration: summary.NextGeneration, ExpectedReceiptDigest: summary.ExpectedReceiptDigest,
		ReasonCode: reasonCode, IncidentReferenceDigest: incidentReferenceDigest,
		RequestedAt: now.UTC(), ExpiresAt: now.UTC().Add(AuthorizationTTL),
	}, nil
}

func CanonicalAuthorization(auth Authorization) ([]byte, error) {
	if auth.ContractVersion != AuthorizationContractVersion || auth.ExpectedGeneration < 1 || auth.ExpectedGeneration > 3 ||
		!validReason(auth.ReasonCode) || !validDigest(auth.IncidentReferenceDigest) || !validDigest(auth.ExpectedReceiptDigest) {
		return nil, errors.New("invalid replay authorization")
	}
	if _, err := uuid.Parse(auth.AuthorizationID); err != nil {
		return nil, errors.New("invalid authorization id")
	}
	if _, err := uuid.Parse(auth.OutboxID); err != nil {
		return nil, errors.New("invalid outbox id")
	}
	if auth.RequestedAt.IsZero() || auth.ExpiresAt.Sub(auth.RequestedAt) != AuthorizationTTL {
		return nil, errors.New("invalid authorization lifetime")
	}
	auth.RequestedAt = auth.RequestedAt.UTC()
	auth.ExpiresAt = auth.ExpiresAt.UTC()
	return json.Marshal(auth)
}

func Execute(ctx context.Context, pool *pgxpool.Pool, input ExecuteInput) (ReplayReceipt, error) {
	canonical, err := CanonicalAuthorization(input.Authorization)
	if err != nil {
		return ReplayReceipt{}, err
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Microsecond)
	if err := verifySignatures(canonical, input); err != nil {
		return ReplayReceipt{}, err
	}
	outboxID, _ := util.ParseUUID(input.Authorization.OutboxID)
	authorizationID, _ := util.ParseUUID(input.Authorization.AuthorizationID)
	if existing, lookupErr := db.New(pool).GetRoleSourceOutboxReplayByAuthorization(ctx, authorizationID); lookupErr == nil {
		return matchExistingReplay(existing, input, canonical)
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return ReplayReceipt{}, lookupErr
	}
	// A consumed authorization remains safely idempotent after expiry (handled
	// above). Only a new state transition must fall within the 15-minute window.
	if now.Before(input.Authorization.RequestedAt) || !now.Before(input.Authorization.ExpiresAt) {
		return ReplayReceipt{}, errors.New("replay authorization is not currently valid")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ReplayReceipt{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	q := db.New(tx)
	row, err := q.GetRoleSourceOutboxForReplay(ctx, outboxID)
	if err != nil {
		if isReplayConcurrencyError(err) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReplay(ctx, pool, authorizationID, input, canonical, err)
		}
		return ReplayReceipt{}, err
	}
	if row.Status != "dead" || row.Attempt != 20 || row.ReplayCount+1 != input.Authorization.ExpectedGeneration || row.ReplayCount >= 3 ||
		row.ReceiptDigest != input.Authorization.ExpectedReceiptDigest {
		_ = tx.Rollback(context.Background())
		return reconcileExistingReplay(ctx, pool, authorizationID, input, canonical, ErrNotReplayable)
	}
	if err := validateEvidence(ctx, q, row); err != nil {
		return ReplayReceipt{}, err
	}
	previous := ""
	latest, latestErr := q.GetLatestRoleSourceOutboxReplay(ctx, row.ID)
	if latestErr == nil {
		if latest.Generation != row.ReplayCount || latest.ReplayDigest == "" {
			return ReplayReceipt{}, errors.New("replay receipt chain is inconsistent")
		}
		previous = latest.ReplayDigest
	} else if !errors.Is(latestErr, pgx.ErrNoRows) || row.ReplayCount != 0 {
		return ReplayReceipt{}, errors.New("replay receipt chain is incomplete")
	}
	replayID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	receipt := ReplayReceipt{
		ContractVersion: ReplayReceiptContractVersion, ReplayID: util.UUIDToString(replayID), OutboxID: input.Authorization.OutboxID,
		WorkspaceID: util.UUIDToString(row.WorkspaceID), SourceID: util.UUIDToString(row.SourceID), ApplyID: util.UUIDToString(row.ApplyID),
		AuthorizationID: input.Authorization.AuthorizationID, Generation: input.Authorization.ExpectedGeneration,
		ReasonCode: input.Authorization.ReasonCode, IncidentReferenceDigest: input.Authorization.IncidentReferenceDigest,
		RequesterKeyID: input.Requester.KeyID, ApproverKeyID: input.Approver.KeyID,
		AuthorizationDigest: digest(canonical), RequesterSignatureDigest: digest(input.Requester.Value), ApproverSignatureDigest: digest(input.Approver.Value),
		ExpectedReceiptDigest: input.Authorization.ExpectedReceiptDigest, PreviousReplayDigest: previous, CreatedAt: now,
	}
	receipt.ReplayDigest, err = digestReceipt(receipt)
	if err != nil {
		return ReplayReceipt{}, err
	}
	_, err = q.InsertRoleSourceOutboxReplay(ctx, db.InsertRoleSourceOutboxReplayParams{
		ID: replayID, OutboxID: row.ID, WorkspaceID: row.WorkspaceID, SourceID: row.SourceID, ApplyID: row.ApplyID,
		AuthorizationID: authorizationID, Generation: receipt.Generation, ReasonCode: receipt.ReasonCode,
		IncidentReferenceDigest: receipt.IncidentReferenceDigest, RequesterKeyID: receipt.RequesterKeyID, ApproverKeyID: receipt.ApproverKeyID,
		AuthorizationDigest: receipt.AuthorizationDigest, RequesterSignatureDigest: receipt.RequesterSignatureDigest,
		ApproverSignatureDigest: receipt.ApproverSignatureDigest, ExpectedReceiptDigest: receipt.ExpectedReceiptDigest,
		PreviousReplayDigest: pgtype.Text{String: previous, Valid: previous != ""}, ReplayDigest: receipt.ReplayDigest,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if isReplayConcurrencyError(err) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReplay(ctx, pool, authorizationID, input, canonical, err)
		}
		return ReplayReceipt{}, err
	}
	if _, err := q.RequeueDeadRoleSourceOutboxEvent(ctx, db.RequeueDeadRoleSourceOutboxEventParams{
		ReplayedAt: pgtype.Timestamptz{Time: now, Valid: true}, ID: row.ID, ExpectedReplayCount: row.ReplayCount,
	}); err != nil {
		if isReplayConcurrencyError(err) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReplay(ctx, pool, authorizationID, input, canonical, err)
		}
		return ReplayReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return reconcileExistingReplay(ctx, pool, authorizationID, input, canonical, err)
	}
	return receipt, nil
}

func reconcileExistingReplay(ctx context.Context, pool *pgxpool.Pool, authorizationID pgtype.UUID, input ExecuteInput, canonical []byte, fallback error) (ReplayReceipt, error) {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	existing, err := db.New(pool).GetRoleSourceOutboxReplayByAuthorization(reconcileCtx, authorizationID)
	if err == nil {
		return matchExistingReplay(existing, input, canonical)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ReplayReceipt{}, fallback
	}
	return ReplayReceipt{}, err
}

func isReplayConcurrencyError(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && (databaseError.Code == "40001" || databaseError.Code == "23505")
}

func matchExistingReplay(row db.RoleSourceOutboxReplay, input ExecuteInput, canonical []byte) (ReplayReceipt, error) {
	receipt, err := DecodePersistedReplayReceipt(row)
	if err != nil || receipt.OutboxID != input.Authorization.OutboxID || receipt.Generation != input.Authorization.ExpectedGeneration ||
		receipt.ExpectedReceiptDigest != input.Authorization.ExpectedReceiptDigest || receipt.ReasonCode != input.Authorization.ReasonCode ||
		receipt.IncidentReferenceDigest != input.Authorization.IncidentReferenceDigest || receipt.AuthorizationDigest != digest(canonical) ||
		receipt.RequesterKeyID != input.Requester.KeyID || receipt.ApproverKeyID != input.Approver.KeyID ||
		receipt.RequesterSignatureDigest != digest(input.Requester.Value) || receipt.ApproverSignatureDigest != digest(input.Approver.Value) {
		return ReplayReceipt{}, errors.New("replay authorization conflicts with its existing receipt")
	}
	return receipt, nil
}

// DecodePersistedReplayReceipt reconstructs and verifies the immutable replay
// chain row for idempotent operator retries and disaster-recovery validation.
func DecodePersistedReplayReceipt(row db.RoleSourceOutboxReplay) (ReplayReceipt, error) {
	receipt := ReplayReceipt{
		ContractVersion: ReplayReceiptContractVersion, ReplayID: util.UUIDToString(row.ID), OutboxID: util.UUIDToString(row.OutboxID),
		WorkspaceID: util.UUIDToString(row.WorkspaceID), SourceID: util.UUIDToString(row.SourceID), ApplyID: util.UUIDToString(row.ApplyID),
		AuthorizationID: util.UUIDToString(row.AuthorizationID), Generation: row.Generation, ReasonCode: row.ReasonCode,
		IncidentReferenceDigest: row.IncidentReferenceDigest, RequesterKeyID: row.RequesterKeyID, ApproverKeyID: row.ApproverKeyID,
		AuthorizationDigest: row.AuthorizationDigest, RequesterSignatureDigest: row.RequesterSignatureDigest,
		ApproverSignatureDigest: row.ApproverSignatureDigest, ExpectedReceiptDigest: row.ExpectedReceiptDigest,
		PreviousReplayDigest: auditText(row.PreviousReplayDigest), CreatedAt: row.CreatedAt.Time.UTC(), ReplayDigest: row.ReplayDigest,
	}
	computed, err := digestReceipt(receipt)
	if err != nil || computed != receipt.ReplayDigest || receipt.Generation < 1 || receipt.Generation > 3 ||
		!validReason(receipt.ReasonCode) || !validKeyID(receipt.RequesterKeyID) || !validKeyID(receipt.ApproverKeyID) ||
		receipt.RequesterKeyID == receipt.ApproverKeyID || !validDigest(receipt.IncidentReferenceDigest) ||
		!validDigest(receipt.AuthorizationDigest) || !validDigest(receipt.RequesterSignatureDigest) ||
		!validDigest(receipt.ApproverSignatureDigest) || !validDigest(receipt.ExpectedReceiptDigest) ||
		(receipt.Generation == 1) != (receipt.PreviousReplayDigest == "") {
		return ReplayReceipt{}, errors.New("persisted replay receipt is invalid")
	}
	return receipt, nil
}

func auditText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

type evidenceQueries interface {
	GetRoleSourceApplyForOutboxReplay(context.Context, db.GetRoleSourceApplyForOutboxReplayParams) (db.RoleSourceApply, error)
	ListRoleSourceApplyAuditEventsForOutboxReplay(context.Context, db.ListRoleSourceApplyAuditEventsForOutboxReplayParams) ([]db.RoleSourceAuditEvent, error)
	ListRoleSourceAuditChainForOutboxReplay(context.Context, db.ListRoleSourceAuditChainForOutboxReplayParams) ([]db.RoleSourceAuditEvent, error)
}

func validateEvidence(ctx context.Context, q evidenceQueries, row db.RoleSourceOutbox) error {
	apply, err := q.GetRoleSourceApplyForOutboxReplay(ctx, db.GetRoleSourceApplyForOutboxReplayParams{ApplyID: row.ApplyID, WorkspaceID: row.WorkspaceID, SourceID: row.SourceID})
	if err != nil {
		return fmt.Errorf("load successful apply: %w", err)
	}
	receipt, err := rolesource.DecodePersistedApplyReceipt(apply)
	if err != nil || receipt.ReceiptDigest != row.ReceiptDigest || receipt.PlanDigest != row.PlanDigest || receipt.SnapshotDigest != row.SnapshotDigest || receipt.Mode != row.Mode {
		return errors.New("outbox event does not match its verified apply receipt")
	}
	if row.ActorType != "user" || !row.ActorID.Valid || row.ActorID != apply.ActorUserID {
		return errors.New("outbox event actor does not match its successful apply")
	}
	eventType := "apply_succeeded"
	if row.Mode == "rollback" {
		eventType = "rollback_succeeded"
	}
	events, err := q.ListRoleSourceApplyAuditEventsForOutboxReplay(ctx, db.ListRoleSourceApplyAuditEventsForOutboxReplayParams{
		WorkspaceID: row.WorkspaceID, SourceID: row.SourceID, EventType: eventType,
		ApplyID: util.UUIDToString(row.ApplyID), ReceiptDigest: row.ReceiptDigest,
	})
	if err != nil || len(events) != 1 {
		return errors.New("outbox event requires exactly one matching apply audit event")
	}
	event, err := rolesource.DecodePersistedAuditEvent(events[0])
	if err != nil {
		return fmt.Errorf("matching apply audit event failed digest validation: %w", err)
	}
	if event.Payload.PlanDigest != row.PlanDigest || event.Payload.SnapshotDigest != row.SnapshotDigest || event.Payload.Result != "succeeded" {
		return errors.New("matching apply audit event commitments are invalid")
	}
	if event.Actor.Type != "user" || event.Actor.ID != util.UUIDToString(apply.ActorUserID) {
		return errors.New("matching apply audit actor is inconsistent")
	}
	if event.Sequence < 1 || event.Sequence > 100000 {
		return errors.New("matching apply audit sequence is outside the replay verification limit")
	}
	chain, err := q.ListRoleSourceAuditChainForOutboxReplay(ctx, db.ListRoleSourceAuditChainForOutboxReplayParams{
		WorkspaceID: row.WorkspaceID, SourceID: row.SourceID, ThroughSequence: event.Sequence, ResultLimit: 100001,
	})
	if err != nil || len(chain) != int(event.Sequence) || len(chain) > 100000 {
		return errors.New("apply audit chain is incomplete or exceeds the replay verification limit")
	}
	previousDigest := ""
	for index, chainRow := range chain {
		decoded, err := rolesource.DecodePersistedAuditEvent(chainRow)
		if err != nil {
			return fmt.Errorf("apply audit chain event %d failed digest validation: %w", index+1, err)
		}
		if decoded.Sequence != int64(index+1) || decoded.PreviousEventDigest != previousDigest {
			return errors.New("apply audit chain is discontinuous")
		}
		previousDigest = decoded.EventDigest
	}
	if chain[len(chain)-1].ID != events[0].ID {
		return errors.New("matching apply audit event is not the verified chain tip")
	}
	return nil
}

func summary(row db.RoleSourceOutbox) Summary {
	lastError := ""
	if row.LastErrorCode.Valid {
		lastError = row.LastErrorCode.String
	}
	return Summary{OutboxID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), SourceID: util.UUIDToString(row.SourceID),
		ApplyID: util.UUIDToString(row.ApplyID), Mode: row.Mode, Status: row.Status, Attempt: row.Attempt, ReplayCount: row.ReplayCount,
		NextGeneration: row.ReplayCount + 1, ExpectedReceiptDigest: row.ReceiptDigest, LastErrorCode: lastError, CreatedAt: row.CreatedAt.Time.UTC()}
}

func verifySignatures(message []byte, input ExecuteInput) error {
	if !validKeyID(input.Requester.KeyID) || !validKeyID(input.Approver.KeyID) || input.Requester.KeyID == input.Approver.KeyID {
		return errors.New("two distinct replay signer key ids are required")
	}
	requester, requesterOK := input.PublicKeys[input.Requester.KeyID]
	approver, approverOK := input.PublicKeys[input.Approver.KeyID]
	if !requesterOK || !approverOK || len(requester) != ed25519.PublicKeySize || len(approver) != ed25519.PublicKeySize || bytes.Equal(requester, approver) {
		return errors.New("replay signer is not in the configured keyring")
	}
	if !ed25519.Verify(requester, message, input.Requester.Value) || !ed25519.Verify(approver, message, input.Approver.Value) {
		return errors.New("replay authorization signature is invalid")
	}
	return nil
}

func DecodePublicKeyring(raw []byte) (map[string]ed25519.PublicKey, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("replay keyring must be one JSON object")
	}
	encoded := map[string]string{}
	seenIDs := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		id, ok := keyToken.(string)
		if err != nil || !ok || seenIDs[id] {
			return nil, errors.New("replay keyring contains an invalid or duplicate key id")
		}
		seenIDs[id] = true
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("replay keyring values must be strings")
		}
		encoded[id] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("replay keyring object is incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || len(encoded) < 2 || len(encoded) > 32 {
		return nil, errors.New("replay keyring must contain 2 to 32 keys")
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	seenKeys := make(map[string]bool, len(encoded))
	for id, value := range encoded {
		if !validKeyID(id) {
			return nil, errors.New("invalid replay key id")
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid replay public key %q", id)
		}
		fingerprint := string(decoded)
		if seenKeys[fingerprint] {
			return nil, errors.New("replay keyring contains one public key under multiple ids")
		}
		seenKeys[fingerprint] = true
		keys[id] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func digestReceipt(receipt ReplayReceipt) (string, error) {
	receipt.ReplayDigest = ""
	receipt.CreatedAt = receipt.CreatedAt.UTC().Truncate(time.Microsecond)
	body, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return digest(body), nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

func validReason(value string) bool {
	return value == "dependency_recovered" || value == "incident_recovery" || value == "delivery_reconciliation"
}

func validKeyID(value string) bool {
	if len(value) < 2 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
