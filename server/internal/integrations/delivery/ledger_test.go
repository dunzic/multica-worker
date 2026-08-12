package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type memoryQueries struct {
	row db.ChannelDelivery
	has bool
}

func testUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Bytes[15] = n
	id.Valid = true
	return id
}

func (m *memoryQueries) ClaimChannelDelivery(_ context.Context, p db.ClaimChannelDeliveryParams) (db.ChannelDelivery, error) {
	if m.has {
		if m.row.PayloadDigest != p.PayloadDigest || (m.row.Status != "failed" && !(m.row.Status == "pending" && m.row.LeaseExpiresAt.Time.Before(time.Now()))) {
			return db.ChannelDelivery{}, pgx.ErrNoRows
		}
		m.row.Status = "pending"
		m.row.AttemptCount++
		m.row.LeaseToken = testUUID(byte(20 + m.row.AttemptCount))
		m.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(30 * time.Second), Valid: true}
		m.row.LastErrorCode = pgtype.Text{}
		m.row.FailedAt = pgtype.Timestamptz{}
		return m.row, nil
	}
	m.has = true
	m.row = db.ChannelDelivery{
		ID: testUUID(10), WorkspaceID: p.WorkspaceID, InstallationID: p.InstallationID,
		TaskID: p.TaskID, ChatSessionID: p.ChatSessionID, ChannelType: p.ChannelType,
		ChannelChatID: p.ChannelChatID, OperationKind: p.OperationKind, CorrelationID: testUUID(11),
		PayloadDigest: p.PayloadDigest, Status: "pending", AttemptCount: 1, LeaseToken: testUUID(12),
		LeaseExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(30 * time.Second), Valid: true},
	}
	return m.row, nil
}

func (m *memoryQueries) GetChannelDeliveryByIdentity(_ context.Context, p db.GetChannelDeliveryByIdentityParams) (db.ChannelDelivery, error) {
	if !m.has || m.row.InstallationID != p.InstallationID || m.row.TaskID != p.TaskID || m.row.OperationKind != p.OperationKind {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	return m.row, nil
}

func (m *memoryQueries) CompleteChannelDelivery(_ context.Context, p db.CompleteChannelDeliveryParams) (db.ChannelDelivery, error) {
	if !m.has || m.row.ID != p.ID || m.row.LeaseToken != p.LeaseToken || m.row.Status != "pending" {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	m.row.Status = "delivered"
	m.row.ExternalMessageID = p.ExternalMessageID
	m.row.Evidence = append([]byte(nil), p.Evidence...)
	m.row.EvidenceDigest = p.EvidenceDigest
	m.row.DeliveredAt = p.DeliveredAt
	m.row.LeaseToken = pgtype.UUID{}
	m.row.LeaseExpiresAt = pgtype.Timestamptz{}
	return m.row, nil
}

func (m *memoryQueries) FailChannelDelivery(_ context.Context, p db.FailChannelDeliveryParams) (db.ChannelDelivery, error) {
	if !m.has || m.row.ID != p.ID || m.row.LeaseToken != p.LeaseToken || m.row.Status != "pending" {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	m.row.Status = "failed"
	m.row.LastErrorCode = p.LastErrorCode
	m.row.FailedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	m.row.LeaseToken = pgtype.UUID{}
	m.row.LeaseExpiresAt = pgtype.Timestamptz{}
	return m.row, nil
}

func (m *memoryQueries) GetChannelDeliveryByExternalMessage(_ context.Context, p db.GetChannelDeliveryByExternalMessageParams) (db.ChannelDelivery, error) {
	if !m.has || m.row.InstallationID != p.InstallationID || m.row.ExternalMessageID != p.ExternalMessageID {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	return m.row, nil
}

func (m *memoryQueries) MarkChannelDeliveryReadback(_ context.Context, p db.MarkChannelDeliveryReadbackParams) (db.ChannelDelivery, error) {
	if !m.has || m.row.ID != p.ID || m.row.Status != "delivered" || m.row.ExternalMessageID != p.ExternalMessageID {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	m.row.Status = "readback"
	m.row.Evidence = append([]byte(nil), p.Evidence...)
	m.row.EvidenceDigest = p.EvidenceDigest
	m.row.ReadbackAt = p.ReadbackAt
	return m.row, nil
}

func (m *memoryQueries) ListChannelDeliveriesByWorkspace(_ context.Context, p db.ListChannelDeliveriesByWorkspaceParams) ([]db.ChannelDelivery, error) {
	if !m.has || m.row.WorkspaceID != p.WorkspaceID {
		return []db.ChannelDelivery{}, nil
	}
	return []db.ChannelDelivery{m.row}, nil
}

func (m *memoryQueries) ExpireChannelDeliveryLeases(context.Context, int32) ([]db.ChannelDelivery, error) {
	if !m.has || m.row.Status != "pending" || !m.row.LeaseExpiresAt.Valid || !m.row.LeaseExpiresAt.Time.Before(time.Now()) {
		return []db.ChannelDelivery{}, nil
	}
	m.row.Status = "failed"
	m.row.LastErrorCode = pgtype.Text{String: "timeout", Valid: true}
	m.row.FailedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	m.row.LeaseToken = pgtype.UUID{}
	m.row.LeaseExpiresAt = pgtype.Timestamptz{}
	return []db.ChannelDelivery{m.row}, nil
}

func baseClaimInput() ClaimInput {
	return ClaimInput{
		WorkspaceID: testUUID(1), InstallationID: testUUID(2), TaskID: testUUID(3), ChatSessionID: testUUID(4),
		ChannelType: channel.Type("slack"), ChannelChatID: "C123", OperationKind: OperationChatReply, Payload: "answer",
	}
}

func TestSendRecordsDeliveredAndDeduplicates(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	now := time.Date(2026, 8, 13, 8, 0, 0, 123, time.UTC)
	ledger.now = func() time.Time { return now }
	sends := 0
	send := func(context.Context) (channel.SendResult, error) {
		sends++
		return channel.SendResult{MessageID: "provider-1"}, nil
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), send); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), send); err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if sends != 1 {
		t.Fatalf("provider sends = %d, want 1", sends)
	}
	if _, err := ValidateRow(q.row); err != nil {
		t.Fatalf("stored evidence invalid: %v", err)
	}
}

func TestFailedDeliveryCanBeRetriedWithoutChangingCorrelation(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	first := true
	send := func(context.Context) (channel.SendResult, error) {
		if first {
			first = false
			return channel.SendResult{}, errors.New("provider unavailable")
		}
		return channel.SendResult{MessageID: "provider-2"}, nil
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), send); err == nil {
		t.Fatal("provider failure was hidden")
	}
	correlation := q.row.CorrelationID
	if q.row.Status != "failed" || q.row.LastErrorCode.String != "provider_error" {
		t.Fatalf("failed row = status %q code %q", q.row.Status, q.row.LastErrorCode.String)
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), send); err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if q.row.AttemptCount != 2 || q.row.CorrelationID != correlation || q.row.Status != "delivered" {
		t.Fatalf("retry row = attempt %d correlation %v status %q", q.row.AttemptCount, q.row.CorrelationID, q.row.Status)
	}
}

func TestReadbackIsMonotonicAndTamperEvident(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	if _, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{MessageID: "provider-3"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkReadback(context.Background(), testUUID(2), "provider-3", "human-reply-1"); err != nil {
		t.Fatalf("mark readback: %v", err)
	}
	if _, err := ledger.MarkReadback(context.Background(), testUUID(2), "provider-3", "duplicate-callback"); err != nil {
		t.Fatalf("duplicate readback: %v", err)
	}
	evidence, err := ValidateRow(q.row)
	if err != nil || evidence.ReadbackMessageID != "human-reply-1" {
		t.Fatalf("readback evidence = %+v, %v", evidence, err)
	}
	q.row.Evidence[5] ^= 1
	if _, err := ValidateRow(q.row); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("tampered evidence error = %v", err)
	}
}

func TestPayloadConflictFailsClosed(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	input := baseClaimInput()
	if _, err := Send(context.Background(), ledger, input, func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{MessageID: "provider-4"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	input.Payload = "mutated answer"
	if _, err := ledger.Claim(context.Background(), input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("payload conflict error = %v", err)
	}
}

func TestReadbackBeforeDeliveryDoesNotCreateEvidence(t *testing.T) {
	ledger := NewLedger(&memoryQueries{})
	if _, err := ledger.MarkReadback(context.Background(), testUUID(2), "not-delivered", "human-1"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("out-of-order readback error = %v", err)
	}
}
