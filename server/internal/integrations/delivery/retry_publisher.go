package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type retryEventQueries interface {
	GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error)
	GetChatMessageByTaskAssistant(context.Context, pgtype.UUID) (db.ChatMessage, error)
}

// RetryEventPublisher reconstructs only server-persisted, already-redacted
// terminal chat output. The payload digest must equal the quarantined send, so
// the approval cannot be repurposed to publish different content.
type RetryEventPublisher struct {
	q   retryEventQueries
	bus *events.Bus
	now func() time.Time
}

func NewRetryEventPublisher(q retryEventQueries, bus *events.Bus) *RetryEventPublisher {
	return &RetryEventPublisher{q: q, bus: bus, now: func() time.Time { return time.Now().UTC() }}
}

func (p *RetryEventPublisher) PublishAuthorizedRetry(ctx context.Context, row db.ChannelDelivery) error {
	if p == nil || p.q == nil || p.bus == nil || row.Status != "retry_authorized" ||
		!row.RetryPublishToken.Valid || !row.RetryPublishExpiresAt.Valid || !p.now().Before(row.RetryPublishExpiresAt.Time) {
		return errors.New("authorized channel delivery retry has no live publish lease")
	}
	task, err := p.q.GetAgentTask(ctx, row.TaskID)
	if err != nil {
		return fmt.Errorf("load authorized retry task: %w", err)
	}
	if !task.ChatSessionID.Valid || task.ChatSessionID != row.ChatSessionID {
		return errors.New("authorized retry task no longer matches its chat session")
	}
	message, err := p.q.GetChatMessageByTaskAssistant(ctx, row.TaskID)
	if err != nil {
		return fmt.Errorf("load authorized retry message: %w", err)
	}
	if message.ChatSessionID != row.ChatSessionID || !message.TaskID.Valid || message.TaskID != row.TaskID {
		return errors.New("authorized retry message identity is inconsistent")
	}

	content := message.Content
	eventType := protocol.EventChatDone
	switch row.OperationKind {
	case OperationChatReply:
		if task.Status != "completed" {
			return errors.New("chat-reply retry requires a completed task")
		}
	case OperationFailureNotice:
		if task.Status != "failed" || content == "" {
			return errors.New("failure-notice retry requires a terminal failed task")
		}
		content = "⚠️ " + content
		eventType = protocol.EventTaskFailed
	default:
		return errors.New("authorized retry operation is unsupported")
	}
	if content == "" || PayloadDigest(content) != row.PayloadDigest {
		return errors.New("authorized retry payload digest does not match persisted chat output")
	}
	p.bus.Publish(events.Event{
		Type: eventType, WorkspaceID: util.UUIDToString(row.WorkspaceID), ActorType: "system",
		TaskID: util.UUIDToString(row.TaskID), ChatSessionID: util.UUIDToString(row.ChatSessionID),
		Payload: map[string]any{
			"task_id": util.UUIDToString(row.TaskID), "chat_session_id": util.UUIDToString(row.ChatSessionID),
			"content": content, "error": message.Content, "retry_pending": false,
		},
	})
	return nil
}
