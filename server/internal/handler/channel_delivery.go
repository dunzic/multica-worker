package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/delivery"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type channelDeliveryResponse struct {
	ID                string             `json:"id"`
	WorkspaceID       string             `json:"workspace_id"`
	InstallationID    *string            `json:"installation_id"`
	TaskID            string             `json:"task_id"`
	ChatSessionID     string             `json:"chat_session_id"`
	ChannelType       string             `json:"channel_type"`
	ChannelChatID     string             `json:"channel_chat_id"`
	OperationKind     string             `json:"operation_kind"`
	CorrelationID     string             `json:"correlation_id"`
	PayloadDigest     string             `json:"payload_digest"`
	Status            string             `json:"status"`
	AttemptCount      int32              `json:"attempt_count"`
	ExternalMessageID *string            `json:"external_message_id"`
	EvidenceDigest    *string            `json:"evidence_digest"`
	Evidence          *delivery.Evidence `json:"evidence"`
	LastErrorCode     *string            `json:"last_error_code"`
	AmbiguousAt       *string            `json:"ambiguous_at"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
}

// ListChannelDeliveries returns only verified, content-free evidence. The
// ledger validates every terminal receipt before the handler serializes it, so
// a database-side mutation fails the whole audit view instead of being shown as
// trustworthy history.
func (h *Handler) ListChannelDeliveries(w http.ResponseWriter, r *http.Request) {
	if h.ChannelDeliveries == nil {
		writeError(w, http.StatusServiceUnavailable, "channel delivery ledger is not configured")
		return
	}
	workspaceID, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil || !workspaceID.Valid {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = int32(parsed)
	}
	rows, err := h.ChannelDeliveries.List(r.Context(), workspaceID, limit)
	if err != nil {
		slog.ErrorContext(r.Context(), "list channel deliveries failed", "workspace_id", chi.URLParam(r, "id"), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to validate channel delivery history")
		return
	}
	response := make([]channelDeliveryResponse, 0, len(rows))
	for _, row := range rows {
		item, err := channelDeliveryToResponse(row)
		if err != nil {
			slog.ErrorContext(r.Context(), "serialize channel delivery failed", "delivery_id", util.UUIDToString(row.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to validate channel delivery history")
			return
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": response})
}

func channelDeliveryToResponse(row db.ChannelDelivery) (channelDeliveryResponse, error) {
	response := channelDeliveryResponse{
		ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), TaskID: util.UUIDToString(row.TaskID),
		ChatSessionID: util.UUIDToString(row.ChatSessionID), ChannelType: row.ChannelType, ChannelChatID: row.ChannelChatID,
		OperationKind: row.OperationKind, CorrelationID: util.UUIDToString(row.CorrelationID), PayloadDigest: row.PayloadDigest,
		Status: row.Status, AttemptCount: row.AttemptCount, CreatedAt: row.CreatedAt.Time.UTC().Format(timeFormat),
		UpdatedAt: row.UpdatedAt.Time.UTC().Format(timeFormat),
	}
	if row.InstallationID.Valid {
		value := util.UUIDToString(row.InstallationID)
		response.InstallationID = &value
	}
	if row.ExternalMessageID.Valid {
		value := row.ExternalMessageID.String
		response.ExternalMessageID = &value
	}
	if row.EvidenceDigest.Valid {
		value := row.EvidenceDigest.String
		response.EvidenceDigest = &value
	}
	if row.LastErrorCode.Valid {
		value := row.LastErrorCode.String
		response.LastErrorCode = &value
	}
	if row.AmbiguousAt.Valid {
		value := row.AmbiguousAt.Time.UTC().Format(timeFormat)
		response.AmbiguousAt = &value
	}
	if row.Status == "delivered" || row.Status == "readback" || row.Status == "ambiguous" {
		evidence, err := delivery.ValidateRow(row)
		if err != nil {
			return channelDeliveryResponse{}, err
		}
		response.Evidence = &evidence
	}
	return response, nil
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"
