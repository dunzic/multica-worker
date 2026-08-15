package delivery

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

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	ReconciliationAuthorizationContractVersion = "1.0"
	ReconciliationReceiptContractVersion       = "1.0"
	ReconciliationAuthorizationTTL             = 15 * time.Minute
	MaxReconciliationGenerations               = 3

	ReconciliationConfirmedDelivered    = "confirmed_delivered"
	ReconciliationConfirmedNotDelivered = "confirmed_not_delivered"
	ReconciliationClosedNoRetry         = "closed_no_retry"

	ReconciliationProviderDeliveryConfirmed    = "provider_delivery_confirmed"
	ReconciliationProviderNonDeliveryConfirmed = "provider_non_delivery_confirmed"
	ReconciliationBusinessSuperseded           = "business_superseded"
	ReconciliationRiskAccepted                 = "risk_accepted"
)

var ErrNotReconcileable = errors.New("channel delivery ambiguity is not reconcileable")

type ReconciliationSummary struct {
	DeliveryID                    string    `json:"delivery_id"`
	WorkspaceID                   string    `json:"workspace_id"`
	ChannelType                   string    `json:"channel_type"`
	OperationKind                 string    `json:"operation_kind"`
	Status                        string    `json:"status"`
	AttemptCount                  int32     `json:"attempt_count"`
	ReconciliationCount           int16     `json:"reconciliation_count"`
	NextGeneration                int16     `json:"next_generation"`
	ExpectedAmbiguityEvidenceHash string    `json:"expected_ambiguity_evidence_digest,omitempty"`
	AmbiguityReason               string    `json:"ambiguity_reason,omitempty"`
	LatestOutcome                 string    `json:"latest_outcome,omitempty"`
	CreatedAt                     time.Time `json:"created_at"`
}

type ReconciliationAuthorization struct {
	ContractVersion                 string    `json:"contract_version"`
	AuthorizationID                 string    `json:"authorization_id"`
	DeliveryID                      string    `json:"delivery_id"`
	ExpectedGeneration              int16     `json:"expected_generation"`
	ExpectedAmbiguityEvidenceDigest string    `json:"expected_ambiguity_evidence_digest"`
	Outcome                         string    `json:"outcome"`
	ReasonCode                      string    `json:"reason_code"`
	ExternalEvidenceDigest          string    `json:"external_evidence_digest"`
	RequestedAt                     time.Time `json:"requested_at"`
	ExpiresAt                       time.Time `json:"expires_at"`
}

type ReconciliationSignature struct {
	KeyID string
	Value []byte
}

type ReconciliationExecuteInput struct {
	Authorization ReconciliationAuthorization
	Requester     ReconciliationSignature
	Approver      ReconciliationSignature
	PublicKeys    map[string]ed25519.PublicKey
	Now           time.Time
}

type ReconciliationReceipt struct {
	ContractVersion                 string    `json:"contract_version"`
	ReconciliationID                string    `json:"reconciliation_id"`
	DeliveryID                      string    `json:"delivery_id"`
	WorkspaceID                     string    `json:"workspace_id"`
	AuthorizationID                 string    `json:"authorization_id"`
	Generation                      int16     `json:"generation"`
	Outcome                         string    `json:"outcome"`
	ReasonCode                      string    `json:"reason_code"`
	ExternalEvidenceDigest          string    `json:"external_evidence_digest"`
	ExpectedAmbiguityEvidenceDigest string    `json:"expected_ambiguity_evidence_digest"`
	AmbiguityEvidence               Evidence  `json:"ambiguity_evidence"`
	RequesterKeyID                  string    `json:"requester_key_id"`
	ApproverKeyID                   string    `json:"approver_key_id"`
	AuthorizationDigest             string    `json:"authorization_digest"`
	RequesterSignatureDigest        string    `json:"requester_signature_digest"`
	ApproverSignatureDigest         string    `json:"approver_signature_digest"`
	PreviousReconciliationDigest    string    `json:"previous_reconciliation_digest,omitempty"`
	CreatedAt                       time.Time `json:"created_at"`
	ReconciliationDigest            string    `json:"reconciliation_digest"`
}

type reconciliationChainQueries interface {
	ListChannelDeliveryReconciliationsByDelivery(context.Context, pgtype.UUID) ([]db.ChannelDeliveryReconciliation, error)
}

func InspectReconciliation(ctx context.Context, pool *pgxpool.Pool, deliveryIDText string) (ReconciliationSummary, error) {
	deliveryID, err := util.ParseUUID(deliveryIDText)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	q := db.New(pool)
	row, err := q.GetChannelDeliveryByID(ctx, deliveryID)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	latest, err := validateReconciliationChain(ctx, q, row)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	summary := ReconciliationSummary{
		DeliveryID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		ChannelType: row.ChannelType, OperationKind: row.OperationKind, Status: row.Status,
		AttemptCount: row.AttemptCount, ReconciliationCount: row.ReconciliationCount,
		NextGeneration: row.ReconciliationCount + 1, CreatedAt: row.CreatedAt.Time.UTC(),
	}
	if row.Status == "ambiguous" {
		evidence, validateErr := ValidateRow(row)
		if validateErr != nil {
			return ReconciliationSummary{}, validateErr
		}
		summary.ExpectedAmbiguityEvidenceHash = row.EvidenceDigest.String
		summary.AmbiguityReason = evidence.AmbiguityReason
	}
	if latest != nil {
		summary.LatestOutcome = latest.Outcome
	}
	return summary, nil
}

func NewReconciliationAuthorization(summary ReconciliationSummary, outcome, reasonCode, externalEvidenceDigest string, now time.Time) (ReconciliationAuthorization, error) {
	if summary.Status != "ambiguous" || summary.NextGeneration < 1 || summary.NextGeneration > MaxReconciliationGenerations ||
		!validDigest(summary.ExpectedAmbiguityEvidenceHash) || !validDigest(externalEvidenceDigest) ||
		!validReconciliationDecision(outcome, reasonCode) ||
		(summary.AmbiguityReason == AmbiguityPartialDelivery && outcome == ReconciliationConfirmedNotDelivered) {
		return ReconciliationAuthorization{}, ErrNotReconcileable
	}
	return ReconciliationAuthorization{
		ContractVersion: ReconciliationAuthorizationContractVersion,
		AuthorizationID: uuid.NewString(), DeliveryID: summary.DeliveryID,
		ExpectedGeneration:              summary.NextGeneration,
		ExpectedAmbiguityEvidenceDigest: summary.ExpectedAmbiguityEvidenceHash,
		Outcome:                         outcome, ReasonCode: reasonCode, ExternalEvidenceDigest: externalEvidenceDigest,
		RequestedAt: now.UTC(), ExpiresAt: now.UTC().Add(ReconciliationAuthorizationTTL),
	}, nil
}

func CanonicalReconciliationAuthorization(auth ReconciliationAuthorization) ([]byte, error) {
	if auth.ContractVersion != ReconciliationAuthorizationContractVersion ||
		auth.ExpectedGeneration < 1 || auth.ExpectedGeneration > MaxReconciliationGenerations ||
		!validDigest(auth.ExpectedAmbiguityEvidenceDigest) || !validDigest(auth.ExternalEvidenceDigest) ||
		!validReconciliationDecision(auth.Outcome, auth.ReasonCode) {
		return nil, errors.New("invalid channel delivery reconciliation authorization")
	}
	if _, err := uuid.Parse(auth.AuthorizationID); err != nil {
		return nil, errors.New("invalid reconciliation authorization id")
	}
	if _, err := uuid.Parse(auth.DeliveryID); err != nil {
		return nil, errors.New("invalid channel delivery id")
	}
	if auth.RequestedAt.IsZero() || auth.ExpiresAt.Sub(auth.RequestedAt) != ReconciliationAuthorizationTTL {
		return nil, errors.New("invalid reconciliation authorization lifetime")
	}
	auth.RequestedAt = auth.RequestedAt.UTC()
	auth.ExpiresAt = auth.ExpiresAt.UTC()
	return json.Marshal(auth)
}

func ExecuteReconciliation(ctx context.Context, pool *pgxpool.Pool, input ReconciliationExecuteInput) (ReconciliationReceipt, error) {
	canonical, err := CanonicalReconciliationAuthorization(input.Authorization)
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.Truncate(time.Microsecond)
	if err := verifyReconciliationSignatures(canonical, input); err != nil {
		return ReconciliationReceipt{}, err
	}
	deliveryID, _ := util.ParseUUID(input.Authorization.DeliveryID)
	authorizationID, _ := util.ParseUUID(input.Authorization.AuthorizationID)
	if existing, lookupErr := db.New(pool).GetChannelDeliveryReconciliationByAuthorization(ctx, authorizationID); lookupErr == nil {
		return matchExistingReconciliation(existing, input, canonical)
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return ReconciliationReceipt{}, lookupErr
	}
	if now.Before(input.Authorization.RequestedAt) || !now.Before(input.Authorization.ExpiresAt) {
		return ReconciliationReceipt{}, errors.New("reconciliation authorization is not currently valid")
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	q := db.New(tx)
	row, err := q.GetChannelDeliveryForReconciliation(ctx, deliveryID)
	if err != nil {
		if isReconciliationConcurrencyError(err) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReconciliation(ctx, pool, authorizationID, input, canonical, err)
		}
		return ReconciliationReceipt{}, err
	}
	if row.Status != "ambiguous" || !row.EvidenceDigest.Valid ||
		row.ReconciliationCount+1 != input.Authorization.ExpectedGeneration ||
		row.ReconciliationCount >= MaxReconciliationGenerations ||
		row.EvidenceDigest.String != input.Authorization.ExpectedAmbiguityEvidenceDigest {
		_ = tx.Rollback(context.Background())
		return reconcileExistingReconciliation(ctx, pool, authorizationID, input, canonical, ErrNotReconcileable)
	}
	ambiguityEvidence, err := ValidateRow(row)
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	if ambiguityEvidence.AmbiguityReason == AmbiguityPartialDelivery && input.Authorization.Outcome == ReconciliationConfirmedNotDelivered {
		return ReconciliationReceipt{}, errors.New("partial delivery cannot be reconciled as provider non-delivery")
	}
	latest, err := validateReconciliationChain(ctx, q, row)
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	previous := ""
	if latest != nil {
		previous = latest.ReconciliationDigest
	}

	reconciliationID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	receipt := ReconciliationReceipt{
		ContractVersion:  ReconciliationReceiptContractVersion,
		ReconciliationID: util.UUIDToString(reconciliationID), DeliveryID: input.Authorization.DeliveryID,
		WorkspaceID: util.UUIDToString(row.WorkspaceID), AuthorizationID: input.Authorization.AuthorizationID,
		Generation: input.Authorization.ExpectedGeneration, Outcome: input.Authorization.Outcome,
		ReasonCode: input.Authorization.ReasonCode, ExternalEvidenceDigest: input.Authorization.ExternalEvidenceDigest,
		ExpectedAmbiguityEvidenceDigest: input.Authorization.ExpectedAmbiguityEvidenceDigest,
		AmbiguityEvidence:               ambiguityEvidence, RequesterKeyID: input.Requester.KeyID, ApproverKeyID: input.Approver.KeyID,
		AuthorizationDigest: digest(canonical), RequesterSignatureDigest: digest(input.Requester.Value),
		ApproverSignatureDigest: digest(input.Approver.Value), PreviousReconciliationDigest: previous, CreatedAt: now,
	}
	receipt.ReconciliationDigest, err = digestReconciliationReceipt(receipt)
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	ambiguityBody, _, err := encodeEvidence(ambiguityEvidence)
	if err != nil {
		return ReconciliationReceipt{}, err
	}
	_, err = q.InsertChannelDeliveryReconciliation(ctx, db.InsertChannelDeliveryReconciliationParams{
		ID: reconciliationID, DeliveryID: row.ID, WorkspaceID: row.WorkspaceID, AuthorizationID: authorizationID,
		Generation: receipt.Generation, Outcome: receipt.Outcome, ReasonCode: receipt.ReasonCode,
		ExternalEvidenceDigest:          receipt.ExternalEvidenceDigest,
		ExpectedAmbiguityEvidenceDigest: receipt.ExpectedAmbiguityEvidenceDigest, AmbiguityEvidence: ambiguityBody,
		RequesterKeyID: receipt.RequesterKeyID, ApproverKeyID: receipt.ApproverKeyID,
		AuthorizationDigest: receipt.AuthorizationDigest, RequesterSignatureDigest: receipt.RequesterSignatureDigest,
		ApproverSignatureDigest:      receipt.ApproverSignatureDigest,
		PreviousReconciliationDigest: pgtype.Text{String: previous, Valid: previous != ""},
		ReconciliationDigest:         receipt.ReconciliationDigest,
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		if isReconciliationConcurrencyError(err) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReconciliation(ctx, pool, authorizationID, input, canonical, err)
		}
		return ReconciliationReceipt{}, err
	}
	if _, err = q.ResolveChannelDeliveryAmbiguity(ctx, db.ResolveChannelDeliveryAmbiguityParams{
		Outcome: receipt.Outcome, ReconciledAt: pgtype.Timestamptz{Time: now, Valid: true}, ID: row.ID,
		ExpectedReconciliationCount:     row.ReconciliationCount,
		ExpectedAmbiguityEvidenceDigest: row.EvidenceDigest,
	}); err != nil {
		if isReconciliationConcurrencyError(err) || errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(context.Background())
			return reconcileExistingReconciliation(ctx, pool, authorizationID, input, canonical, ErrNotReconcileable)
		}
		return ReconciliationReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return reconcileExistingReconciliation(ctx, pool, authorizationID, input, canonical, err)
	}
	return receipt, nil
}

func reconcileExistingReconciliation(ctx context.Context, pool *pgxpool.Pool, authorizationID pgtype.UUID, input ReconciliationExecuteInput, canonical []byte, fallback error) (ReconciliationReceipt, error) {
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	existing, err := db.New(pool).GetChannelDeliveryReconciliationByAuthorization(reconcileCtx, authorizationID)
	if err == nil {
		return matchExistingReconciliation(existing, input, canonical)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationReceipt{}, fallback
	}
	return ReconciliationReceipt{}, err
}

func matchExistingReconciliation(row db.ChannelDeliveryReconciliation, input ReconciliationExecuteInput, canonical []byte) (ReconciliationReceipt, error) {
	receipt, err := DecodePersistedReconciliationReceipt(row)
	if err != nil || receipt.DeliveryID != input.Authorization.DeliveryID ||
		receipt.Generation != input.Authorization.ExpectedGeneration ||
		receipt.ExpectedAmbiguityEvidenceDigest != input.Authorization.ExpectedAmbiguityEvidenceDigest ||
		receipt.Outcome != input.Authorization.Outcome || receipt.ReasonCode != input.Authorization.ReasonCode ||
		receipt.ExternalEvidenceDigest != input.Authorization.ExternalEvidenceDigest ||
		receipt.AuthorizationDigest != digest(canonical) || receipt.RequesterKeyID != input.Requester.KeyID ||
		receipt.ApproverKeyID != input.Approver.KeyID ||
		receipt.RequesterSignatureDigest != digest(input.Requester.Value) ||
		receipt.ApproverSignatureDigest != digest(input.Approver.Value) {
		return ReconciliationReceipt{}, errors.New("reconciliation authorization conflicts with its existing receipt")
	}
	return receipt, nil
}

func DecodePersistedReconciliationReceipt(row db.ChannelDeliveryReconciliation) (ReconciliationReceipt, error) {
	var ambiguityEvidence Evidence
	if err := json.Unmarshal(row.AmbiguityEvidence, &ambiguityEvidence); err != nil {
		return ReconciliationReceipt{}, errors.New("persisted reconciliation ambiguity evidence is invalid")
	}
	receipt := ReconciliationReceipt{
		ContractVersion:  ReconciliationReceiptContractVersion,
		ReconciliationID: util.UUIDToString(row.ID), DeliveryID: util.UUIDToString(row.DeliveryID),
		WorkspaceID: util.UUIDToString(row.WorkspaceID), AuthorizationID: util.UUIDToString(row.AuthorizationID),
		Generation: row.Generation, Outcome: row.Outcome, ReasonCode: row.ReasonCode,
		ExternalEvidenceDigest:          row.ExternalEvidenceDigest,
		ExpectedAmbiguityEvidenceDigest: row.ExpectedAmbiguityEvidenceDigest,
		AmbiguityEvidence:               ambiguityEvidence, RequesterKeyID: row.RequesterKeyID, ApproverKeyID: row.ApproverKeyID,
		AuthorizationDigest: row.AuthorizationDigest, RequesterSignatureDigest: row.RequesterSignatureDigest,
		ApproverSignatureDigest:      row.ApproverSignatureDigest,
		PreviousReconciliationDigest: auditText(row.PreviousReconciliationDigest),
		CreatedAt:                    row.CreatedAt.Time.UTC(), ReconciliationDigest: row.ReconciliationDigest,
	}
	_, ambiguityDigest, evidenceErr := encodeEvidence(ambiguityEvidence)
	computed, digestErr := digestReconciliationReceipt(receipt)
	if evidenceErr != nil || digestErr != nil || computed != receipt.ReconciliationDigest ||
		ambiguityDigest != receipt.ExpectedAmbiguityEvidenceDigest ||
		receipt.Generation < 1 || receipt.Generation > MaxReconciliationGenerations ||
		!validReconciliationDecision(receipt.Outcome, receipt.ReasonCode) ||
		!validDigest(receipt.ExternalEvidenceDigest) || !validDigest(receipt.ExpectedAmbiguityEvidenceDigest) ||
		!validKeyID(receipt.RequesterKeyID) || !validKeyID(receipt.ApproverKeyID) ||
		receipt.RequesterKeyID == receipt.ApproverKeyID || !validDigest(receipt.AuthorizationDigest) ||
		!validDigest(receipt.RequesterSignatureDigest) || !validDigest(receipt.ApproverSignatureDigest) ||
		(receipt.Generation == 1) != (receipt.PreviousReconciliationDigest == "") ||
		(receipt.PreviousReconciliationDigest != "" && !validDigest(receipt.PreviousReconciliationDigest)) ||
		ambiguityEvidence.ContractVersion != AmbiguityEvidenceContractVersion || ambiguityEvidence.Status != "ambiguous" ||
		!validAmbiguityReason(ambiguityEvidence.AmbiguityReason) || ambiguityEvidence.AmbiguousAt == "" {
		return ReconciliationReceipt{}, errors.New("persisted channel delivery reconciliation receipt is invalid")
	}
	return receipt, nil
}

func validateReconciliationChain(ctx context.Context, q reconciliationChainQueries, delivery db.ChannelDelivery) (*ReconciliationReceipt, error) {
	rows, err := q.ListChannelDeliveryReconciliationsByDelivery(ctx, delivery.ID)
	if err != nil {
		return nil, err
	}
	return validateReconciliationRows(delivery, rows)
}

func validateReconciliationRows(delivery db.ChannelDelivery, rows []db.ChannelDeliveryReconciliation) (*ReconciliationReceipt, error) {
	if len(rows) != int(delivery.ReconciliationCount) || len(rows) > MaxReconciliationGenerations {
		return nil, errors.New("channel delivery reconciliation chain count is inconsistent")
	}
	previous := ""
	var latest ReconciliationReceipt
	for index, row := range rows {
		receipt, err := DecodePersistedReconciliationReceipt(row)
		if err != nil {
			return nil, fmt.Errorf("reconciliation generation %d: %w", index+1, err)
		}
		if receipt.Generation != int16(index+1) || receipt.PreviousReconciliationDigest != previous ||
			receipt.DeliveryID != util.UUIDToString(delivery.ID) || receipt.WorkspaceID != util.UUIDToString(delivery.WorkspaceID) ||
			receipt.AmbiguityEvidence.DeliveryID != util.UUIDToString(delivery.ID) ||
			receipt.AmbiguityEvidence.WorkspaceID != util.UUIDToString(delivery.WorkspaceID) ||
			receipt.AmbiguityEvidence.TaskID != util.UUIDToString(delivery.TaskID) ||
			receipt.AmbiguityEvidence.ChatSessionID != util.UUIDToString(delivery.ChatSessionID) ||
			receipt.AmbiguityEvidence.ChannelType != delivery.ChannelType ||
			receipt.AmbiguityEvidence.ChannelChatID != delivery.ChannelChatID ||
			receipt.AmbiguityEvidence.OperationKind != delivery.OperationKind ||
			receipt.AmbiguityEvidence.PayloadDigest != delivery.PayloadDigest ||
			receipt.AmbiguityEvidence.AttemptCount > delivery.AttemptCount {
			return nil, errors.New("channel delivery reconciliation chain identity is inconsistent")
		}
		previous = receipt.ReconciliationDigest
		latest = receipt
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &latest, nil
}

func verifyReconciliationSignatures(message []byte, input ReconciliationExecuteInput) error {
	if !validKeyID(input.Requester.KeyID) || !validKeyID(input.Approver.KeyID) || input.Requester.KeyID == input.Approver.KeyID {
		return errors.New("two distinct reconciliation signer key ids are required")
	}
	requester, requesterOK := input.PublicKeys[input.Requester.KeyID]
	approver, approverOK := input.PublicKeys[input.Approver.KeyID]
	if !requesterOK || !approverOK || len(requester) != ed25519.PublicKeySize || len(approver) != ed25519.PublicKeySize || bytes.Equal(requester, approver) {
		return errors.New("reconciliation signer is not in the configured keyring")
	}
	if !ed25519.Verify(requester, message, input.Requester.Value) || !ed25519.Verify(approver, message, input.Approver.Value) {
		return errors.New("reconciliation authorization signature is invalid")
	}
	return nil
}

func DecodeReconciliationPublicKeyring(raw []byte) (map[string]ed25519.PublicKey, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("reconciliation keyring must be one JSON object")
	}
	encoded := map[string]string{}
	seenIDs := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		id, ok := keyToken.(string)
		if err != nil || !ok || seenIDs[id] {
			return nil, errors.New("reconciliation keyring contains an invalid or duplicate key id")
		}
		seenIDs[id] = true
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("reconciliation keyring values must be strings")
		}
		encoded[id] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("reconciliation keyring object is incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || len(encoded) < 2 || len(encoded) > 32 {
		return nil, errors.New("reconciliation keyring must contain 2 to 32 keys")
	}
	keys := make(map[string]ed25519.PublicKey, len(encoded))
	seenKeys := make(map[string]bool, len(encoded))
	for id, value := range encoded {
		if !validKeyID(id) {
			return nil, errors.New("invalid reconciliation key id")
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid reconciliation public key %q", id)
		}
		fingerprint := string(decoded)
		if seenKeys[fingerprint] {
			return nil, errors.New("reconciliation keyring contains one public key under multiple ids")
		}
		seenKeys[fingerprint] = true
		keys[id] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func digestReconciliationReceipt(receipt ReconciliationReceipt) (string, error) {
	receipt.ReconciliationDigest = ""
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

func validReconciliationDecision(outcome, reasonCode string) bool {
	switch outcome {
	case ReconciliationConfirmedDelivered:
		return reasonCode == ReconciliationProviderDeliveryConfirmed
	case ReconciliationConfirmedNotDelivered:
		return reasonCode == ReconciliationProviderNonDeliveryConfirmed
	case ReconciliationClosedNoRetry:
		return reasonCode == ReconciliationBusinessSuperseded || reasonCode == ReconciliationRiskAccepted
	default:
		return false
	}
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

func auditText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func isReconciliationConcurrencyError(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && (databaseError.Code == "40001" || databaseError.Code == "23505")
}
