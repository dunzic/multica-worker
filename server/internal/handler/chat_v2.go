// Workspace-scoped group collaboration. Existing single-chat routes remain
// independent; messages and runs here belong to a conversation topic.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// MountChatV2Routes registers the /api/v2 chat surface on a Chi router.
// Caller is expected to have already wired the auth + workspace
// middleware on r; this function only adds the v2-specific routes.
func MountChatV2Routes(r chi.Router, h *Handler) {
	r.Route("/api/v2/conversations", func(r chi.Router) {
		r.Post("/", h.CreateConversation)
		r.Get("/", h.ListConversations)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.GetConversation)
			r.Patch("/", h.UpdateConversation)
			r.Delete("/", h.LeaveConversation)
			r.Post("/read", h.MarkConversationRead)
			r.Post("/participants", h.AddConversationParticipant)
			r.Delete("/participants/{type}/{principalId}", h.RemoveConversationParticipant)
			r.Get("/topics", h.ListTopics)
			r.Post("/topics", h.CreateTopic)
		})
	})
	r.Route("/api/v2/topics/{topicId}", func(r chi.Router) {
		r.Patch("/", h.UpdateTopic)
		r.Get("/messages", h.ListTopicMessages)
		r.Post("/messages", h.SendTopicMessage)
		r.Get("/pending-task", h.GetPendingTopicTask)
		r.Get("/runs", h.ListTopicRuns)
	})
	r.Get("/api/v2/pending-tasks", h.ListPendingTopicTasks)
	r.Get("/api/v2/unread-counts", h.ListUnreadCounts)
}

// ---------------------------------------------------------------------------
// Wire types (request / response JSON shapes)
// ---------------------------------------------------------------------------

type Principal struct {
	Type string `json:"type"` // "member" | "agent"
	ID   string `json:"id"`
}

type ParticipantWire struct {
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	Role          string  `json:"role"`
	JoinedAt      string  `json:"joined_at"`
	LeftAt        *string `json:"left_at,omitempty"`
}

type ConversationWire struct {
	ID           string            `json:"id"`
	WorkspaceID  string            `json:"workspace_id"`
	Kind         string            `json:"kind"`
	Title        *string           `json:"title,omitempty"`
	Participants []ParticipantWire `json:"participants"`
	ArchivedAt   *string           `json:"archived_at,omitempty"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at"`
}

type TopicWire struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id"`
	Title          *string `json:"title,omitempty"`
	CreatedByType  string  `json:"created_by_type"`
	CreatedByID    string  `json:"created_by_id"`
	ArchivedAt     *string `json:"archived_at,omitempty"`
	LastMessageAt  string  `json:"last_message_at"`
	CreatedAt      string  `json:"created_at"`
}

type MessageWire struct {
	ID          string               `json:"id"`
	TopicID     string               `json:"topic_id"`
	SenderType  string               `json:"sender_type"`
	SenderID    string               `json:"sender_id"`
	Content     string               `json:"content"`
	TaskID      *string              `json:"task_id,omitempty"`
	CreatedAt   string               `json:"created_at"`
	Attachments []AttachmentResponse `json:"attachments,omitempty"`
}

// ---------------------------------------------------------------------------
// DM signature — MUST match migration 573's CASE expression exactly.
// "member:<uuid>" and "agent:<uuid>" tokens are sorted lexicographically
// then joined with '|'. Order of (a, b) at the call site is irrelevant.
// ---------------------------------------------------------------------------

// ComputeDMSignature returns the canonical signature for the unique DM
// between two principals in a workspace. The signature is what the
// partial UNIQUE index on conversation(workspace_id, dm_signature)
// dedupes on — drift with the history backfill would cause two
// rows for the same pair.
func ComputeDMSignature(aType string, aID pgtype.UUID, bType string, bID pgtype.UUID) string {
	a := aType + ":" + uuidToString(aID)
	b := bType + ":" + uuidToString(bID)
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

// ---------------------------------------------------------------------------
// ACL helper
// ---------------------------------------------------------------------------

// requireActiveMember verifies the calling user is an active participant
// (left_at IS NULL) of the conversation. Writes 403 + returns false on
// failure. Reads 404 if the conversation doesn't exist at all (don't leak
// existence to non-members).
func (h *Handler) requireActiveMember(w http.ResponseWriter, r *http.Request, conversationID pgtype.UUID, userID string) (db.Conversation, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	ws, valid := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !valid {
		return db.Conversation{}, false
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(userID), WorkspaceID: ws}); err != nil {
		writeError(w, http.StatusNotFound, "conversation not found")
		return db.Conversation{}, false
	}
	conv, err := h.Queries.GetConversation(r.Context(), db.GetConversationParams{
		ID:          conversationID,
		WorkspaceID: ws,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "conversation not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load conversation")
		}
		return db.Conversation{}, false
	}
	ok, err := h.Queries.IsActiveParticipant(r.Context(), db.IsActiveParticipantParams{
		ConversationID: conversationID,
		PrincipalType:  "member",
		PrincipalID:    parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check participation")
		return db.Conversation{}, false
	}
	if !ok {
		// Match the GetConversation 404 — don't reveal that the
		// conversation exists to someone who isn't in it.
		writeError(w, http.StatusNotFound, "conversation not found")
		return db.Conversation{}, false
	}
	return conv, true
}

// ---------------------------------------------------------------------------
// Conversations
// ---------------------------------------------------------------------------

type CreateConversationRequest struct {
	Kind         string      `json:"kind"`         // "group" | "dm"
	Title        string      `json:"title"`        // required for group, ignored for dm
	Participants []Principal `json:"participants"` // for dm: exactly one OTHER principal; for group: ≥1 OTHER
}

func (h *Handler) CreateConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	var req CreateConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	switch req.Kind {
	case "dm":
		h.createOrGetDM(w, r, workspaceID, userID, req.Participants)
	case "group":
		h.createGroup(w, r, workspaceID, userID, req.Title, req.Participants)
	default:
		writeError(w, http.StatusBadRequest, "kind must be 'dm' or 'group'")
	}
}

func (h *Handler) createOrGetDM(w http.ResponseWriter, r *http.Request, workspaceID, userID string, others []Principal) {
	if len(others) != 1 {
		writeError(w, http.StatusBadRequest, "dm requires one other participant")
		return
	}
	h.createConversationAtomic(w, r, workspaceID, userID, "dm", "", others)
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request, workspaceID, userID, title string, others []Principal) {
	if strings.TrimSpace(title) == "" || len(others) == 0 || len(others) > 100 {
		writeError(w, http.StatusBadRequest, "group requires a title and 1-100 participants")
		return
	}
	h.createConversationAtomic(w, r, workspaceID, userID, "group", strings.TrimSpace(title), others)
}

func (h *Handler) ListConversations(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	includeArchived := r.URL.Query().Get("archived") == "true"
	var kindFilter pgtype.Text
	if k := r.URL.Query().Get("kind"); k == "dm" || k == "group" {
		kindFilter = pgtype.Text{String: k, Valid: true}
	}

	convs, err := h.Queries.ListConversationsByMember(r.Context(), db.ListConversationsByMemberParams{
		WorkspaceID:     parseUUID(workspaceID),
		PrincipalID:     parseUUID(userID),
		IncludeArchived: pgtype.Bool{Bool: includeArchived, Valid: true},
		Kind:            kindFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list conversations")
		return
	}

	out := make([]ConversationWire, 0, len(convs))
	for _, c := range convs {
		out = append(out, h.hydrateConversation(r, c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	conv, ok := h.requireActiveMember(w, r, id, userID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.hydrateConversation(r, conv))
}

type UpdateConversationRequest struct {
	Title  *string `json:"title,omitempty"`
	Action string  `json:"action,omitempty"` // "archive" | "unarchive"
}

func (h *Handler) UpdateConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	conv, ok := h.requireActiveMember(w, r, id, userID)
	if !ok {
		return
	}
	if !h.requireConversationAdmin(w, r, conv, userID) {
		return
	}

	var req UpdateConversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Title != nil {
		if conv.Kind != "group" {
			writeError(w, http.StatusBadRequest, "only group conversations can be renamed")
			return
		}
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
		updated, err := h.Queries.UpdateConversationTitle(r.Context(), db.UpdateConversationTitleParams{
			ID:    id,
			Title: strToText(title),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update title")
			return
		}
		conv = updated
	}

	switch req.Action {
	case "archive":
		if err := h.Queries.ArchiveConversation(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to archive")
			return
		}
		conv, _ = h.Queries.GetConversation(r.Context(), db.GetConversationParams{ID: id, WorkspaceID: parseUUID(h.resolveWorkspaceID(r))})
	case "unarchive":
		if err := h.Queries.UnarchiveConversation(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to unarchive")
			return
		}
		conv, _ = h.Queries.GetConversation(r.Context(), db.GetConversationParams{ID: id, WorkspaceID: parseUUID(h.resolveWorkspaceID(r))})
	case "":
		// no-op
	default:
		writeError(w, http.StatusBadRequest, "action must be 'archive' or 'unarchive'")
		return
	}

	h.publishConversationUpdated(r.Context(), userID, conv)
	writeJSON(w, http.StatusOK, h.hydrateConversation(r, conv))
}

func (h *Handler) LeaveConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	conv, ok := h.requireActiveMember(w, r, id, userID)
	if !ok {
		return
	}

	// Snapshot recipients BEFORE removing — we want the leaver to receive
	// their own participant:removed so their sidebar drops the row.
	recipients := h.activeMemberRecipients(r.Context(), id)

	if !h.removeConversationParticipantAtomic(w, r, conv, userID, "member", parseUUID(userID)) {
		return
	}

	publishV2Event(h.Bus, protocol.EventParticipantRemoved, uuidToString(conv.WorkspaceID), "member", userID,
		protocol.ParticipantChangePayload{
			ConversationID:   uuidToString(id),
			PrincipalType:    "member",
			PrincipalID:      userID,
			RecipientUserIDs: recipients,
		})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

type AddParticipantRequest struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Role string `json:"role,omitempty"` // "member" | "admin", default "member"
}

func (h *Handler) AddConversationParticipant(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	conv, ok := h.requireActiveMember(w, r, id, userID)
	if !ok {
		return
	}
	if conv.Kind != "group" {
		writeError(w, http.StatusBadRequest, "only groups accept participants")
		return
	}
	var req AddParticipantRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	role := req.Role
	if role == "" {
		role = "member"
	}
	if (role != "member" && role != "admin") || req.Type == "agent" && role == "admin" {
		writeError(w, http.StatusBadRequest, "invalid participant role")
		return
	}
	pid, ok := parseUUIDOrBadRequest(w, req.ID, "participant.id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	if _, err := q.LockConversation(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	parts, err := q.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{ConversationID: id})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load participants failed")
		return
	}
	isAdmin, targetAdmin, adminCount := false, false, 0
	for _, p := range parts {
		if p.LeftAt.Valid {
			continue
		}
		if p.PrincipalType == "member" && p.Role == "admin" {
			adminCount++
			if uuidToString(p.PrincipalID) == userID {
				isAdmin = true
			}
			if p.PrincipalID == pid && req.Type == "member" {
				targetAdmin = true
			}
		}
	}
	if !isAdmin {
		writeError(w, http.StatusForbidden, "group admin required")
		return
	}
	if role != "admin" && targetAdmin && adminCount <= 1 {
		writeError(w, http.StatusConflict, "appoint another admin first")
		return
	}
	if !h.validateConversationPrincipal(w, r, q, conv.WorkspaceID, userID, Principal{Type: req.Type, ID: req.ID}) {
		return
	}
	participant, err := q.AddParticipant(r.Context(), db.AddParticipantParams{ConversationID: id, PrincipalType: req.Type, PrincipalID: pid, Role: strToText(role)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "add participant failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}
	publishV2Event(h.Bus, protocol.EventParticipantAdded, uuidToString(conv.WorkspaceID), "member", userID, protocol.ParticipantChangePayload{ConversationID: uuidToString(id), PrincipalType: req.Type, PrincipalID: req.ID, Role: role, RecipientUserIDs: h.activeMemberRecipients(r.Context(), id)})
	writeJSON(w, http.StatusCreated, participantToWire(participant))
}

func (h *Handler) RemoveConversationParticipant(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	conv, ok := h.requireActiveMember(w, r, id, userID)
	if !ok {
		return
	}
	if !h.requireConversationAdmin(w, r, conv, userID) {
		return
	}
	if conv.Kind != "group" {
		writeError(w, http.StatusBadRequest, "dm participants cannot be removed (delete the conversation instead)")
		return
	}

	pType := chi.URLParam(r, "type")
	pID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "principalId"), "principalId")
	if !ok {
		return
	}
	if !pID.Valid || (pType != "member" && pType != "agent") {
		writeError(w, http.StatusBadRequest, "invalid participant")
		return
	}

	// Snapshot BEFORE removing so the removed principal receives their own
	// participant:removed (mirrors LeaveConversation).
	recipients := h.activeMemberRecipients(r.Context(), id)

	if !h.removeConversationParticipantAtomic(w, r, conv, userID, pType, pID) {
		return
	}

	publishV2Event(h.Bus, protocol.EventParticipantRemoved, uuidToString(conv.WorkspaceID), "member", userID,
		protocol.ParticipantChangePayload{
			ConversationID:   uuidToString(id),
			PrincipalType:    pType,
			PrincipalID:      uuidToString(pID),
			RecipientUserIDs: recipients,
		})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Topics
// ---------------------------------------------------------------------------

func (h *Handler) ListTopics(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	if _, ok := h.requireActiveMember(w, r, id, userID); !ok {
		return
	}
	includeArchived := r.URL.Query().Get("archived") == "true"
	topics, err := h.Queries.ListTopicsInConversation(r.Context(), db.ListTopicsInConversationParams{
		ConversationID:  id,
		IncludeArchived: pgtype.Bool{Bool: includeArchived, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list topics")
		return
	}
	out := make([]TopicWire, 0, len(topics))
	for _, t := range topics {
		out = append(out, topicToWire(t))
	}
	writeJSON(w, http.StatusOK, out)
}

type CreateTopicRequest struct {
	Title string `json:"title"`
}

func (h *Handler) CreateTopic(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	if _, ok := h.requireActiveMember(w, r, id, userID); !ok {
		return
	}

	var req CreateTopicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	title := pgtype.Text{Valid: false}
	if s := strings.TrimSpace(req.Title); s != "" {
		title = pgtype.Text{String: s, Valid: true}
	}

	t, err := h.Queries.CreateTopic(r.Context(), db.CreateTopicParams{
		ConversationID: id,
		Title:          title,
		CreatedByType:  "member",
		CreatedByID:    parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create topic")
		return
	}
	publishV2Event(h.Bus, protocol.EventTopicCreated, h.resolveWorkspaceID(r), "member", userID,
		protocol.TopicChangePayload{
			ConversationID:   uuidToString(id),
			TopicID:          uuidToString(t.ID),
			Title:            textToPtr(t.Title),
			LastMessageAt:    timestampToString(t.LastMessageAt),
			RecipientUserIDs: h.activeMemberRecipients(r.Context(), id),
		})
	writeJSON(w, http.StatusCreated, topicToWire(t))
}

type UpdateTopicRequest struct {
	Title  *string `json:"title,omitempty"`
	Action string  `json:"action,omitempty"` // "archive"
}

func (h *Handler) UpdateTopic(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "topicId"), "topicId")
	if !ok {
		return
	}
	if !tid.Valid {
		writeError(w, http.StatusBadRequest, "invalid topic id")
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

	var req UpdateTopicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title != nil {
		s := strings.TrimSpace(*req.Title)
		updated, err := h.Queries.UpdateTopicTitle(r.Context(), db.UpdateTopicTitleParams{
			ID:    tid,
			Title: pgtype.Text{String: s, Valid: s != ""},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update topic")
			return
		}
		topic = updated
	}
	if req.Action == "archive" {
		if err := h.Queries.ArchiveTopic(r.Context(), tid); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to archive topic")
			return
		}
		topic, _ = h.Queries.GetTopic(r.Context(), tid)
	}
	publishV2Event(h.Bus, protocol.EventTopicUpdated, h.resolveWorkspaceID(r), "member", userID,
		protocol.TopicChangePayload{
			ConversationID:   uuidToString(topic.ConversationID),
			TopicID:          uuidToString(topic.ID),
			Title:            textToPtr(topic.Title),
			ArchivedAt:       timestampToPtr(topic.ArchivedAt),
			LastMessageAt:    timestampToString(topic.LastMessageAt),
			RecipientUserIDs: h.activeMemberRecipients(r.Context(), topic.ConversationID),
		})
	writeJSON(w, http.StatusOK, topicToWire(topic))
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (h *Handler) ListTopicMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	tid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "topicId"), "topicId")
	if !ok {
		return
	}
	if !tid.Valid {
		writeError(w, http.StatusBadRequest, "invalid topic id")
		return
	}
	topic, err := h.Queries.GetTopic(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	conv, ok := h.requireActiveMember(w, r, topic.ConversationID, userID)
	if !ok {
		return
	}

	msgs, err := h.Queries.ListMessagesByTopic(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	// Bulk-load attachments for every message in this topic — same
	// pattern as the legacy chat handler. Saves N+1; the group-by
	// is local map work. Best-effort: load failures degrade to "no
	// attachments shown" rather than failing the whole topic load.
	atts := []db.Attachment{}
	if len(msgs) > 0 {
		ids := make([]pgtype.UUID, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		loaded, err := h.Queries.ListAttachmentsByChatMessageIDs(r.Context(), db.ListAttachmentsByChatMessageIDsParams{
			Column1:     ids,
			WorkspaceID: conv.WorkspaceID,
		})
		if err != nil {
			slog.Warn("load v2 topic attachments failed", "topic_id", uuidToString(tid), "error", err)
		} else {
			atts = loaded
		}
	}
	grouped := make(map[string][]AttachmentResponse, len(msgs))
	for _, a := range atts {
		mid := uuidToString(a.ChatMessageID)
		grouped[mid] = append(grouped[mid], h.attachmentToResponse(a, attachmentURLModeFromRequest(r)))
	}

	out := make([]MessageWire, 0, len(msgs))
	for _, m := range msgs {
		wire := messageToWire(m)
		wire.Attachments = grouped[uuidToString(m.ID)]
		out = append(out, wire)
	}
	writeJSON(w, http.StatusOK, out)
}

type SendMessageRequest struct {
	Content       string   `json:"content"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

type SendMessageResponse struct {
	TaskIDs []string    `json:"task_ids"`
	Message MessageWire `json:"message"`
	TaskID  *string     `json:"task_id,omitempty"`
}

func (h *Handler) SendTopicMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, h.resolveWorkspaceID(r))
	if actorType != "member" {
		writeError(w, http.StatusForbidden, "agent messages do not trigger group runs")
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
	conv, ok := h.requireActiveMember(w, r, topic.ConversationID, userID)
	if !ok {
		return
	}
	if topic.ArchivedAt.Valid || conv.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "conversation or topic is archived")
		return
	}
	var req SendMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Content) == "" && len(req.AttachmentIDs) == 0 {
		writeError(w, http.StatusBadRequest, "content or attachments required")
		return
	}
	if len(req.AttachmentIDs) > service.MaxChatMessageAttachments {
		writeError(w, http.StatusBadRequest, "a message supports at most 100 attachments")
		return
	}
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}
	parts, err := h.Queries.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{ConversationID: conv.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load participants")
		return
	}
	activeAgents := map[string]pgtype.UUID{}
	for _, p := range parts {
		if p.PrincipalType == "agent" && !p.LeftAt.Valid {
			activeAgents[uuidToString(p.PrincipalID)] = p.PrincipalID
		}
	}
	var targets []pgtype.UUID
	if conv.Kind == "dm" && len(activeAgents) == 1 {
		for _, id := range activeAgents {
			targets = append(targets, id)
		}
	}
	if conv.Kind == "group" {
		for _, id := range parseAgentMentions(req.Content) {
			target, found := activeAgents[id]
			if !found {
				writeError(w, http.StatusBadRequest, "mentioned agent is not in this group")
				return
			}
			targets = append(targets, target)
		}
	}
	for _, id := range targets {
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: conv.WorkspaceID})
		if err != nil || agent.ArchivedAt.Valid {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), uuidToString(conv.WorkspaceID)) {
			writeError(w, http.StatusForbidden, "you cannot run this agent")
			return
		}
		if !agent.RuntimeID.Valid {
			writeError(w, http.StatusConflict, "agent has no runtime")
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	// Serialize sends against participant changes. In particular, a leave must
	// cancel all of the sender's runs, including any concurrent message send.
	locked, err := q.LockConversation(r.Context(), conv.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if locked.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "conversation is archived")
		return
	}
	currentParts, err := q.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{ConversationID: conv.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load participants")
		return
	}
	senderActive := false
	currentAgents := map[pgtype.UUID]bool{}
	for _, p := range currentParts {
		if p.LeftAt.Valid {
			continue
		}
		if p.PrincipalType == "member" && uuidToString(p.PrincipalID) == userID {
			senderActive = true
		}
		if p.PrincipalType == "agent" {
			currentAgents[p.PrincipalID] = true
		}
	}
	if !senderActive {
		writeError(w, http.StatusNotFound, "conversation not found")
		return
	}
	for _, target := range targets {
		if !currentAgents[target] {
			writeError(w, http.StatusConflict, "mentioned agent left this group")
			return
		}
	}
	msg, err := q.CreateMessageV2(r.Context(), db.CreateMessageV2Params{TopicID: tid, SenderType: strToText("member"), SenderID: parseUUID(userID), Content: req.Content})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to send message")
		return
	}
	if len(attachmentIDs) > 0 {
		linked, err := q.LinkTopicAttachmentsToMessage(r.Context(), db.LinkTopicAttachmentsToMessageParams{ChatMessageID: msg.ID, TopicID: tid, WorkspaceID: conv.WorkspaceID, UploaderID: parseUUID(userID), AttachmentIds: attachmentIDs})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "attachment link failed")
			return
		}
		if len(linked) != len(attachmentIDs) {
			writeError(w, http.StatusBadRequest, "attachments invalid or already linked")
			return
		}
		if err := service.ValidateChatMessageAttachments(r.Context(), q, conv.WorkspaceID, msg.ID); err != nil {
			if errors.Is(err, service.ErrChatAttachmentCapacity) {
				writeError(w, http.StatusRequestEntityTooLarge, err.Error())
			} else {
				writeError(w, http.StatusInternalServerError, "could not validate attachment capacity")
			}
			return
		}
	}
	resp := SendMessageResponse{Message: messageToWire(msg), TaskIDs: []string{}}
	queued := make([]db.AgentTaskQueue, 0, len(targets))
	for _, agentID := range targets {
		agent, err := q.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: conv.WorkspaceID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "load agent failed")
			return
		}
		task, err := q.CreateTopicTask(r.Context(), db.CreateTopicTaskParams{AgentID: agentID, RuntimeID: agent.RuntimeID, Priority: 2, TopicID: tid, OriginatorUserID: parseUUID(userID), TriggerMessageID: msg.ID})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "enqueue run failed")
			return
		}
		resp.TaskIDs = append(resp.TaskIDs, uuidToString(task.ID))
		queued = append(queued, task)
	}
	if err := q.TouchConversation(r.Context(), conv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "update conversation failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}
	if len(resp.TaskIDs) > 0 {
		resp.TaskID = &resp.TaskIDs[0]
	}
	// Only committed tasks may invalidate the empty-claim cache and wake a
	// daemon. A rolled-back message must never dispatch any of its agents.
	for _, task := range queued {
		h.TaskService.NotifyTaskEnqueued(r.Context(), task)
	}
	publishV2Event(h.Bus, protocol.EventMessageSent, uuidToString(conv.WorkspaceID), "member", userID, protocol.MessageSentPayload{ConversationID: uuidToString(conv.ID), TopicID: uuidToString(tid), MessageID: uuidToString(msg.ID), SenderType: "member", SenderID: userID, Content: msg.Content, CreatedAt: timestampToString(msg.CreatedAt), RecipientUserIDs: h.activeMemberRecipients(r.Context(), conv.ID)})
	writeJSON(w, http.StatusCreated, resp)
}

// Parse canonical mention links only, with UUID validation and deduplication.
func parseAgentMentions(content string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, mention := range util.ParseMentions(content) {
		if mention.Type != "agent" {
			continue
		}
		id, err := util.ParseUUID(mention.ID)
		if err != nil {
			continue
		}
		key := util.UUIDToString(id)
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

type MarkReadRequest struct {
	LastReadTopicID string `json:"last_read_topic_id"`
}

func (h *Handler) MarkConversationRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	if !id.Valid {
		writeError(w, http.StatusBadRequest, "invalid conversation id")
		return
	}
	if _, ok := h.requireActiveMember(w, r, id, userID); !ok {
		return
	}

	var req MarkReadRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body OK
	topicID := pgtype.UUID{}
	if req.LastReadTopicID != "" {
		var valid bool
		topicID, valid = parseUUIDOrBadRequest(w, req.LastReadTopicID, "last_read_topic_id")
		if !valid {
			return
		}
		topic, err := h.Queries.GetTopic(r.Context(), topicID)
		if err != nil || topic.ConversationID != id {
			writeError(w, http.StatusBadRequest, "read marker does not belong to this conversation")
			return
		}
	}

	if err := h.Queries.UpdateParticipantReadState(r.Context(), db.UpdateParticipantReadStateParams{
		ConversationID:  id,
		PrincipalType:   "member",
		PrincipalID:     parseUUID(userID),
		LastReadTopicID: topicID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}

	// Read-state is per-user; only fan out to the SAME user's other tabs.
	// Other participants don't care that I marked something read.
	var lastReadPtr *string
	if topicID.Valid {
		s := uuidToString(topicID)
		lastReadPtr = &s
	}
	publishV2Event(h.Bus, protocol.EventConversationRead, h.resolveWorkspaceID(r), "member", userID,
		protocol.ConversationReadPayload{
			ConversationID:   uuidToString(id),
			UserID:           userID,
			LastReadTopicID:  lastReadPtr,
			RecipientUserIDs: []string{userID},
		})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Hydration / wire conversion
// ---------------------------------------------------------------------------

func (h *Handler) hydrateConversation(r *http.Request, c db.Conversation) ConversationWire {
	wire := ConversationWire{
		ID:          uuidToString(c.ID),
		WorkspaceID: uuidToString(c.WorkspaceID),
		Kind:        c.Kind,
		Title:       textToPtr(c.Title),
		ArchivedAt:  timestampToPtr(c.ArchivedAt),
		CreatedAt:   timestampToString(c.CreatedAt),
		UpdatedAt:   timestampToString(c.UpdatedAt),
	}
	parts, err := h.Queries.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{
		ConversationID: c.ID,
		IncludeRemoved: pgtype.Bool{Bool: false, Valid: true},
	})
	if err == nil {
		wire.Participants = make([]ParticipantWire, 0, len(parts))
		for _, p := range parts {
			wire.Participants = append(wire.Participants, participantToWire(p))
		}
		// Stable order for clients (member→agent within, then by ID).
		sort.Slice(wire.Participants, func(i, j int) bool {
			a, b := wire.Participants[i], wire.Participants[j]
			if a.PrincipalType != b.PrincipalType {
				return a.PrincipalType < b.PrincipalType
			}
			return a.PrincipalID < b.PrincipalID
		})
	} else {
		wire.Participants = []ParticipantWire{}
	}
	return wire
}

func participantToWire(p db.ConversationParticipant) ParticipantWire {
	return ParticipantWire{
		PrincipalType: p.PrincipalType,
		PrincipalID:   uuidToString(p.PrincipalID),
		Role:          p.Role,
		JoinedAt:      timestampToString(p.JoinedAt),
		LeftAt:        timestampToPtr(p.LeftAt),
	}
}

func topicToWire(t db.Topic) TopicWire {
	return TopicWire{
		ID:             uuidToString(t.ID),
		ConversationID: uuidToString(t.ConversationID),
		Title:          textToPtr(t.Title),
		CreatedByType:  t.CreatedByType,
		CreatedByID:    uuidToString(t.CreatedByID),
		ArchivedAt:     timestampToPtr(t.ArchivedAt),
		LastMessageAt:  timestampToString(t.LastMessageAt),
		CreatedAt:      timestampToString(t.CreatedAt),
	}
}

func messageToWire(m db.ChatMessage) MessageWire {
	return MessageWire{
		ID:         uuidToString(m.ID),
		TopicID:    uuidToString(m.TopicID),
		SenderType: textGet(m.SenderType),
		SenderID:   uuidToString(m.SenderID),
		Content:    m.Content,
		TaskID:     uuidToPtr(m.TaskID),
		CreatedAt:  timestampToString(m.CreatedAt),
	}
}

func textGet(t pgtype.Text) string {
	if t.Valid {
		return t.String
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────
// Pending-task surface — v2 sibling of GetPendingChatTask /
// ListPendingChatTasks (chat.go). Drives the FAB's running indicator
// and per-topic "agent is working" UI when the popover is closed and
// no individual topic's message query is subscribed.
// ─────────────────────────────────────────────────────────────────────

type PendingTopicTaskResponse struct {
	TaskID string `json:"task_id,omitempty"`
	Status string `json:"status,omitempty"`
}

type PendingTopicTaskItem struct {
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	TopicID string `json:"topic_id"`
}

type PendingTopicTasksResponse struct {
	Tasks []PendingTopicTaskItem `json:"tasks"`
}

// GetPendingTopicTask returns the most recent in-flight task for a topic.
// 404 when the topic doesn't exist in the caller's workspace; empty
// {task_id,status} when no task is pending (200 OK so the frontend can
// distinguish "no task" from "lookup failed").
func (h *Handler) GetPendingTopicTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	topicID := chi.URLParam(r, "topicId")

	// Workspace ACL — ensure the topic belongs to a conversation the
	// caller participates in. Reuse GetTopic + GetConversationWorkspaceID
	// rather than crafting a custom join; cheap and keeps the policy
	// localised in chat_v2.go.
	tid, ok := parseUUIDOrBadRequest(w, topicID, "topicId")
	if !ok {
		return
	}
	topic, err := h.Queries.GetTopic(r.Context(), tid)
	if err != nil {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	wsUUID, err := h.Queries.GetConversationWorkspaceID(r.Context(), topic.ConversationID)
	if err != nil || uuidToString(wsUUID) != workspaceID {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	// Confirm caller is a member participant — the FAB asks for pending
	// state on every reopen, don't leak across workspaces or to non-members.
	ok, err = h.Queries.IsActiveParticipant(r.Context(), db.IsActiveParticipantParams{
		ConversationID: topic.ConversationID,
		PrincipalType:  "member",
		PrincipalID:    parseUUID(userID),
	})
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}

	row, err := h.Queries.ListPendingTopicTaskByTopic(r.Context(), tid)
	if err != nil {
		// pgx returns ErrNoRows when no in-flight task — that's the common
		// path, not an error. Surface empty payload.
		writeJSON(w, http.StatusOK, PendingTopicTaskResponse{})
		return
	}
	writeJSON(w, http.StatusOK, PendingTopicTaskResponse{
		TaskID: uuidToString(row.ID),
		Status: row.Status,
	})
}

// ListPendingTopicTasks aggregates in-flight topic tasks across every
// conversation the caller participates in. Drives the FAB's running
// indicator when the popover is closed.
func (h *Handler) ListPendingTopicTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	rows, err := h.Queries.ListPendingTopicTasksByCreator(r.Context(), db.ListPendingTopicTasksByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		PrincipalID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending topic tasks")
		return
	}

	items := make([]PendingTopicTaskItem, len(rows))
	for i, row := range rows {
		items[i] = PendingTopicTaskItem{
			TaskID:  uuidToString(row.TaskID),
			Status:  row.Status,
			TopicID: uuidToString(row.TopicID),
		}
	}
	writeJSON(w, http.StatusOK, PendingTopicTasksResponse{Tasks: items})
}

// (no extra helpers — pending-task handlers reuse the existing
// IsActiveParticipant query for ACL.)

// ─────────────────────────────────────────────────────────────────────
// M5 read markers — per-topic unread counts.
// Drives the FAB aggregate badge AND the per-topic dot in
// SessionDropdown. One query covers both UIs.
// ─────────────────────────────────────────────────────────────────────

type UnreadCountItem struct {
	TopicID        string `json:"topic_id"`
	ConversationID string `json:"conversation_id"`
	UnreadCount    int64  `json:"unread_count"`
}

type UnreadCountsResponse struct {
	Counts []UnreadCountItem `json:"counts"`
}

// ListUnreadCounts returns one row per topic the caller has unread
// messages in, across every conversation they're an active member of
// in this workspace. Topics with zero unread are omitted (HAVING
// clause in the SQL) — the frontend defaults missing topics to 0.
func (h *Handler) ListUnreadCounts(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	rows, err := h.Queries.ListUnreadCountsByTopicForMember(r.Context(), db.ListUnreadCountsByTopicForMemberParams{
		WorkspaceID: parseUUID(workspaceID),
		PrincipalID: parseUUID(userID),
	})
	if err != nil {
		slog.Error("ListUnreadCounts: query failed",
			"user", userID, "workspace", workspaceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list unread counts")
		return
	}

	items := make([]UnreadCountItem, len(rows))
	for i, row := range rows {
		items[i] = UnreadCountItem{
			TopicID:        uuidToString(row.TopicID),
			ConversationID: uuidToString(row.ConversationID),
			UnreadCount:    row.UnreadCount,
		}
	}
	if len(items) > 0 {
		slog.Info("unread counts served",
			"user", userID, "topics_with_unread", len(items))
	}
	writeJSON(w, http.StatusOK, UnreadCountsResponse{Counts: items})
}
