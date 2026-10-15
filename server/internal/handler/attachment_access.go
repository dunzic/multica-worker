package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The optional task token binds a capability to the verified machine credential,
// not merely to its human owner. Neither field is taken from a public actor header.
type attachmentCapabilitySubject struct {
	UserID      string
	TaskTokenID string
}

func attachmentSubjectForRequest(w http.ResponseWriter, r *http.Request) (attachmentCapabilitySubject, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return attachmentCapabilitySubject{}, false
	}
	subject := attachmentCapabilitySubject{UserID: userID}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		token, verified := middleware.AuthenticatedTaskToken(r.Context())
		if !verified || uuidToString(token.UserID) != userID {
			writeError(w, http.StatusUnauthorized, "invalid task credential")
			return attachmentCapabilitySubject{}, false
		}
		subject.TaskTokenID = uuidToString(token.ID)
	}
	return subject, true
}

func (h *Handler) authorizeAttachmentSubject(w http.ResponseWriter, r *http.Request, att db.Attachment, subject attachmentCapabilitySubject) bool {
	deny := func() bool {
		writeError(w, http.StatusNotFound, "attachment not found")
		return false
	}
	if auth.IsTemporarilyDisabledUser(subject.UserID, "") {
		return deny()
	}
	// Query the primary database on every access, including stable Markdown links.
	// Positive membership caches can race with invalidation after removal.
	if _, err := h.getWorkspaceMember(r.Context(), subject.UserID, uuidToString(att.WorkspaceID)); err != nil {
		return deny()
	}
	actorType, actorID := "member", subject.UserID
	if subject.TaskTokenID != "" {
		tokenID, ok := parseUUIDOrBadRequest(w, subject.TaskTokenID, "task credential id")
		if !ok {
			return false
		}
		token, err := h.Queries.GetActiveTaskTokenByID(r.Context(), tokenID)
		if err != nil || token.WorkspaceID != att.WorkspaceID || uuidToString(token.UserID) != subject.UserID {
			return deny()
		}
		actorType, actorID = "agent", uuidToString(token.AgentID)
	}
	return h.authorizeTopicAttachmentActor(w, r, att, actorType, actorID)
}

func (h *Handler) authorizeAttachmentRequest(w http.ResponseWriter, r *http.Request, att db.Attachment) bool {
	subject, ok := attachmentSubjectForRequest(w, r)
	return ok && h.authorizeAttachmentSubject(w, r, att, subject)
}
