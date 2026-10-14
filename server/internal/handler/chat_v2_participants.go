package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

// Membership removal, last-admin protection and run cancellation share one transaction.
func (h *Handler) removeConversationParticipantAtomic(w http.ResponseWriter, r *http.Request, conv db.Conversation, caller, pType string, pID pgtype.UUID) bool {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return false
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if _, err := q.LockConversation(r.Context(), conv.ID); err != nil {
		writeError(w, http.StatusNotFound, "conversation not found")
		return false
	}
	parts, err := q.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{ConversationID: conv.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load participants failed")
		return false
	}
	admins := 0
	actorAdmin, actorActive, targetAdmin := false, false, false
	for _, p := range parts {
		if p.LeftAt.Valid {
			continue
		}
		if p.PrincipalType == "member" && p.Role == "admin" {
			admins++
		}
		if p.PrincipalType == "member" && uuidToString(p.PrincipalID) == caller {
			actorActive = true
			actorAdmin = p.Role == "admin"
		}
		if p.PrincipalType == pType && p.PrincipalID == pID {
			targetAdmin = p.PrincipalType == "member" && p.Role == "admin"
		}
	}
	if !actorActive || (uuidToString(pID) != caller || pType != "member") && !actorAdmin {
		writeError(w, http.StatusForbidden, "group admin required")
		return false
	}
	if conv.Kind == "group" && targetAdmin && admins <= 1 {
		writeError(w, http.StatusConflict, "appoint another admin before leaving or removing the last admin")
		return false
	}
	if err := q.RemoveParticipant(r.Context(), db.RemoveParticipantParams{ConversationID: conv.ID, PrincipalType: pType, PrincipalID: pID}); err != nil {
		writeError(w, http.StatusInternalServerError, "remove participant failed")
		return false
	}
	cancelled, err := q.CancelTopicParticipantRuns(r.Context(), db.CancelTopicParticipantRunsParams{ConversationID: conv.ID, PrincipalType: pType, PrincipalID: pID, CallerID: parseUUID(caller)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cancel participant runs failed")
		return false
	}
	for _, task := range cancelled {
		if err := service.SettleTerminalTaskState(r.Context(), q, task); err != nil {
			writeError(w, http.StatusInternalServerError, "settle participant runs failed")
			return false
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return false
	}
	h.TaskService.BroadcastCancelledTasks(r.Context(), uuidToString(conv.WorkspaceID), cancelled)
	return true
}
