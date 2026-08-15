package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	EvidenceContractVersion          = "1.0"
	AmbiguityEvidenceContractVersion = "2.0"
	OperationChatReply               = "chat_reply"
	OperationFailureNotice           = "failure_notice"
	AmbiguityResponseUnknown         = "response_unknown"
	AmbiguityPartialDelivery         = "partial_delivery"
	AmbiguityReceiptPersistFailed    = "receipt_persist_failed"
	AmbiguityLeaseExpired            = "lease_expired"
	AmbiguityMissingProviderID       = "missing_provider_id"
)

var (
	ErrIdempotencyConflict    = errors.New("channel delivery idempotency conflict")
	ErrClaimLost              = errors.New("channel delivery claim lost")
	ErrInvalidEvidence        = errors.New("invalid channel delivery evidence")
	ErrRetryExhausted         = errors.New("channel delivery retry limit exhausted")
	ErrAmbiguous              = errors.New("channel delivery provider acceptance is ambiguous")
	ErrAuthorizedRetryPending = errors.New("channel delivery retry is authorized but not yet consumed")
)

type Queries interface {
	ClaimChannelDelivery(context.Context, db.ClaimChannelDeliveryParams) (db.ChannelDelivery, error)
	GetChannelDeliveryByIdentity(context.Context, db.GetChannelDeliveryByIdentityParams) (db.ChannelDelivery, error)
	CompleteChannelDelivery(context.Context, db.CompleteChannelDeliveryParams) (db.ChannelDelivery, error)
	FailChannelDelivery(context.Context, db.FailChannelDeliveryParams) (db.ChannelDelivery, error)
	MarkChannelDeliveryAmbiguous(context.Context, db.MarkChannelDeliveryAmbiguousParams) (db.ChannelDelivery, error)
	GetChannelDeliveryByExternalMessage(context.Context, db.GetChannelDeliveryByExternalMessageParams) (db.ChannelDelivery, error)
	MarkChannelDeliveryReadback(context.Context, db.MarkChannelDeliveryReadbackParams) (db.ChannelDelivery, error)
	ListChannelDeliveriesByWorkspace(context.Context, db.ListChannelDeliveriesByWorkspaceParams) ([]db.ChannelDelivery, error)
	ClaimExpiredChannelDeliveryLeases(context.Context, int32) ([]db.ChannelDelivery, error)
}

type authorizedRetryQueries interface {
	ClaimAuthorizedChannelDeliveryRetries(context.Context, int32) ([]db.ChannelDelivery, error)
	GetChannelDeliveryByID(context.Context, pgtype.UUID) (db.ChannelDelivery, error)
}

type reconciliationReadQueries interface {
	ListChannelDeliveryReconciliationsByDelivery(context.Context, pgtype.UUID) ([]db.ChannelDeliveryReconciliation, error)
}

type reconciliationWorkspaceReadQueries interface {
	ListChannelDeliveryReconciliationsByWorkspaceDeliveries(context.Context, db.ListChannelDeliveryReconciliationsByWorkspaceDeliveriesParams) ([]db.ChannelDeliveryReconciliation, error)
}

type Ledger struct {
	q       Queries
	now     func() time.Time
	metrics Metrics
}

type Metrics interface {
	RecordChannelDeliveryTransition(connector, operation, status, errorCode string)
}

func NewLedger(q Queries) *Ledger {
	return &Ledger{q: q, now: func() time.Time { return time.Now().UTC() }}
}

func (l *Ledger) SetMetrics(metrics Metrics) { l.metrics = metrics }

type ClaimInput struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	TaskID         pgtype.UUID
	ChatSessionID  pgtype.UUID
	ChannelType    channel.Type
	ChannelChatID  string
	OperationKind  string
	Payload        string
}

type Claim struct {
	Row        db.ChannelDelivery
	ShouldSend bool
}

type Recorder interface {
	Claim(context.Context, ClaimInput) (Claim, error)
	MarkDelivered(context.Context, Claim, string) (db.ChannelDelivery, error)
	MarkFailed(context.Context, Claim, string) error
	MarkAmbiguous(context.Context, Claim, string, string) (db.ChannelDelivery, error)
}

type ReadbackRecorder interface {
	MarkReadback(context.Context, pgtype.UUID, string, string) (db.ChannelDelivery, error)
}

type Evidence struct {
	ContractVersion   string `json:"contract_version"`
	DeliveryID        string `json:"delivery_id"`
	CorrelationID     string `json:"correlation_id"`
	WorkspaceID       string `json:"workspace_id"`
	TaskID            string `json:"task_id"`
	ChatSessionID     string `json:"chat_session_id"`
	ChannelType       string `json:"channel_type"`
	ChannelChatID     string `json:"channel_chat_id"`
	OperationKind     string `json:"operation_kind"`
	PayloadDigest     string `json:"payload_digest"`
	Status            string `json:"status"`
	AttemptCount      int32  `json:"attempt_count"`
	ExternalMessageID string `json:"external_message_id"`
	DeliveredAt       string `json:"delivered_at"`
	ReadbackMessageID string `json:"readback_message_id,omitempty"`
	ReadbackAt        string `json:"readback_at,omitempty"`
	AmbiguityReason   string `json:"ambiguity_reason,omitempty"`
	AmbiguousAt       string `json:"ambiguous_at,omitempty"`
}

func PayloadDigest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (l *Ledger) Claim(ctx context.Context, input ClaimInput) (Claim, error) {
	if l == nil || l.q == nil {
		return Claim{}, errors.New("channel delivery ledger is not configured")
	}
	if !input.WorkspaceID.Valid || !input.InstallationID.Valid || !input.TaskID.Valid || !input.ChatSessionID.Valid ||
		input.ChannelType == "" || strings.TrimSpace(input.ChannelChatID) == "" || !validOperation(input.OperationKind) {
		return Claim{}, errors.New("invalid channel delivery claim")
	}
	payloadDigest := PayloadDigest(input.Payload)
	row, err := l.q.ClaimChannelDelivery(ctx, db.ClaimChannelDeliveryParams{
		WorkspaceID: input.WorkspaceID, InstallationID: input.InstallationID, TaskID: input.TaskID,
		ChatSessionID: input.ChatSessionID, ChannelType: string(input.ChannelType), ChannelChatID: input.ChannelChatID,
		OperationKind: input.OperationKind, PayloadDigest: payloadDigest,
	})
	if err == nil {
		if !row.LeaseToken.Valid || row.Status != "pending" {
			return Claim{}, ErrClaimLost
		}
		return Claim{Row: row, ShouldSend: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, err
	}
	existing, err := l.q.GetChannelDeliveryByIdentity(ctx, db.GetChannelDeliveryByIdentityParams{
		InstallationID: input.InstallationID, TaskID: input.TaskID, OperationKind: input.OperationKind,
	})
	if err != nil {
		return Claim{}, err
	}
	if existing.PayloadDigest != payloadDigest || existing.WorkspaceID != input.WorkspaceID || existing.ChatSessionID != input.ChatSessionID ||
		existing.ChannelType != string(input.ChannelType) || existing.ChannelChatID != input.ChannelChatID {
		return Claim{}, ErrIdempotencyConflict
	}
	if existing.Status == "delivered" || existing.Status == "readback" {
		if _, err := ValidateRow(existing); err != nil {
			return Claim{}, err
		}
	}
	if (existing.Status == "reconciled" || existing.Status == "retry_authorized") && existing.ReconciliationCount == 0 {
		return Claim{}, ErrInvalidEvidence
	}
	if existing.ReconciliationCount > 0 {
		latest, err := l.LatestReconciliation(ctx, existing)
		if err != nil {
			return Claim{}, err
		}
		if existing.Status == "reconciled" && (latest == nil || latest.Outcome == ReconciliationConfirmedNotDelivered) {
			return Claim{}, ErrInvalidEvidence
		}
		if existing.Status == "retry_authorized" {
			if latest == nil || latest.Outcome != ReconciliationConfirmedNotDelivered {
				return Claim{}, ErrInvalidEvidence
			}
			return Claim{}, ErrAuthorizedRetryPending
		}
	}
	if existing.Status == "ambiguous" {
		if _, err := ValidateRow(existing); err != nil {
			return Claim{}, err
		}
		return Claim{}, ErrAmbiguous
	}
	if existing.AttemptCount >= 100 && existing.Status == "failed" {
		return Claim{}, ErrRetryExhausted
	}
	return Claim{Row: existing, ShouldSend: false}, nil
}

func (l *Ledger) MarkDelivered(ctx context.Context, claim Claim, externalMessageID string) (db.ChannelDelivery, error) {
	if !claim.ShouldSend || !claim.Row.LeaseToken.Valid || strings.TrimSpace(externalMessageID) == "" {
		return db.ChannelDelivery{}, ErrClaimLost
	}
	// PostgreSQL timestamptz stores microsecond precision. Canonical evidence
	// must use the same precision or a successful round trip would look tampered.
	deliveredAt := l.now().UTC().Truncate(time.Microsecond)
	evidence := evidenceFromRow(claim.Row)
	evidence.Status = "delivered"
	evidence.ExternalMessageID = externalMessageID
	evidence.DeliveredAt = deliveredAt.Format(time.RFC3339Nano)
	body, digest, err := encodeEvidence(evidence)
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	row, err := l.q.CompleteChannelDelivery(ctx, db.CompleteChannelDeliveryParams{
		ID: claim.Row.ID, LeaseToken: claim.Row.LeaseToken,
		ExternalMessageID: pgtype.Text{String: externalMessageID, Valid: true}, Evidence: body,
		EvidenceDigest: pgtype.Text{String: digest, Valid: true}, DeliveredAt: pgtype.Timestamptz{Time: deliveredAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelDelivery{}, ErrClaimLost
	}
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	if _, err := ValidateRow(row); err != nil {
		return db.ChannelDelivery{}, err
	}
	if l.metrics != nil {
		l.metrics.RecordChannelDeliveryTransition(row.ChannelType, row.OperationKind, "delivered", "none")
	}
	return row, nil
}

func (l *Ledger) MarkFailed(ctx context.Context, claim Claim, errorCode string) error {
	if !claim.ShouldSend || !claim.Row.LeaseToken.Valid {
		return ErrClaimLost
	}
	errorCode = normalizeErrorCode(errorCode)
	row, err := l.q.FailChannelDelivery(ctx, db.FailChannelDeliveryParams{
		ID: claim.Row.ID, LeaseToken: claim.Row.LeaseToken, LastErrorCode: pgtype.Text{String: errorCode, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrClaimLost
	}
	if err == nil && l.metrics != nil {
		l.metrics.RecordChannelDeliveryTransition(row.ChannelType, row.OperationKind, "failed", errorCode)
	}
	return err
}

func (l *Ledger) MarkAmbiguous(ctx context.Context, claim Claim, reason, externalMessageID string) (db.ChannelDelivery, error) {
	if !claim.ShouldSend || !claim.Row.LeaseToken.Valid || !validAmbiguityReason(reason) {
		return db.ChannelDelivery{}, ErrClaimLost
	}
	ambiguousAt := l.now().UTC().Truncate(time.Microsecond)
	evidence := evidenceFromRow(claim.Row)
	evidence.ContractVersion = AmbiguityEvidenceContractVersion
	evidence.Status = "ambiguous"
	evidence.ExternalMessageID = strings.TrimSpace(externalMessageID)
	evidence.AmbiguityReason = reason
	evidence.AmbiguousAt = ambiguousAt.Format(time.RFC3339Nano)
	body, digest, err := encodeEvidence(evidence)
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	row, err := l.q.MarkChannelDeliveryAmbiguous(ctx, db.MarkChannelDeliveryAmbiguousParams{
		ID: claim.Row.ID, LeaseToken: claim.Row.LeaseToken,
		ExternalMessageID: pgtype.Text{String: evidence.ExternalMessageID, Valid: evidence.ExternalMessageID != ""},
		Evidence:          body, EvidenceDigest: pgtype.Text{String: digest, Valid: true},
		LastErrorCode: pgtype.Text{String: reason, Valid: true},
		AmbiguousAt:   pgtype.Timestamptz{Time: ambiguousAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelDelivery{}, ErrClaimLost
	}
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	if _, err := ValidateRow(row); err != nil {
		return db.ChannelDelivery{}, err
	}
	if l.metrics != nil {
		l.metrics.RecordChannelDeliveryTransition(row.ChannelType, row.OperationKind, "ambiguous", reason)
	}
	return row, nil
}

func (l *Ledger) MarkReadback(ctx context.Context, installationID pgtype.UUID, externalMessageID, inboundMessageID string) (db.ChannelDelivery, error) {
	if !installationID.Valid || externalMessageID == "" || inboundMessageID == "" {
		return db.ChannelDelivery{}, errors.New("invalid channel delivery readback")
	}
	row, err := l.q.GetChannelDeliveryByExternalMessage(ctx, db.GetChannelDeliveryByExternalMessageParams{
		InstallationID: installationID, ExternalMessageID: pgtype.Text{String: externalMessageID, Valid: true},
	})
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	evidence, err := ValidateRow(row)
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	if row.Status == "readback" {
		return row, nil
	}
	if row.Status != "delivered" {
		return db.ChannelDelivery{}, ErrClaimLost
	}
	readbackAt := l.now().UTC().Truncate(time.Microsecond)
	evidence.Status = "readback"
	evidence.ReadbackMessageID = inboundMessageID
	evidence.ReadbackAt = readbackAt.Format(time.RFC3339Nano)
	body, digest, err := encodeEvidence(evidence)
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	updated, err := l.q.MarkChannelDeliveryReadback(ctx, db.MarkChannelDeliveryReadbackParams{
		ID: row.ID, ExternalMessageID: row.ExternalMessageID, Evidence: body,
		EvidenceDigest: pgtype.Text{String: digest, Valid: true}, ReadbackAt: pgtype.Timestamptz{Time: readbackAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent duplicate reply may have advanced the same provider
		// message first. Re-read and accept only a valid terminal readback;
		// every other lost compare-and-swap remains an error.
		current, loadErr := l.q.GetChannelDeliveryByExternalMessage(ctx, db.GetChannelDeliveryByExternalMessageParams{
			InstallationID: installationID, ExternalMessageID: pgtype.Text{String: externalMessageID, Valid: true},
		})
		if loadErr == nil && current.Status == "readback" {
			if _, validateErr := ValidateRow(current); validateErr == nil {
				return current, nil
			}
		}
		return db.ChannelDelivery{}, ErrClaimLost
	}
	if err != nil {
		return db.ChannelDelivery{}, err
	}
	if _, err := ValidateRow(updated); err != nil {
		return db.ChannelDelivery{}, err
	}
	if l.metrics != nil {
		l.metrics.RecordChannelDeliveryTransition(updated.ChannelType, updated.OperationKind, "readback", "none")
	}
	return updated, nil
}

func (l *Ledger) List(ctx context.Context, workspaceID pgtype.UUID, limit int32) ([]db.ChannelDelivery, error) {
	records, err := l.ListRecords(ctx, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	rows := make([]db.ChannelDelivery, 0, len(records))
	for _, record := range records {
		rows = append(rows, record.Row)
	}
	return rows, nil
}

type Record struct {
	Row            db.ChannelDelivery
	Reconciliation *ReconciliationReceipt
}

func (l *Ledger) ListRecords(ctx context.Context, workspaceID pgtype.UUID, limit int32) ([]Record, error) {
	if !workspaceID.Valid || limit < 1 || limit > 100 {
		return nil, errors.New("invalid channel delivery list request")
	}
	rows, err := l.q.ListChannelDeliveriesByWorkspace(ctx, db.ListChannelDeliveriesByWorkspaceParams{WorkspaceID: workspaceID, Limit: limit})
	if err != nil {
		return nil, err
	}
	needsReconciliations := false
	for _, row := range rows {
		if row.ReconciliationCount > 0 {
			needsReconciliations = true
			break
		}
	}
	reconciliationsByDelivery := map[pgtype.UUID][]db.ChannelDeliveryReconciliation{}
	if needsReconciliations {
		q, ok := l.q.(reconciliationWorkspaceReadQueries)
		if !ok {
			return nil, errors.New("channel delivery reconciliation listing is not configured")
		}
		deliveryIDs := make([]pgtype.UUID, 0, len(rows))
		for _, row := range rows {
			if row.ReconciliationCount > 0 {
				deliveryIDs = append(deliveryIDs, row.ID)
			}
		}
		reconciliations, err := q.ListChannelDeliveryReconciliationsByWorkspaceDeliveries(ctx, db.ListChannelDeliveryReconciliationsByWorkspaceDeliveriesParams{
			WorkspaceID: workspaceID,
			DeliveryIds: deliveryIDs,
		})
		if err != nil {
			return nil, err
		}
		for _, reconciliation := range reconciliations {
			reconciliationsByDelivery[reconciliation.DeliveryID] = append(reconciliationsByDelivery[reconciliation.DeliveryID], reconciliation)
		}
	}
	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		if row.Status == "delivered" || row.Status == "readback" || row.Status == "ambiguous" {
			if _, err := ValidateRow(row); err != nil {
				return nil, fmt.Errorf("delivery %s: %w", util.UUIDToString(row.ID), err)
			}
		}
		var latest *ReconciliationReceipt
		if row.ReconciliationCount > 0 {
			latest, err = validateReconciliationRows(row, reconciliationsByDelivery[row.ID])
			if err != nil {
				return nil, fmt.Errorf("delivery %s reconciliation: %w", util.UUIDToString(row.ID), err)
			}
		}
		if row.Status == "retry_authorized" && (latest == nil || latest.Outcome != ReconciliationConfirmedNotDelivered) {
			return nil, fmt.Errorf("delivery %s: %w", util.UUIDToString(row.ID), ErrInvalidEvidence)
		}
		if row.Status == "reconciled" && (latest == nil || latest.Outcome == ReconciliationConfirmedNotDelivered) {
			return nil, fmt.Errorf("delivery %s: %w", util.UUIDToString(row.ID), ErrInvalidEvidence)
		}
		records = append(records, Record{Row: row, Reconciliation: latest})
	}
	return records, nil
}

func (l *Ledger) LatestReconciliation(ctx context.Context, row db.ChannelDelivery) (*ReconciliationReceipt, error) {
	if l == nil || l.q == nil || !row.ID.Valid || row.ReconciliationCount < 0 || row.ReconciliationCount > MaxReconciliationGenerations {
		return nil, errors.New("invalid channel delivery reconciliation lookup")
	}
	q, ok := l.q.(reconciliationReadQueries)
	if !ok {
		return nil, errors.New("channel delivery reconciliation lookup is not configured")
	}
	return validateReconciliationChain(ctx, q, row)
}

func (l *Ledger) ClaimExpiredLeases(ctx context.Context, limit int32) ([]db.ChannelDelivery, error) {
	if l == nil || l.q == nil || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid channel delivery expiry request")
	}
	return l.q.ClaimExpiredChannelDeliveryLeases(ctx, limit)
}

func (l *Ledger) ClaimAuthorizedRetries(ctx context.Context, limit int32) ([]db.ChannelDelivery, error) {
	if l == nil || l.q == nil || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid authorized channel delivery retry request")
	}
	q, ok := l.q.(authorizedRetryQueries)
	if !ok {
		return nil, errors.New("authorized channel delivery retry is not configured")
	}
	return q.ClaimAuthorizedChannelDeliveryRetries(ctx, limit)
}

func (l *Ledger) Get(ctx context.Context, id pgtype.UUID) (db.ChannelDelivery, error) {
	if l == nil || l.q == nil || !id.Valid {
		return db.ChannelDelivery{}, errors.New("invalid channel delivery lookup")
	}
	q, ok := l.q.(authorizedRetryQueries)
	if !ok {
		return db.ChannelDelivery{}, errors.New("channel delivery lookup is not configured")
	}
	return q.GetChannelDeliveryByID(ctx, id)
}

func ValidateRow(row db.ChannelDelivery) (Evidence, error) {
	var evidence Evidence
	if (row.Status != "delivered" && row.Status != "readback" && row.Status != "ambiguous") || !row.EvidenceDigest.Valid || len(row.Evidence) == 0 {
		return evidence, ErrInvalidEvidence
	}
	if err := json.Unmarshal(row.Evidence, &evidence); err != nil {
		return evidence, fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	_, digest, err := encodeEvidence(evidence)
	if err != nil || digest != row.EvidenceDigest.String ||
		evidence.DeliveryID != util.UUIDToString(row.ID) || evidence.CorrelationID != util.UUIDToString(row.CorrelationID) ||
		evidence.WorkspaceID != util.UUIDToString(row.WorkspaceID) || evidence.TaskID != util.UUIDToString(row.TaskID) ||
		evidence.ChatSessionID != util.UUIDToString(row.ChatSessionID) || evidence.ChannelType != row.ChannelType ||
		evidence.ChannelChatID != row.ChannelChatID || evidence.OperationKind != row.OperationKind ||
		evidence.PayloadDigest != row.PayloadDigest || evidence.Status != row.Status || evidence.AttemptCount != row.AttemptCount {
		return evidence, ErrInvalidEvidence
	}
	if row.Status == "ambiguous" {
		if evidence.ContractVersion != AmbiguityEvidenceContractVersion || !row.AmbiguousAt.Valid ||
			evidence.AmbiguousAt != row.AmbiguousAt.Time.UTC().Format(time.RFC3339Nano) ||
			!row.LastErrorCode.Valid || evidence.AmbiguityReason != row.LastErrorCode.String ||
			!validAmbiguityReason(evidence.AmbiguityReason) || evidence.DeliveredAt != "" ||
			evidence.ReadbackAt != "" || evidence.ReadbackMessageID != "" || row.DeliveredAt.Valid || row.ReadbackAt.Valid ||
			(row.ExternalMessageID.Valid && evidence.ExternalMessageID != row.ExternalMessageID.String) ||
			(!row.ExternalMessageID.Valid && evidence.ExternalMessageID != "") {
			return evidence, ErrInvalidEvidence
		}
		return evidence, nil
	}
	if evidence.ContractVersion != EvidenceContractVersion || !row.ExternalMessageID.Valid ||
		evidence.ExternalMessageID != row.ExternalMessageID.String || !row.DeliveredAt.Valid ||
		evidence.DeliveredAt != row.DeliveredAt.Time.UTC().Format(time.RFC3339Nano) ||
		evidence.AmbiguityReason != "" || evidence.AmbiguousAt != "" || row.AmbiguousAt.Valid {
		return evidence, ErrInvalidEvidence
	}
	if row.Status == "readback" {
		if !row.ReadbackAt.Valid || evidence.ReadbackAt != row.ReadbackAt.Time.UTC().Format(time.RFC3339Nano) || evidence.ReadbackMessageID == "" {
			return evidence, ErrInvalidEvidence
		}
	} else if evidence.ReadbackAt != "" || evidence.ReadbackMessageID != "" {
		return evidence, ErrInvalidEvidence
	}
	return evidence, nil
}

func evidenceFromRow(row db.ChannelDelivery) Evidence {
	return Evidence{
		ContractVersion: EvidenceContractVersion, DeliveryID: util.UUIDToString(row.ID), CorrelationID: util.UUIDToString(row.CorrelationID),
		WorkspaceID: util.UUIDToString(row.WorkspaceID), TaskID: util.UUIDToString(row.TaskID), ChatSessionID: util.UUIDToString(row.ChatSessionID),
		ChannelType: row.ChannelType, ChannelChatID: row.ChannelChatID, OperationKind: row.OperationKind,
		PayloadDigest: row.PayloadDigest, AttemptCount: row.AttemptCount,
	}
}

func encodeEvidence(evidence Evidence) ([]byte, string, error) {
	body, err := json.Marshal(evidence)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validOperation(operation string) bool {
	return operation == OperationChatReply || operation == OperationFailureNotice
}

func validAmbiguityReason(reason string) bool {
	switch reason {
	case AmbiguityResponseUnknown, AmbiguityPartialDelivery, AmbiguityReceiptPersistFailed, AmbiguityLeaseExpired, AmbiguityMissingProviderID:
		return true
	default:
		return false
	}
}

func normalizeErrorCode(code string) string {
	switch code {
	case "timeout", "authorization", "rate_limited", "provider_error", "delivery_state_conflict":
		return code
	default:
		return "provider_error"
	}
}
