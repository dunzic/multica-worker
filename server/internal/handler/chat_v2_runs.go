package handler

import (
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"net/http"
)

type TopicRunWire struct {
	ID        string  `json:"id"`
	AgentID   string  `json:"agent_id"`
	Status    string  `json:"status"`
	Error     *string `json:"error"`
	MessageID string  `json:"message_id"`
	CreatedAt string  `json:"created_at"`
}

func (h *Handler) ListTopicRuns(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "topicId"), "topicId")
	if !ok {
		return
	}
	topic, err := h.Queries.GetTopic(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	if _, ok := h.requireActiveMember(w, r, topic.ConversationID, userID); !ok {
		return
	}
	runs, err := h.Queries.ListTopicRuns(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot load runs")
		return
	}
	out := []TopicRunWire{}
	for _, run := range runs {
		out = append(out, TopicRunWire{ID: uuidToString(run.ID), AgentID: uuidToString(run.AgentID), Status: run.Status, Error: util.TextToPtr(run.Error), MessageID: uuidToString(run.TriggerEvidenceRefID), CreatedAt: timestampToString(run.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, out)
}
