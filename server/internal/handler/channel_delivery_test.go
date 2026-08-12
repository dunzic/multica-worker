package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

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

func testDeliveryUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Bytes[15] = n
	id.Valid = true
	return id
}
