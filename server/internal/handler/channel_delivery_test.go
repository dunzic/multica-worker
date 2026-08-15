package handler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/delivery"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestChannelDeliveryResponseIsContentFree(t *testing.T) {
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC), Valid: true}
	row := db.ChannelDelivery{
		ID: testDeliveryUUID(1), WorkspaceID: testDeliveryUUID(2), InstallationID: testDeliveryUUID(3),
		TaskID: testDeliveryUUID(4), ChatSessionID: testDeliveryUUID(5), ChannelType: "slack", ChannelChatID: "C1",
		OperationKind: "chat_reply", CorrelationID: testDeliveryUUID(6),
		PayloadDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:        "failed", AttemptCount: 2, LastErrorCode: pgtype.Text{String: "provider_error", Valid: true},
		CreatedAt: now, UpdatedAt: now,
	}
	response, err := channelDeliveryToResponse(row)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	if strings.Contains(encoded, "message_body") || strings.Contains(encoded, "request_key") || strings.Contains(encoded, "secret") {
		t.Fatalf("response leaked a forbidden field: %s", encoded)
	}
	if !strings.Contains(encoded, row.PayloadDigest) || !strings.Contains(encoded, "provider_error") {
		t.Fatalf("response omitted audit fields: %s", encoded)
	}
}

func TestChannelDeliveryResponseIncludesVerifiedAmbiguityEvidence(t *testing.T) {
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC), Valid: true}
	row := db.ChannelDelivery{
		ID: testDeliveryUUID(1), WorkspaceID: testDeliveryUUID(2), InstallationID: testDeliveryUUID(3),
		TaskID: testDeliveryUUID(4), ChatSessionID: testDeliveryUUID(5), ChannelType: "slack", ChannelChatID: "C1",
		OperationKind: "chat_reply", CorrelationID: testDeliveryUUID(6),
		PayloadDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:        "ambiguous", AttemptCount: 1, ExternalMessageID: pgtype.Text{String: "provider-1", Valid: true},
		LastErrorCode: pgtype.Text{String: delivery.AmbiguityPartialDelivery, Valid: true}, AmbiguousAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	evidence := delivery.Evidence{
		ContractVersion: delivery.AmbiguityEvidenceContractVersion,
		DeliveryID:      util.UUIDToString(row.ID), CorrelationID: util.UUIDToString(row.CorrelationID),
		WorkspaceID: util.UUIDToString(row.WorkspaceID), TaskID: util.UUIDToString(row.TaskID),
		ChatSessionID: util.UUIDToString(row.ChatSessionID), ChannelType: row.ChannelType,
		ChannelChatID: row.ChannelChatID, OperationKind: row.OperationKind, PayloadDigest: row.PayloadDigest,
		Status: row.Status, AttemptCount: row.AttemptCount, ExternalMessageID: row.ExternalMessageID.String,
		AmbiguityReason: delivery.AmbiguityPartialDelivery, AmbiguousAt: now.Time.Format(time.RFC3339Nano),
	}
	row.Evidence, _ = json.Marshal(evidence)
	digest := sha256.Sum256(row.Evidence)
	row.EvidenceDigest = pgtype.Text{String: fmt.Sprintf("sha256:%x", digest), Valid: true}

	response, err := channelDeliveryToResponse(row)
	if err != nil {
		t.Fatal(err)
	}
	if response.Evidence == nil || response.AmbiguousAt == nil || response.Evidence.AmbiguityReason != delivery.AmbiguityPartialDelivery {
		t.Fatalf("response=%+v", response)
	}
}

func TestChannelDeliveryResponseRejectsUnverifiedTerminalRow(t *testing.T) {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	row := db.ChannelDelivery{
		ID: testDeliveryUUID(1), WorkspaceID: testDeliveryUUID(2), TaskID: testDeliveryUUID(4), ChatSessionID: testDeliveryUUID(5),
		ChannelType: "slack", ChannelChatID: "C1", OperationKind: "chat_reply", CorrelationID: testDeliveryUUID(6),
		PayloadDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "delivered", AttemptCount: 1,
		ExternalMessageID: pgtype.Text{String: "provider-1", Valid: true}, CreatedAt: now, UpdatedAt: now, DeliveredAt: now,
	}
	if _, err := channelDeliveryToResponse(row); err == nil {
		t.Fatal("terminal row without evidence was accepted")
	}
}

func TestChannelDeliveryResponseIncludesSanitizedReconciliation(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC)
	row := db.ChannelDelivery{
		ID: testDeliveryUUID(1), WorkspaceID: testDeliveryUUID(2), TaskID: testDeliveryUUID(4), ChatSessionID: testDeliveryUUID(5),
		ChannelType: "slack", ChannelChatID: "C1", OperationKind: "chat_reply", CorrelationID: testDeliveryUUID(6),
		PayloadDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:        "reconciled", AttemptCount: 1, ReconciliationCount: 1,
		LastReconciledAt: pgtype.Timestamptz{Time: now, Valid: true},
		CreatedAt:        pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}
	receipt := &delivery.ReconciliationReceipt{
		Generation: 1, Outcome: delivery.ReconciliationConfirmedDelivered,
		ReasonCode:                      delivery.ReconciliationProviderDeliveryConfirmed,
		ExternalEvidenceDigest:          "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExpectedAmbiguityEvidenceDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ReconciliationDigest:            "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		RequesterKeyID:                  "private_requester_key", ApproverKeyID: "private_approver_key", CreatedAt: now,
	}
	response, err := channelDeliveryToResponse(row, receipt)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(response)
	if response.Reconciliation == nil || response.Reconciliation.Outcome != delivery.ReconciliationConfirmedDelivered {
		t.Fatalf("response=%+v", response)
	}
	if strings.Contains(string(body), "private_requester_key") || strings.Contains(string(body), "private_approver_key") {
		t.Fatalf("response leaked operator key identity: %s", body)
	}
}

func testDeliveryUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Bytes[15] = n
	id.Valid = true
	return id
}
