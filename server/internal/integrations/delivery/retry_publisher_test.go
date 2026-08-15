package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type retryPublisherQueries struct {
	task    db.AgentTaskQueue
	message db.ChatMessage
}

type authorizedRetryMemoryQueries struct {
	*memoryQueries
	task    db.AgentTaskQueue
	message db.ChatMessage
}

func (q *authorizedRetryMemoryQueries) ClaimChannelDelivery(ctx context.Context, p db.ClaimChannelDeliveryParams) (db.ChannelDelivery, error) {
	if q.has && q.row.Status == "retry_authorized" && q.row.PayloadDigest == p.PayloadDigest {
		q.row.Status = "pending"
		q.row.AttemptCount++
		q.row.LeaseToken = testUUID(71)
		q.row.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(30 * time.Second), Valid: true}
		q.row.RetryPublishToken = pgtype.UUID{}
		q.row.RetryPublishExpiresAt = pgtype.Timestamptz{}
		return q.row, nil
	}
	return q.memoryQueries.ClaimChannelDelivery(ctx, p)
}

func (q *authorizedRetryMemoryQueries) ClaimAuthorizedChannelDeliveryRetries(context.Context, int32) ([]db.ChannelDelivery, error) {
	if !q.has || q.row.Status != "retry_authorized" {
		return []db.ChannelDelivery{}, nil
	}
	q.row.RetryPublishToken = testUUID(72)
	q.row.RetryPublishExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(30 * time.Second), Valid: true}
	return []db.ChannelDelivery{q.row}, nil
}

func (q *authorizedRetryMemoryQueries) GetChannelDeliveryByID(context.Context, pgtype.UUID) (db.ChannelDelivery, error) {
	return q.row, nil
}

func (q *authorizedRetryMemoryQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}

func (q *authorizedRetryMemoryQueries) GetChatMessageByTaskAssistant(context.Context, pgtype.UUID) (db.ChatMessage, error) {
	return q.message, nil
}

func (q *retryPublisherQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}

func (q *retryPublisherQueries) GetChatMessageByTaskAssistant(context.Context, pgtype.UUID) (db.ChatMessage, error) {
	return q.message, nil
}

func TestRetryEventPublisherReconstructsOnlyDigestBoundOutput(t *testing.T) {
	now := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	row := db.ChannelDelivery{
		ID: testUUID(31), WorkspaceID: testUUID(1), InstallationID: testUUID(2), TaskID: testUUID(3),
		ChatSessionID: testUUID(4), ChannelType: "slack", OperationKind: OperationChatReply,
		PayloadDigest: PayloadDigest("approved answer"), Status: "retry_authorized",
		RetryPublishToken: testUUID(32), RetryPublishExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	}
	queries := &retryPublisherQueries{
		task:    db.AgentTaskQueue{ID: row.TaskID, ChatSessionID: row.ChatSessionID, Status: "completed"},
		message: db.ChatMessage{ChatSessionID: row.ChatSessionID, TaskID: row.TaskID, Role: "assistant", Content: "approved answer"},
	}
	bus := events.New()
	var got events.Event
	bus.Subscribe(protocol.EventChatDone, func(event events.Event) { got = event })
	publisher := NewRetryEventPublisher(queries, bus)
	publisher.now = func() time.Time { return now }
	if err := publisher.PublishAuthorizedRetry(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	if got.TaskID == "" || got.ChatSessionID == "" || got.Type != protocol.EventChatDone {
		t.Fatalf("published event=%+v", got)
	}

	row.PayloadDigest = PayloadDigest("different content")
	got = events.Event{}
	if err := publisher.PublishAuthorizedRetry(context.Background(), row); err == nil {
		t.Fatal("payload-digest mismatch was published")
	}
	if got.Type != "" {
		t.Fatalf("unexpected event after mismatch=%+v", got)
	}
}

func TestRetryEventPublisherRebuildsFailureNoticePrefix(t *testing.T) {
	now := time.Date(2026, 8, 15, 4, 5, 6, 0, time.UTC)
	content := "safe redacted failure"
	row := db.ChannelDelivery{
		ID: testUUID(41), WorkspaceID: testUUID(1), InstallationID: testUUID(2), TaskID: testUUID(3),
		ChatSessionID: testUUID(4), ChannelType: "dingtalk", OperationKind: OperationFailureNotice,
		PayloadDigest: PayloadDigest("⚠️ " + content), Status: "retry_authorized",
		RetryPublishToken: testUUID(42), RetryPublishExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	}
	queries := &retryPublisherQueries{
		task:    db.AgentTaskQueue{ID: row.TaskID, ChatSessionID: row.ChatSessionID, Status: "failed"},
		message: db.ChatMessage{ChatSessionID: row.ChatSessionID, TaskID: row.TaskID, Role: "assistant", Content: content},
	}
	bus := events.New()
	var got events.Event
	bus.Subscribe(protocol.EventTaskFailed, func(event events.Event) { got = event })
	publisher := NewRetryEventPublisher(queries, bus)
	publisher.now = func() time.Time { return now }
	if err := publisher.PublishAuthorizedRetry(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	payload, _ := got.Payload.(map[string]any)
	if got.Type != protocol.EventTaskFailed || payload["error"] != content {
		t.Fatalf("published failure event=%+v payload=%+v", got, payload)
	}
}

func TestReconcilerPublishesAndConsumesAuthorizedRetryOnce(t *testing.T) {
	input := baseClaimInput()
	q := &authorizedRetryMemoryQueries{memoryQueries: &memoryQueries{has: true}}
	q.row = db.ChannelDelivery{
		ID: testUUID(61), WorkspaceID: input.WorkspaceID, InstallationID: input.InstallationID,
		TaskID: input.TaskID, ChatSessionID: input.ChatSessionID, ChannelType: string(input.ChannelType),
		ChannelChatID: input.ChannelChatID, OperationKind: input.OperationKind, CorrelationID: testUUID(62),
		PayloadDigest: PayloadDigest(input.Payload), Status: "retry_authorized", AttemptCount: 1,
		ReconciliationCount: 1, LastReconciledAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	q.task = db.AgentTaskQueue{ID: input.TaskID, ChatSessionID: input.ChatSessionID, Status: "completed"}
	q.message = db.ChatMessage{ChatSessionID: input.ChatSessionID, TaskID: input.TaskID, Role: "assistant", Content: input.Payload}
	ledger := NewLedger(q)
	bus := events.New()
	providerSends := 0
	bus.Subscribe(protocol.EventChatDone, func(events.Event) {
		claim, err := ledger.Claim(context.Background(), input)
		if err != nil || !claim.ShouldSend {
			t.Errorf("authorized event claim=%+v err=%v", claim, err)
			return
		}
		providerSends++
		if _, err := ledger.MarkDelivered(context.Background(), claim, "provider-retry-1"); err != nil {
			t.Error(err)
		}
	})
	metrics := &deliveryMetricsRecorder{}
	reconciler := &Reconciler{Ledger: ledger, RetryPublisher: NewRetryEventPublisher(q, bus), Metrics: metrics}
	reconciler.RunOnce(context.Background())
	if providerSends != 1 || q.row.Status != "delivered" {
		t.Fatalf("provider sends=%d row=%+v", providerSends, q.row)
	}
	reconciler.RunOnce(context.Background())
	if providerSends != 1 {
		t.Fatalf("authorization was consumed more than once: sends=%d", providerSends)
	}
	if len(metrics.reconciles) < 3 || metrics.reconciles[1] != "retry_published" {
		t.Fatalf("reconcile metrics=%v", metrics.reconciles)
	}
}
