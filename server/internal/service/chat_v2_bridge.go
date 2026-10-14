package service

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"log/slog"
)

// Resolve current recipients rather than caching membership on the task.
func (s *TaskService) TopicRecipients(ctx context.Context, topicID pgtype.UUID) []string {
	out := []string{}
	ids, err := s.Queries.ListTopicRecipientUsers(ctx, topicID)
	if err != nil {
		return out
	}
	for _, id := range ids {
		out = append(out, util.UUIDToString(id))
	}
	return out
}

// writeTopicCompletion is atomic with the task's terminal transition.
func (s *TaskService) writeTopicCompletion(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, result []byte) (*db.ChatMessage, error) {
	if !task.TopicID.Valid {
		return nil, nil
	}
	var payload protocol.TaskCompletedPayload
	_ = json.Unmarshal(result, &payload)
	content := redact.Text(payload.Output)
	if content == "" {
		content = chatNoResponseFallback
	}
	msg, err := q.CreateMessageV2(ctx, db.CreateMessageV2Params{TopicID: task.TopicID, SenderType: util.StrToText("agent"), SenderID: task.AgentID, Content: content, TaskID: task.ID})
	if err != nil {
		return nil, err
	}
	topic, err := q.GetTopic(ctx, task.TopicID)
	if err != nil {
		return nil, err
	}
	ws, err := q.GetConversationWorkspaceID(ctx, topic.ConversationID)
	if err != nil {
		return nil, err
	}
	if _, err := q.BindChatAttachmentsToMessage(ctx, db.BindChatAttachmentsToMessageParams{ChatMessageID: msg.ID, WorkspaceID: ws, TaskID: task.ID}); err != nil {
		return nil, err
	}
	return &msg, nil
}

// broadcastMessageDone is the chat v2 counterpart to broadcastChatDone.
// Fires message:done to the topic's conversation's active member
// participants (agents excluded). Recipients are resolved here, not
// cached on the task: membership may have changed between dispatch
// and completion, and a stale list would misroute.
func (s *TaskService) broadcastMessageDone(ctx context.Context, task db.AgentTaskQueue, msg db.ChatMessage) {
	topic, err := s.Queries.GetTopic(ctx, task.TopicID)
	if err != nil {
		slog.Warn("message:done: topic lookup failed", "topic_id", util.UUIDToString(task.TopicID), "error", err)
		return
	}
	recipients := s.TopicRecipients(ctx, task.TopicID)
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventMessageDone,
		WorkspaceID: workspaceID,
		ActorType:   "agent",
		ActorID:     util.UUIDToString(task.AgentID),
		Payload: protocol.MessageDonePayload{
			ConversationID:   util.UUIDToString(topic.ConversationID),
			TopicID:          util.UUIDToString(task.TopicID),
			MessageID:        util.UUIDToString(msg.ID),
			TaskID:           util.UUIDToString(task.ID),
			AgentID:          util.UUIDToString(task.AgentID),
			Content:          msg.Content,
			CreatedAt:        util.TimestampToString(msg.CreatedAt),
			RecipientUserIDs: recipients,
		},
	})
}
