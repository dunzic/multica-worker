package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries is the slice of generated queries the Slack outbound
// subscriber needs. *db.Queries satisfies it.
type outboundQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	SetChatMessageChannelOutboundProvenanceByTask(ctx context.Context, arg db.SetChatMessageChannelOutboundProvenanceByTaskParams) (int64, error)
	RecordChannelOutboundMessage(ctx context.Context, arg db.RecordChannelOutboundMessageParams) error
}

// replySender posts one reply. Satisfied by *slackSender, so the outbound path
// reuses Send's Markdown->mrkdwn conversion, chunking, and threading.
type replySender interface {
	SendWithMetadata(ctx context.Context, out channel.OutboundMessage, metadata slack.SlackMetadata) (channel.SendResult, error)
}

// Outbound delivers an agent's chat reply back to Slack — the outbound half of
// the round trip. It mirrors the Feishu Patcher: on EventChatDone it finds the
// Slack chat binding for the finished task's session and posts the reply into
// the originating channel/thread. Sessions with no Slack binding are ignored,
// so it coexists with the Feishu Patcher on the shared event bus. It is only
// registered when Slack is configured.
type Outbound struct {
	q         outboundQueries
	decrypt   Decrypter
	logger    *slog.Logger
	delivery  delivery.Recorder
	newSender func(creds credentials) replySender
}

// NewOutbound builds the Slack outbound subscriber over the generated queries
// and the bot/app-token decrypter.
func NewOutbound(q outboundQueries, decrypt Decrypter, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{q: q, decrypt: decrypt, logger: logger}
	o.newSender = func(c credentials) replySender {
		// Only the bot token is needed to post; inbound Socket Mode uses the
		// installation's separate app-level token (see slack_channel.go).
		return newSlackSender(c, slack.New(c.BotToken), logger)
	}
	return o
}

// WithDeliveryRecorder enables connector-neutral idempotency and audited
// provider-message receipts. It is optional so isolated adapter tests and
// deployments upgrading before the ledger migration keep their old behavior.
func (o *Outbound) WithDeliveryRecorder(recorder delivery.Recorder) *Outbound {
	o.delivery = recorder
	return o
}

// Register subscribes to terminal chat events on the bus. A failure notice has
// an independent ledger identity from the normal reply and is omitted while an
// automatic retry is pending.
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	bus.Subscribe(protocol.EventTaskFailed, o.handleEvent)
}

func (o *Outbound) handleEvent(e events.Event) {
	// Bus delivery is synchronous, so a stuck Slack HTTP call must not wedge the
	// publish call site: use a fresh ctx with a tight timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.processEvent(ctx, e); err != nil {
		o.logger.WarnContext(ctx, "slack outbound: reply delivery failed",
			"error", err, "chat_session_id", e.ChatSessionID)
	}
}

func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	taskID, sessionID, ok := taskAndSessionFromEvent(e)
	if !ok || !sessionID.Valid {
		// Issue / autopilot tasks carry no chat_session.
		return nil
	}
	content := eventContent(e)
	if content == "" {
		return nil // nothing to say (empty completion)
	}
	// Only bound, non-empty completions reach here, so classify the task origin
	// before loading credentials or sending. Web/mobile direct-chat tasks can
	// reuse a session that originated in Slack, but their replies belong only in
	// Multica. Outbound delivery fails closed when the origin cannot be
	// established. Sealed channel tasks own an input batch just like direct
	// tasks, so the discriminator is the immutable channel_ingested provenance
	// of that batch, not chat_input_task_id presence (which #5645 originally
	// used).
	taskDelivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // direct Chat or another channel
		}
		return fmt.Errorf("lookup slack task delivery: %w", err)
	}
	if taskDelivery.ChannelType != string(TypeSlack) {
		return nil
	}
	binding := slackBindingFromTaskDelivery(taskDelivery)
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, o.q, task)
	if err != nil {
		return fmt.Errorf("classify task input origin: %w", err)
	}
	if !deliver {
		return nil
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeSlack),
	})
	if err != nil {
		return fmt.Errorf("load slack installation: %w", err)
	}
	if inst.Status != "active" {
		return nil // revoked between trigger and reply
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return fmt.Errorf("decode slack credentials: %w", err)
	}
	channelID, threadTS := outboundTarget(binding)
	operation := delivery.OperationChatReply
	outboundKind := "task_reply"
	if e.Type == protocol.EventTaskFailed {
		operation = delivery.OperationFailureNotice
		outboundKind = "failure_notice"
	}
	result, err := delivery.Send(ctx, o.delivery, delivery.ClaimInput{
		WorkspaceID: inst.WorkspaceID, InstallationID: inst.ID, TaskID: taskID, ChatSessionID: sessionID,
		ChannelType: TypeSlack, ChannelChatID: channelID, OperationKind: operation, Payload: content,
	}, func(sendCtx context.Context) (channel.SendResult, error) {
		return o.newSender(creds).SendWithMetadata(sendCtx, channel.OutboundMessage{
			ChatID: channelID, Text: content, ThreadID: threadTS,
		}, outboundMetadata(taskDelivery.BindingID, taskDelivery.RouteRevision, outboundKind))
	})
	if err != nil {
		return fmt.Errorf("post slack reply: %w", err)
	}
	messageIDs := result.MessageIDs
	if len(messageIDs) == 0 && result.MessageID != "" {
		messageIDs = []string{result.MessageID}
	}
	if len(messageIDs) == 0 {
		return errors.New("post slack reply: provider returned no message id")
	}
	rows, err := o.q.SetChatMessageChannelOutboundProvenanceByTask(ctx, db.SetChatMessageChannelOutboundProvenanceByTaskParams{
		ChannelType:    pgtype.Text{String: string(TypeSlack), Valid: true},
		InstallationID: binding.InstallationID,
		ChannelChatID:  pgtype.Text{String: channelID, Valid: true},
		MessageIds:     messageIDs,
		TaskID:         taskID,
	})
	if err != nil {
		return fmt.Errorf("record slack reply provenance: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("record slack reply provenance: updated %d assistant rows, want 1", rows)
	}
	for _, messageID := range messageIDs {
		if err := o.q.RecordChannelOutboundMessage(ctx, db.RecordChannelOutboundMessageParams{
			OutboundInstallationID: taskDelivery.InstallationID,
			OutboundChannelType:    taskDelivery.ChannelType,
			OutboundMessageID:      messageID,
			OutboundBindingID:      taskDelivery.BindingID,
			OutboundRouteRevision:  taskDelivery.RouteRevision,
			OutboundTaskID:         taskID,
			OutboundKind:           outboundKind,
		}); err != nil {
			return fmt.Errorf("record slack outbound message: %w", err)
		}
	}
	return nil
}

// taskAndSessionFromEvent accepts the typed chat completion and the map payload
// used by task-failed broadcasts. Outbound delivery fails closed unless both
// the task origin and chat session are established.
func taskAndSessionFromEvent(e events.Event) (taskID, sessionID pgtype.UUID, ok bool) {
	if e.TaskID != "" {
		_ = taskID.Scan(e.TaskID)
	}
	if e.ChatSessionID != "" {
		_ = sessionID.Scan(e.ChatSessionID)
	}
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		if !taskID.Valid {
			_ = taskID.Scan(p.TaskID)
		}
		if !sessionID.Valid {
			_ = sessionID.Scan(p.ChatSessionID)
		}
	case map[string]any:
		if !taskID.Valid {
			if raw, _ := p["task_id"].(string); raw != "" {
				_ = taskID.Scan(raw)
			}
		}
		if !sessionID.Valid {
			if raw, _ := p["chat_session_id"].(string); raw != "" {
				_ = sessionID.Scan(raw)
			}
		}
	}
	return taskID, sessionID, taskID.Valid
}

func slackBindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// outboundTarget recovers the real send target from the chat binding. The
// channel_chat_id may be a composite "channel:threadRoot" isolation key, so the
// real channel id is read from the binding config (slackBindingConfig); the
// reply thread is the recorded last_thread_id.
func outboundTarget(b db.ChannelChatSessionBinding) (channelID, threadTS string) {
	channelID = b.ChannelChatID
	if len(b.Config) > 0 {
		var cfg slackBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChannelID != "" {
			channelID = cfg.ChannelID
		}
	}
	if b.LastThreadID.Valid {
		threadTS = b.LastThreadID.String
	}
	return channelID, threadTS
}

// eventContent extracts a completed reply or the redacted terminal task error.
// Retry-pending failures remain silent because the retry will report its own
// terminal outcome.
func eventContent(e events.Event) string {
	switch p := e.Payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if e.Type == protocol.EventTaskFailed {
			if retryPending, _ := p["retry_pending"].(bool); retryPending {
				return ""
			}
			if value, _ := p["error"].(string); value != "" {
				return "⚠️ " + value
			}
			return ""
		}
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}
