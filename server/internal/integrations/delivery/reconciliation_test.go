package delivery

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestReconciliationAuthorizationRequiresCompatibleDecision(t *testing.T) {
	summary := ReconciliationSummary{
		DeliveryID: "00000000-0000-4000-8000-000000000001", Status: "ambiguous", NextGeneration: 1,
		ExpectedAmbiguityEvidenceHash: "sha256:" + strings.Repeat("a", 64),
	}
	now := time.Date(2026, 8, 15, 2, 3, 4, 0, time.UTC)
	auth, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedNotDelivered,
		ReconciliationProviderNonDeliveryConfirmed, "sha256:"+strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalReconciliationAuthorization(auth)
	if err != nil || len(canonical) == 0 {
		t.Fatalf("canonical authorization=%q err=%v", canonical, err)
	}
	if _, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedDelivered,
		ReconciliationProviderNonDeliveryConfirmed, "sha256:"+strings.Repeat("b", 64), now); err == nil {
		t.Fatal("incompatible outcome and reason were accepted")
	}
	summary.NextGeneration = 4
	if _, err := NewReconciliationAuthorization(summary, ReconciliationClosedNoRetry,
		ReconciliationRiskAccepted, "sha256:"+strings.Repeat("b", 64), now); err == nil {
		t.Fatal("fourth reconciliation generation was accepted")
	}
	summary.NextGeneration = 1
	summary.AmbiguityReason = AmbiguityPartialDelivery
	if _, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedNotDelivered,
		ReconciliationProviderNonDeliveryConfirmed, "sha256:"+strings.Repeat("b", 64), now); err == nil {
		t.Fatal("known partial delivery was authorized as complete provider non-delivery")
	}
}

func TestReconciliationSignaturesRequireTwoDistinctKeys(t *testing.T) {
	requesterPublic, requesterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approverPublic, approverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"requester_1": base64.StdEncoding.EncodeToString(requesterPublic),
		"approver_1":  base64.StdEncoding.EncodeToString(approverPublic),
	})
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := DecodeReconciliationPublicKeyring(body)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("canonical authorization")
	input := ReconciliationExecuteInput{
		Requester:  ReconciliationSignature{KeyID: "requester_1", Value: ed25519.Sign(requesterPrivate, message)},
		Approver:   ReconciliationSignature{KeyID: "approver_1", Value: ed25519.Sign(approverPrivate, message)},
		PublicKeys: keyring,
	}
	if err := verifyReconciliationSignatures(message, input); err != nil {
		t.Fatal(err)
	}
	input.Approver.KeyID = input.Requester.KeyID
	if err := verifyReconciliationSignatures(message, input); err == nil {
		t.Fatal("one signer under two roles was accepted")
	}
	aliasBody, _ := json.Marshal(map[string]string{
		"requester_1": base64.StdEncoding.EncodeToString(requesterPublic),
		"requester_2": base64.StdEncoding.EncodeToString(requesterPublic),
	})
	if _, err := DecodeReconciliationPublicKeyring(aliasBody); err == nil {
		t.Fatal("one public key under two ids was accepted")
	}
}

func TestPersistedReconciliationReceiptIsTamperEvident(t *testing.T) {
	now := time.Date(2026, 8, 15, 3, 4, 5, 123456000, time.UTC)
	deliveryID := pgUUIDForReconciliation("00000000-0000-4000-8000-000000000001")
	workspaceID := pgUUIDForReconciliation("00000000-0000-4000-8000-000000000002")
	reconciliationID := pgUUIDForReconciliation("00000000-0000-4000-8000-000000000003")
	authorizationID := pgUUIDForReconciliation("00000000-0000-4000-8000-000000000004")
	evidence := Evidence{
		ContractVersion: AmbiguityEvidenceContractVersion, DeliveryID: uuidText(deliveryID),
		CorrelationID: "00000000-0000-4000-8000-000000000005", WorkspaceID: uuidText(workspaceID),
		TaskID: "00000000-0000-4000-8000-000000000006", ChatSessionID: "00000000-0000-4000-8000-000000000007",
		ChannelType: "slack", ChannelChatID: "C1", OperationKind: OperationChatReply,
		PayloadDigest: "sha256:" + strings.Repeat("a", 64), Status: "ambiguous", AttemptCount: 1,
		AmbiguityReason: AmbiguityResponseUnknown, AmbiguousAt: now.Format(time.RFC3339Nano),
	}
	evidenceBody, evidenceDigest, err := encodeEvidence(evidence)
	if err != nil {
		t.Fatal(err)
	}
	receipt := ReconciliationReceipt{
		ContractVersion: ReconciliationReceiptContractVersion, ReconciliationID: uuidText(reconciliationID),
		DeliveryID: uuidText(deliveryID), WorkspaceID: uuidText(workspaceID), AuthorizationID: uuidText(authorizationID),
		Generation: 1, Outcome: ReconciliationConfirmedDelivered, ReasonCode: ReconciliationProviderDeliveryConfirmed,
		ExternalEvidenceDigest: "sha256:" + strings.Repeat("b", 64), ExpectedAmbiguityEvidenceDigest: evidenceDigest,
		AmbiguityEvidence: evidence, RequesterKeyID: "requester_1", ApproverKeyID: "approver_1",
		AuthorizationDigest:      "sha256:" + strings.Repeat("c", 64),
		RequesterSignatureDigest: "sha256:" + strings.Repeat("d", 64),
		ApproverSignatureDigest:  "sha256:" + strings.Repeat("e", 64), CreatedAt: now,
	}
	receipt.ReconciliationDigest, err = digestReconciliationReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	row := db.ChannelDeliveryReconciliation{
		ID: reconciliationID, DeliveryID: deliveryID, WorkspaceID: workspaceID, AuthorizationID: authorizationID,
		Generation: receipt.Generation, Outcome: receipt.Outcome, ReasonCode: receipt.ReasonCode,
		ExternalEvidenceDigest:          receipt.ExternalEvidenceDigest,
		ExpectedAmbiguityEvidenceDigest: receipt.ExpectedAmbiguityEvidenceDigest, AmbiguityEvidence: evidenceBody,
		RequesterKeyID: receipt.RequesterKeyID, ApproverKeyID: receipt.ApproverKeyID,
		AuthorizationDigest: receipt.AuthorizationDigest, RequesterSignatureDigest: receipt.RequesterSignatureDigest,
		ApproverSignatureDigest: receipt.ApproverSignatureDigest, ReconciliationDigest: receipt.ReconciliationDigest,
		CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	if _, err := DecodePersistedReconciliationReceipt(row); err != nil {
		t.Fatal(err)
	}
	row.Outcome = ReconciliationClosedNoRetry
	if _, err := DecodePersistedReconciliationReceipt(row); err == nil {
		t.Fatal("tampered reconciliation receipt was accepted")
	}
}

func pgUUIDForReconciliation(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}

func uuidText(value pgtype.UUID) string { return uuid.UUID(value.Bytes).String() }
