package handler

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

// Group attachments inherit active participation, not workspace-wide visibility.
func (h *Handler) authorizeTopicAttachment(w http.ResponseWriter, r *http.Request, att db.Attachment) bool {
	topicID := att.TopicID
	if !topicID.Valid && !att.ChatMessageID.Valid {
		return true
	}
	if topicID.Valid {
		return h.authorizeTopicAttachmentID(w, r, att, topicID)
	}
	msg, err := h.Queries.GetMessageV2(r.Context(), att.ChatMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "attachment access lookup failed")
		return false
	}
	return h.authorizeTopicAttachmentID(w, r, att, msg.TopicID)
}

func (h *Handler) authorizeTopicAttachmentID(w http.ResponseWriter, r *http.Request, att db.Attachment, topicID pgtype.UUID) bool {
	topic, err := h.Queries.GetTopic(r.Context(), topicID)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return false
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(att.WorkspaceID))
	id, ok := parseUUIDOrBadRequest(w, actorID, "actor id")
	if !ok {
		return false
	}
	active, err := h.Queries.IsActiveParticipant(r.Context(), db.IsActiveParticipantParams{ConversationID: topic.ConversationID, PrincipalType: actorType, PrincipalID: id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "attachment access lookup failed")
		return false
	}
	if !active {
		writeError(w, http.StatusNotFound, "attachment not found")
		return false
	}
	if actorType == "member" {
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{WorkspaceID: att.WorkspaceID, UserID: id}); err != nil {
			writeError(w, http.StatusNotFound, "attachment not found")
			return false
		}
	}
	return true
}
