// Group collaboration realtime fan-out.
//
// The model: each v2 event payload carries `recipient_user_ids` at the
// top level (the active member participants of the conversation at the
// moment of publish). The bus listener in cmd/server/listeners.go reads
// that list and routes via Broadcaster.SendToUser, so DMs and private
// groups never leak existence to non-members.
//
// Recipients are recomputed PER EVENT — not cached. Memberships change
// (participant:added / participant:removed), and a stale cached list
// would silently misroute. The query is cheap (one indexed seek per
// conversation) and the fan-out path isn't hot enough to need caching.
package handler

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// activeMemberRecipients returns the user_ids of every active member
// participant of the conversation. Empty slice (not nil) on error so
// callers can treat the result uniformly.
//
// Agents are deliberately excluded: they're servers in this model, not
// WS clients. When an agent needs to react to a message it does so via
// the task-dispatch path, not via WS.
func (h *Handler) activeMemberRecipients(ctx context.Context, conversationID pgtype.UUID) []string {
	ids, err := h.Queries.ListConversationRecipientUsers(ctx, conversationID)
	out := []string{}
	if err != nil {
		return out
	}
	for _, id := range ids {
		out = append(out, uuidToString(id))
	}
	return out
}

// publishConversationCreated fires conversation:created to every active
// member participant. Called immediately after participant rows are
// inserted so recipients are guaranteed to include the creator + invitees.
func (h *Handler) publishConversationCreated(ctx context.Context, workspaceID, actorID string, conv db.Conversation) {
	publishV2Event(h.Bus, protocol.EventConversationCreated, workspaceID, "member", actorID,
		protocol.ConversationSummary{
			ConversationID:   uuidToString(conv.ID),
			WorkspaceID:      workspaceID,
			Kind:             conv.Kind,
			Title:            textToPtr(conv.Title),
			ArchivedAt:       timestampToPtr(conv.ArchivedAt),
			UpdatedAt:        timestampToString(conv.UpdatedAt),
			RecipientUserIDs: h.activeMemberRecipients(ctx, conv.ID),
		})
}

// publishConversationUpdated fires conversation:updated after a rename or
// archive/unarchive. Recipients are the current active members — anyone
// who left earlier won't see the rename, which matches their UX (the row
// dropped from their sidebar when they left).
func (h *Handler) publishConversationUpdated(ctx context.Context, actorID string, conv db.Conversation) {
	publishV2Event(h.Bus, protocol.EventConversationUpdated, uuidToString(conv.WorkspaceID), "member", actorID,
		protocol.ConversationSummary{
			ConversationID:   uuidToString(conv.ID),
			WorkspaceID:      uuidToString(conv.WorkspaceID),
			Kind:             conv.Kind,
			Title:            textToPtr(conv.Title),
			ArchivedAt:       timestampToPtr(conv.ArchivedAt),
			UpdatedAt:        timestampToString(conv.UpdatedAt),
			RecipientUserIDs: h.activeMemberRecipients(ctx, conv.ID),
		})
}

// publishV2Event is a thin wrapper that fills in the constant Event fields.
// Kept as a function rather than a method on Handler so service/task.go can
// reuse the same shape from the task-completion path (it would import this
// package, but task.go already imports events directly).
func publishV2Event(bus *events.Bus, eventType, workspaceID, actorType, actorID string, payload any) {
	if bus == nil || workspaceID == "" {
		return
	}
	bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload:     payload,
	})
}
