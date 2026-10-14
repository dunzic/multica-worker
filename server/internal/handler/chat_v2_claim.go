// Enrich group task claims with validated workspace, participant and pinned
// message context. Never substitute a later message for the triggering input.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ownsTopicTask is the chat v2 bridge called from CancelTaskByUser
// (chat.go). Returns true when `task` is a topic task whose conversation
// the caller is an active member of, in the caller's workspace. Returns
// false silently otherwise — the upstream cancel handler falls back to
// the 404 it would already write.
//
// Keeps the upstream chat.go diff to a single one-line `else if` arm.
func (h *Handler) ownsTopicTask(ctx context.Context, task db.AgentTaskQueue, userID, workspaceID string) bool {
	if !task.TopicID.Valid {
		return false
	}
	topic, err := h.Queries.GetTopic(ctx, task.TopicID)
	if err != nil {
		return false
	}
	wsUUID, err := h.Queries.GetConversationWorkspaceID(ctx, topic.ConversationID)
	if err != nil || util.UUIDToString(wsUUID) != workspaceID {
		return false
	}
	ok, err := h.Queries.IsActiveParticipant(ctx, db.IsActiveParticipantParams{
		ConversationID: topic.ConversationID,
		PrincipalType:  "member",
		PrincipalID:    parseUUID(userID),
	})
	return err == nil && ok
}

// enrichTopicClaim populates the topic-task arm of an AgentTaskResponse
// during a daemon claim. No-op when this isn't a topic task (chat_session
// / issue paths handle themselves in the upstream branches). Pointer
// receiver on resp so the caller's struct mutates in place.
func (h *Handler) enrichTopicClaim(ctx context.Context, task db.AgentTaskQueue, resp *AgentTaskResponse) error {
	if !task.TopicID.Valid {
		return nil
	}
	resp.TopicID = util.UUIDToString(task.TopicID)
	topic, err := h.Queries.GetTopic(ctx, task.TopicID)
	if err != nil {
		return err
	}
	ws, err := h.Queries.GetConversationWorkspaceID(ctx, topic.ConversationID)
	if err != nil {
		return err
	}
	resp.WorkspaceID = util.UUIDToString(ws)
	conv, err := h.Queries.GetConversation(ctx, db.GetConversationParams{ID: topic.ConversationID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	active, err := h.Queries.IsActiveParticipant(ctx, db.IsActiveParticipantParams{ConversationID: conv.ID, PrincipalType: "agent", PrincipalID: task.AgentID})
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("agent no longer participates in this group")
	}
	actor, err := h.Queries.IsActiveParticipant(ctx, db.IsActiveParticipantParams{ConversationID: conv.ID, PrincipalType: "member", PrincipalID: task.InitiatorUserID})
	if err != nil {
		return err
	}
	if !actor {
		return fmt.Errorf("sender no longer participates in this group")
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{WorkspaceID: ws, UserID: task.InitiatorUserID}); err != nil {
		return err
	}
	msg, err := h.Queries.GetMessageV2(ctx, task.TriggerEvidenceRefID)
	if err != nil {
		return err
	}
	if msg.TopicID != task.TopicID || msg.SenderID != task.InitiatorUserID {
		return fmt.Errorf("topic input does not match its run")
	}
	resp.ChatMessage = expandIssueMentions(ctx, h.Queries, ws, msg.Content)
	resp.ChatType = conv.Kind
	resp.ThreadName = topic.Title.String
	if recent, err := h.Queries.ListTopicMessagesForRun(ctx, db.ListTopicMessagesForRunParams{TopicID: task.TopicID, TriggerMessageID: msg.ID}); err != nil {
		return err
	} else {
		var history strings.Builder
		for i := len(recent) - 1; i >= 0; i-- {
			m := recent[i]
			if m.ID == msg.ID {
				continue
			}
			text := []rune(m.Content)
			if len(text) > 2000 {
				text = text[:2000]
			}
			fmt.Fprintf(&history, "[%s %s] %s\n", m.SenderType.String, util.UUIDToString(m.SenderID), string(text))
		}
		if history.Len() > 0 {
			resp.ChatMessage = "Recent topic context (may be truncated):\n" + history.String() + "\nCurrent request:\n" + resp.ChatMessage
		}
	}
	atts, err := h.Queries.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{ChatMessageID: msg.ID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, a := range atts {
		resp.ChatMessageAttachments = append(resp.ChatMessageAttachments, ChatAttachmentMeta{ID: util.UUIDToString(a.ID), Filename: a.Filename, ContentType: a.ContentType})
	}
	if !task.ForceFreshSession {
		prior, err := h.Queries.GetLastTopicTaskSession(ctx, db.GetLastTopicTaskSessionParams{TopicID: task.TopicID, AgentID: task.AgentID, RuntimeID: task.RuntimeID})
		if err == nil {
			resp.PriorSessionID = prior.SessionID.String
			resp.PriorWorkDir = prior.WorkDir.String
		}
	}
	return nil
}

// expandIssueMentions walks `content` for [Label](mention://issue/<uuid>)
// links and appends a "Referenced issues" context block listing each
// referenced issue's identifier (e.g. TES-1), title, status, and priority.
// Short DM prompts like "TES-1 这是什么?" otherwise leave the agent guessing
// what the URL means — agents rarely call `multica issue show` reactively
// for an opaque UUID. Resolved server-side so v2 works out of the box;
// upstream's chat_session arm still ships raw content (its prompt shape
// stays untouched).
//
// Returns the original content unchanged when there are no issue mentions
// or every lookup fails.
func expandIssueMentions(ctx context.Context, q *db.Queries, wsID pgtype.UUID, content string) string {

	mentions := util.ParseMentions(content)
	if len(mentions) == 0 {
		return content
	}
	seen := map[string]bool{}
	var lines []string
	for _, m := range mentions {
		if m.Type != "issue" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		issueUUID, parseErr := util.ParseUUID(m.ID)
		if parseErr != nil {
			continue
		}
		if !issueUUID.Valid {
			continue
		}
		issue, err := q.GetIssue(ctx, issueUUID)
		if err != nil || issue.WorkspaceID != wsID {
			slog.Warn("v2 mention expand: GetIssue failed", "issue_id", m.ID, "error", err)
			continue
		}
		label := fmt.Sprintf("#%d", issue.Number)
		// Best-effort prefix: same workspace lookup as upstream's
		// ExpandIssueIdentifiers — fall back to "#<n>" on miss.
		if ws, err := q.GetWorkspace(ctx, issue.WorkspaceID); err == nil && ws.IssuePrefix != "" {
			label = fmt.Sprintf("%s-%d", ws.IssuePrefix, issue.Number)
		}
		line := fmt.Sprintf("- %s: %q — status: %s, priority: %s",
			label, issue.Title, issue.Status, issue.Priority)
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return content
	}
	return content + "\n\n## Referenced issues\n\n" + strings.Join(lines, "\n")
}
