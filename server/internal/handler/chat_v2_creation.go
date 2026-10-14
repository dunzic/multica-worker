package handler

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
)

// validateConversationPrincipal prevents tenant-crossing and private-agent leaks.
func (h *Handler) validateConversationPrincipal(w http.ResponseWriter, r *http.Request, q *db.Queries, ws pgtype.UUID, caller string, p Principal) bool {
	id, ok := parseUUIDOrBadRequest(w, p.ID, "participant.id")
	if !ok {
		return false
	}
	switch p.Type {
	case "member":
		if _, err := q.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: id, WorkspaceID: ws}); err != nil {
			writeError(w, http.StatusNotFound, "member not found")
			return false
		}
	case "agent":
		a, err := q.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: ws})
		if err != nil || a.ArchivedAt.Valid || !h.canAccessPrivateAgent(r.Context(), a, "member", caller, uuidToString(ws)) {
			writeError(w, http.StatusNotFound, "agent not found")
			return false
		}
	default:
		writeError(w, http.StatusBadRequest, "participant.type must be member or agent")
		return false
	}
	return true
}

func (h *Handler) createConversationAtomic(w http.ResponseWriter, r *http.Request, workspaceID, userID, kind, title string, others []Principal) {
	ws, caller := parseUUID(workspaceID), parseUUID(userID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	for i, p := range others {
		if !h.validateConversationPrincipal(w, r, q, ws, userID, p) {
			return
		}
		// Canonicalize before self-checks and deduplication; casing must not
		// let a duplicate creator overwrite the sole admin role.
		others[i].ID = uuidToString(parseUUID(p.ID))
		if kind == "dm" && p.Type == "member" && parseUUID(others[i].ID) == caller {
			writeError(w, http.StatusBadRequest, "cannot create a dm with yourself")
			return
		}
	}
	var conv db.Conversation
	if kind == "dm" {
		other := others[0]
		sig := ComputeDMSignature("member", caller, other.Type, parseUUID(other.ID))
		if err := q.LockConversationDM(r.Context(), workspaceID+":"+sig); err != nil {
			writeError(w, http.StatusInternalServerError, "lock conversation failed")
			return
		}
		existing, err := q.GetDMConversation(r.Context(), db.GetDMConversationParams{WorkspaceID: ws, DmSignature: strToText(sig)})
		if err == nil {
			if err := tx.Commit(r.Context()); err != nil {
				writeError(w, http.StatusInternalServerError, "commit failed")
				return
			}
			writeJSON(w, http.StatusOK, h.hydrateConversation(r, existing))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "load dm failed")
			return
		}
		conv, err = q.CreateDMConversation(r.Context(), db.CreateDMConversationParams{WorkspaceID: ws, DmSignature: strToText(sig), CreatedBy: caller})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create dm failed")
			return
		}
	} else {
		conv, err = q.CreateGroupConversation(r.Context(), db.CreateGroupConversationParams{WorkspaceID: ws, Title: strToText(title), CreatedBy: caller})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "create group failed")
			return
		}
	}
	role := "member"
	if kind == "group" {
		role = "admin"
	}
	if _, err := q.AddParticipant(r.Context(), db.AddParticipantParams{ConversationID: conv.ID, PrincipalType: "member", PrincipalID: caller, Role: strToText(role)}); err != nil {
		writeError(w, http.StatusInternalServerError, "add creator failed")
		return
	}
	seen := map[string]bool{"member:" + userID: true}
	for _, p := range others {
		key := p.Type + ":" + p.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, err := q.AddParticipant(r.Context(), db.AddParticipantParams{ConversationID: conv.ID, PrincipalType: p.Type, PrincipalID: parseUUID(p.ID), Role: strToText("member")}); err != nil {
			writeError(w, http.StatusInternalServerError, "add participant failed")
			return
		}
	}
	if _, err := q.CreateTopic(r.Context(), db.CreateTopicParams{ConversationID: conv.ID, Title: strToText(""), CreatedByType: "member", CreatedByID: caller}); err != nil {
		writeError(w, http.StatusInternalServerError, "create topic failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "commit failed")
		return
	}
	h.publishConversationCreated(r.Context(), workspaceID, userID, conv)
	writeJSON(w, http.StatusCreated, h.hydrateConversation(r, conv))
}

func (h *Handler) requireConversationAdmin(w http.ResponseWriter, r *http.Request, conv db.Conversation, userID string) bool {
	if conv.Kind != "group" {
		return true
	}
	parts, err := h.Queries.ListConversationParticipants(r.Context(), db.ListConversationParticipantsParams{ConversationID: conv.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load participants failed")
		return false
	}
	for _, p := range parts {
		if p.PrincipalType == "member" && uuidToString(p.PrincipalID) == userID && !p.LeftAt.Valid && p.Role == "admin" {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "group admin required")
	return false
}
