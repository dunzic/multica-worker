package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Topic tasks stay private even when the agent itself is shared. Dedicated
// daemon credentials retain their workspace-scoped execution authority; a
// user PAT on a daemon endpoint must still pass the conversation ACL.
func (h *Handler) canReadTopicTask(r *http.Request, task db.AgentTaskQueue, workspaceID string, daemonRoute bool) (bool, error) {
	if !task.TopicID.Valid {
		return true, nil
	}
	if daemonRoute && middleware.DaemonWorkspaceIDFromContext(r.Context()) == workspaceID {
		return true, nil
	}
	userID := requestUserID(r)
	if userID == "" {
		return false, nil
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	return h.Queries.CanReadTopic(r.Context(), db.CanReadTopicParams{
		TopicID: task.TopicID, WorkspaceID: parseUUID(workspaceID),
		PrincipalType: actorType, PrincipalID: parseUUID(actorID),
	})
}

func (h *Handler) requireTopicTaskAccess(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, workspaceID string, daemonRoute bool) bool {
	allowed, err := h.canReadTopicTask(r, task, workspaceID, daemonRoute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check task access")
		return false
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "task not found")
		return false
	}
	return true
}
