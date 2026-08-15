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
	row          db.ChannelDelivery
	has          bool
	completeErr  error
	ambiguousErr error
	ambiguousCtx error
}

type deliveryMetricsRecorder struct {
	transitions []string
	reconciles  []string
}

func (m *deliveryMetricsRecorder) RecordChannelDeliveryTransition(connector, operation, status, errorCode string) {
	m.transitions = append(m.transitions, connector+":"+operation+":"+status+":"+errorCode)
}

func (m *deliveryMetricsRecorder) RecordChannelDeliveryReconcile(outcome string) {
	m.reconciles = append(m.reconciles, outcome)
}

func testUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Bytes[15] = n
	id.Valid = true
	return id
}

func (m *memoryQueries) ClaimChannelDelivery(_ context.Context, p db.ClaimChannelDeliveryParams) (db.ChannelDelivery, error) {
	if m.has {
		if m.row.PayloadDigest != p.PayloadDigest || m.row.Status != "failed" {
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
	if m.completeErr != nil {
		return db.ChannelDelivery{}, m.completeErr
	}
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

func (m *memoryQueries) MarkChannelDeliveryAmbiguous(ctx context.Context, p db.MarkChannelDeliveryAmbiguousParams) (db.ChannelDelivery, error) {
	m.ambiguousCtx = ctx.Err()
	if m.ambiguousErr != nil {
		return db.ChannelDelivery{}, m.ambiguousErr
	}
	if !m.has || m.row.ID != p.ID || m.row.LeaseToken != p.LeaseToken || m.row.Status != "pending" {
		return db.ChannelDelivery{}, pgx.ErrNoRows
	}
	m.row.Status = "ambiguous"
	m.row.ExternalMessageID = p.ExternalMessageID
	m.row.Evidence = append([]byte(nil), p.Evidence...)
	m.row.EvidenceDigest = p.EvidenceDigest
	m.row.LastErrorCode = p.LastErrorCode
	m.row.AmbiguousAt = p.AmbiguousAt
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

func (m *memoryQueries) ClaimExpiredChannelDeliveryLeases(context.Context, int32) ([]db.ChannelDelivery, error) {
	if !m.has || m.row.Status != "pending" || !m.row.LeaseExpiresAt.Valid || !m.row.LeaseExpiresAt.Time.Before(time.Now()) {
		return []db.ChannelDelivery{}, nil
	}
	m.row.LeaseToken = testUUID(99)
	m.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(30 * time.Second), Valid: true}
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

func TestDeliveryMetricsObserveOnlyCommittedTransitions(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	metrics := &deliveryMetricsRecorder{}
	ledger.SetMetrics(metrics)

	if _, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{}, DefiniteFailure("provider_error", errors.New("provider token must never become a label"))
	}); err == nil {
		t.Fatal("provider failure was hidden")
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{MessageID: "provider-1"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkReadback(context.Background(), testUUID(2), "provider-1", "reply-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"slack:chat_reply:failed:provider_error",
		"slack:chat_reply:delivered:none",
		"slack:chat_reply:readback:none",
	}
	if len(metrics.transitions) != len(want) {
		t.Fatalf("transitions=%v, want %v", metrics.transitions, want)
	}
	for index := range want {
		if metrics.transitions[index] != want[index] {
			t.Fatalf("transitions=%v, want %v", metrics.transitions, want)
		}
	}
}

func TestReconcilerRecordsExpiredClaimsWithoutResending(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	claim, err := ledger.Claim(context.Background(), baseClaimInput())
	if err != nil {
		t.Fatal(err)
	}
	q.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	metrics := &deliveryMetricsRecorder{}
	ledger.SetMetrics(metrics)
	reconciler := &Reconciler{Ledger: ledger, Metrics: metrics}
	reconciler.RunOnce(context.Background())

	if claim.ShouldSend != true || q.row.Status != "ambiguous" {
		t.Fatalf("claim=%+v row=%+v", claim, q.row)
	}
	if _, err := ValidateRow(q.row); err != nil {
		t.Fatalf("expired lease evidence invalid: %v", err)
	}
	if len(metrics.reconciles) != 1 || metrics.reconciles[0] != "completed" || len(metrics.transitions) != 1 || metrics.transitions[0] != "slack:chat_reply:ambiguous:lease_expired" {
		t.Fatalf("reconcile metrics=%+v", metrics)
	}
}

func TestReconcilerWriteFailureLeavesLeaseNonRetryable(t *testing.T) {
	q := &memoryQueries{ambiguousErr: errors.New("database unavailable")}
	ledger := NewLedger(q)
	if _, err := ledger.Claim(context.Background(), baseClaimInput()); err != nil {
		t.Fatal(err)
	}
	q.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	metrics := &deliveryMetricsRecorder{}
	(&Reconciler{Ledger: ledger, Metrics: metrics}).RunOnce(context.Background())
	if q.row.Status != "pending" || !q.row.LeaseToken.Valid || len(metrics.reconciles) != 1 || metrics.reconciles[0] != "write_failed" {
		t.Fatalf("row=%+v metrics=%+v", q.row, metrics)
	}
	replay, err := ledger.Claim(context.Background(), baseClaimInput())
	if err != nil || replay.ShouldSend {
		t.Fatalf("pending replay=%+v err=%v", replay, err)
	}
}

func TestFailedDeliveryCanBeRetriedWithoutChangingCorrelation(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	first := true
	send := func(context.Context) (channel.SendResult, error) {
		if first {
			first = false
			return channel.SendResult{}, DefiniteFailure("provider_error", errors.New("provider rejected request"))
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

func TestUnknownProviderOutcomeFreezesAndNeverResends(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	sends := 0
	result, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		sends++
		return channel.SendResult{}, context.DeadlineExceeded
	})
	if result.MessageID != "" || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	evidence, validateErr := ValidateRow(q.row)
	if validateErr != nil || q.row.Status != "ambiguous" || evidence.AmbiguityReason != AmbiguityResponseUnknown {
		t.Fatalf("row=%+v evidence=%+v err=%v", q.row, evidence, validateErr)
	}
	if _, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		sends++
		return channel.SendResult{MessageID: "must-not-send"}, nil
	}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("duplicate ambiguous send error=%v", err)
	}
	if sends != 1 {
		t.Fatalf("provider sends=%d, want 1", sends)
	}
}

func TestCallerCancellationDoesNotCancelAmbiguityEvidenceWrite(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Send(ctx, ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		cancel()
		return channel.SendResult{}, context.Canceled
	})
	if !errors.Is(err, ErrAmbiguous) || q.ambiguousCtx != nil || q.row.Status != "ambiguous" {
		t.Fatalf("err=%v record_ctx_err=%v row=%+v", err, q.ambiguousCtx, q.row)
	}
}

func TestPartialDeliveryFreezesKnownProviderMessage(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	result, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{MessageID: "provider-chunk-1"}, errors.New("second chunk response lost")
	})
	if result.MessageID != "provider-chunk-1" || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	evidence, validateErr := ValidateRow(q.row)
	if validateErr != nil || evidence.AmbiguityReason != AmbiguityPartialDelivery || evidence.ExternalMessageID != "provider-chunk-1" {
		t.Fatalf("evidence=%+v err=%v", evidence, validateErr)
	}
}

func TestReceiptPersistenceFailureFreezesAcceptedMessage(t *testing.T) {
	q := &memoryQueries{completeErr: errors.New("database commit result unknown")}
	ledger := NewLedger(q)
	result, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{MessageID: "provider-accepted-1"}, nil
	})
	if result.MessageID != "provider-accepted-1" || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	evidence, validateErr := ValidateRow(q.row)
	if validateErr != nil || evidence.AmbiguityReason != AmbiguityReceiptPersistFailed || evidence.ExternalMessageID != "provider-accepted-1" {
		t.Fatalf("evidence=%+v err=%v", evidence, validateErr)
	}
}

func TestMissingProviderMessageIDFreezesInsteadOfRetrying(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	if _, err := Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{}, nil
	}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("empty provider id error=%v", err)
	}
	evidence, err := ValidateRow(q.row)
	if err != nil || evidence.AmbiguityReason != AmbiguityMissingProviderID {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestAmbiguityEvidenceRejectsRowMutation(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	_, _ = Send(context.Background(), ledger, baseClaimInput(), func(context.Context) (channel.SendResult, error) {
		return channel.SendResult{}, errors.New("response lost")
	})
	q.row.LastErrorCode = pgtype.Text{String: AmbiguityLeaseExpired, Valid: true}
	if _, err := ValidateRow(q.row); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("mutated ambiguity reason error=%v", err)
	}
}

func TestExpiredPendingClaimCannotBeReclaimedByBusinessEvent(t *testing.T) {
	q := &memoryQueries{}
	ledger := NewLedger(q)
	if _, err := ledger.Claim(context.Background(), baseClaimInput()); err != nil {
		t.Fatal(err)
	}
	q.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	second, err := ledger.Claim(context.Background(), baseClaimInput())
	if err != nil || second.ShouldSend || second.Row.Status != "pending" || second.Row.AttemptCount != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
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
