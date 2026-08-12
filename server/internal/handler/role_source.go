package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type RoleSourceControlPlane interface {
	RegisterSource(context.Context, rolesource.RegisterSourceInput) (db.RoleSource, error)
	RequestScan(context.Context, string, string, string) (db.RoleSourceScanRequest, error)
	ListSources(context.Context, string) ([]db.RoleSource, error)
	GetSource(context.Context, string, string) (db.RoleSource, error)
	GetScan(context.Context, string, string, string) (db.RoleSourceScanRequest, error)
	ClaimNextScan(context.Context, string, time.Duration) (rolesource.ClaimedScan, error)
	ReportScanSuccess(context.Context, rolesource.ReportScanSuccessInput) (db.RoleSourceSnapshot, error)
	ReportScanFailure(context.Context, rolesource.ReportScanFailureInput) (db.RoleSourceScanRequest, error)
}

type roleSourceResponse struct {
	ID                    string                   `json:"id"`
	WorkspaceID           string                   `json:"workspace_id"`
	RuntimeID             string                   `json:"runtime_id"`
	Name                  string                   `json:"name"`
	Kind                  rolesource.Kind          `json:"kind"`
	AdapterVersion        string                   `json:"adapter_version"`
	ConfigSummary         rolesource.ConfigSummary `json:"config_summary"`
	Policy                json.RawMessage          `json:"policy"`
	State                 string                   `json:"state"`
	CurrentSnapshotDigest *string                  `json:"current_snapshot_digest"`
	Version               int64                    `json:"version"`
	CreatedAt             string                   `json:"created_at"`
	UpdatedAt             string                   `json:"updated_at"`
}

type roleSourceScanResponse struct {
	ID                     string  `json:"id"`
	SourceID               string  `json:"source_id"`
	WorkspaceID            string  `json:"workspace_id"`
	Status                 string  `json:"status"`
	ExpectedAdapterVersion string  `json:"expected_adapter_version"`
	SnapshotDigest         *string `json:"snapshot_digest"`
	ErrorCode              *string `json:"error_code"`
	RequestedAt            string  `json:"requested_at"`
	ClaimedAt              *string `json:"claimed_at"`
	CompletedAt            *string `json:"completed_at"`
}

func (h *Handler) roleSourceFeatureEnabled(r *http.Request, workspaceID, key string) bool {
	ctx := featureflag.WithEvalContext(r.Context(), featureflag.EvalContext{
		UserID: requestUserID(r), WorkspaceID: workspaceID,
	})
	return h.FeatureFlags.IsEnabled(ctx, rolesource.FeatureFlagRoleSourceSync, false) &&
		h.FeatureFlags.IsEnabled(ctx, key, false)
}

func (h *Handler) roleSourceDaemonScanEnabled(ctx context.Context, workspaceID string) bool {
	if h.RoleSources == nil || h.RoleSourceCatalog == nil {
		return false
	}
	ctx = featureflag.WithEvalContext(ctx, featureflag.EvalContext{WorkspaceID: workspaceID})
	return h.FeatureFlags.IsEnabled(ctx, rolesource.FeatureFlagRoleSourceSync, false) &&
		h.FeatureFlags.IsEnabled(ctx, rolesource.FeatureFlagRoleSourceScan, false)
}

func roleSourcePendingScanToProtocol(claimed rolesource.ClaimedScan) *protocol.DaemonHeartbeatPendingRoleSourceScan {
	return &protocol.DaemonHeartbeatPendingRoleSourceScan{
		RequestID: util.UUIDToString(claimed.Request.ID), SourceID: util.UUIDToString(claimed.Request.SourceID),
		WorkspaceID: util.UUIDToString(claimed.Request.WorkspaceID), Kind: claimed.Source.Kind,
		AdapterVersion: claimed.Request.ExpectedAdapterVersion, DaemonConfigID: claimed.Source.DaemonConfigID,
		LeaseToken: util.UUIDToString(claimed.Request.LeaseToken), LeaseExpiresAt: util.TimestampToString(claimed.Request.LeaseExpiresAt),
	}
}

// populateRoleSourceHeartbeat negotiates scan support and, only when the
// daemon explicitly asks to poll, claims one durable request. Separating
// support from polling keeps the 15-second heartbeat hot path free of an
// empty PostgreSQL claim at fleet scale; push hints request an immediate poll
// and the daemon performs a slower recovery poll when hints are missed.
func (h *Handler) populateRoleSourceHeartbeat(ctx context.Context, ack *protocol.DaemonHeartbeatAckPayload, runtimeID, workspaceID string, supports, poll bool) {
	if !supports || !h.roleSourceDaemonScanEnabled(ctx, workspaceID) {
		return
	}
	ack.ServerCapabilities = append(ack.ServerCapabilities, protocol.DaemonCapabilityRoleSourceScanV1)
	if !poll {
		return
	}
	claimed, err := h.RoleSources.ClaimNextScan(ctx, runtimeID, 2*time.Minute)
	switch {
	case err == nil:
		ack.PendingRoleSourceScan = roleSourcePendingScanToProtocol(claimed)
	case errors.Is(err, pgx.ErrNoRows):
		// Empty queue is the normal recovery-poll path.
	default:
		// Optional scan work must not make a healthy daemon appear offline.
		slog.Warn("role source scan claim failed", "runtime_id", runtimeID, "error", err)
	}
}

func (h *Handler) requireRoleSourceFeature(w http.ResponseWriter, r *http.Request, workspaceID, key string) bool {
	if h.RoleSources == nil || h.RoleSourceCatalog == nil || !h.roleSourceFeatureEnabled(r, workspaceID, key) {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

func (h *Handler) ListRoleSourceAdapters(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"adapters": h.RoleSourceCatalog.Descriptors()})
}

func (h *Handler) ListRoleSources(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	rows, err := h.RoleSources.ListSources(r.Context(), workspaceID)
	if err != nil {
		slog.Warn("list role sources failed", "workspace_id", workspaceID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list role sources")
		return
	}
	items := make([]roleSourceResponse, 0, len(rows))
	for _, row := range rows {
		item, err := roleSourceToResponse(row)
		if err != nil {
			slog.Error("stored role source safe summary is invalid", "source_id", util.UUIDToString(row.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list role sources")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": items})
}

func (h *Handler) CreateRoleSource(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot register role sources")
		return
	}
	var request struct {
		RuntimeID      string                   `json:"runtime_id"`
		Name           string                   `json:"name"`
		Kind           rolesource.Kind          `json:"kind"`
		AdapterVersion string                   `json:"adapter_version"`
		DaemonConfigID string                   `json:"daemon_config_id"`
		ConfigSummary  rolesource.ConfigSummary `json:"config_summary"`
		Policy         json.RawMessage          `json:"policy"`
	}
	if err := decodeStrictRoleSourceJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.RoleSources.RegisterSource(r.Context(), rolesource.RegisterSourceInput{
		WorkspaceID: workspaceID, RuntimeID: request.RuntimeID, ActorUserID: userID,
		Name: request.Name, Kind: request.Kind, AdapterVersion: request.AdapterVersion,
		DaemonConfigID: request.DaemonConfigID, ConfigSummary: request.ConfigSummary, Policy: request.Policy,
	})
	if err != nil {
		switch {
		case isUniqueViolation(err):
			writeError(w, http.StatusConflict, "a role source with this name already exists")
		case errors.Is(err, rolesource.ErrAdapterNotFound):
			writeError(w, http.StatusBadRequest, "unsupported role source adapter")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusBadRequest, "runtime is not in this workspace")
		default:
			writeError(w, http.StatusBadRequest, "invalid role source configuration")
		}
		return
	}
	response, err := roleSourceToResponse(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode role source")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) RequestRoleSourceScan(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot request role source scans")
		return
	}
	row, err := h.RoleSources.RequestScan(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), userID)
	if err != nil {
		switch {
		case errors.Is(err, rolesource.ErrScanAlreadyActive):
			writeError(w, http.StatusConflict, "a scan is already active for this source")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "role source not found")
		default:
			writeError(w, http.StatusInternalServerError, "failed to request scan")
		}
		return
	}
	if source, sourceErr := h.RoleSources.GetSource(r.Context(), workspaceID, chi.URLParam(r, "sourceId")); sourceErr != nil {
		slog.Warn("role source scan queued but runtime wakeup lookup failed", "source_id", chi.URLParam(r, "sourceId"), "error", sourceErr)
	} else {
		h.requestDaemonPendingWork(util.UUIDToString(source.RuntimeID), protocol.PendingWorkKindRoleSourceScan)
	}
	writeJSON(w, http.StatusAccepted, roleSourceScanToResponse(row))
}

func (h *Handler) GetRoleSourceScan(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	row, err := h.RoleSources.GetScan(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), chi.URLParam(r, "scanId"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "scan not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load scan")
		return
	}
	writeJSON(w, http.StatusOK, roleSourceScanToResponse(row))
}

// ReportRoleSourceScanResult receives the terminal result for a leased scan.
// It is daemon-authenticated, workspace-scoped through the runtime row and
// retry-safe when the same terminal report is delivered more than once.
func (h *Handler) ReportRoleSourceScanResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	workspaceID := util.UUIDToString(runtime.WorkspaceID)
	if !h.roleSourceDaemonScanEnabled(r.Context(), workspaceID) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	requestID, sourceID := chi.URLParam(r, "requestId"), chi.URLParam(r, "sourceId")
	if _, err := util.ParseUUID(requestID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request_id")
		return
	}
	if _, err := util.ParseUUID(sourceID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid source_id")
		return
	}
	var body struct {
		Status     string               `json:"status"`
		LeaseToken string               `json:"lease_token"`
		Snapshot   *rolesource.Snapshot `json:"snapshot,omitempty"`
		ErrorCode  string               `json:"error_code,omitempty"`
	}
	if err := decodeStrictRoleSourceJSONLimit(w, r, &body, 16<<20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, err := util.ParseUUID(body.LeaseToken); err != nil {
		writeError(w, http.StatusBadRequest, "invalid lease_token")
		return
	}
	var err error
	switch body.Status {
	case "completed":
		if body.Snapshot == nil || body.ErrorCode != "" {
			writeError(w, http.StatusBadRequest, "completed result requires only snapshot")
			return
		}
		_, err = h.RoleSources.ReportScanSuccess(r.Context(), rolesource.ReportScanSuccessInput{
			WorkspaceID: workspaceID, SourceID: sourceID, RequestID: requestID,
			RuntimeID: runtimeID, LeaseToken: body.LeaseToken, Snapshot: *body.Snapshot,
		})
	case "failed":
		if body.Snapshot != nil || body.ErrorCode == "" {
			writeError(w, http.StatusBadRequest, "failed result requires only error_code")
			return
		}
		_, err = h.RoleSources.ReportScanFailure(r.Context(), rolesource.ReportScanFailureInput{
			WorkspaceID: workspaceID, SourceID: sourceID, RequestID: requestID,
			RuntimeID: runtimeID, LeaseToken: body.LeaseToken, ErrorCode: body.ErrorCode,
		})
	default:
		writeError(w, http.StatusBadRequest, "status must be completed or failed")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, rolesource.ErrScanLeaseLost):
			writeError(w, http.StatusConflict, "scan lease is stale or no longer owned")
		case errors.Is(err, rolesource.ErrInvalidScanReport):
			writeError(w, http.StatusBadRequest, "invalid scan result")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "scan not found")
		default:
			slog.Error("persist role source scan result failed", "runtime_id", runtimeID, "request_id", requestID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to persist scan result")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func decodeStrictRoleSourceJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return decodeStrictRoleSourceJSONLimit(w, r, destination, 128<<10)
}

func decodeStrictRoleSourceJSONLimit(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing JSON")
	}
	return nil
}

func roleSourceToResponse(row db.RoleSource) (roleSourceResponse, error) {
	var summary rolesource.ConfigSummary
	if err := json.Unmarshal(row.ConfigRedacted, &summary); err != nil {
		return roleSourceResponse{}, err
	}
	policy := append(json.RawMessage(nil), row.Policy...)
	return roleSourceResponse{
		ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), RuntimeID: util.UUIDToString(row.RuntimeID),
		Name: row.Name, Kind: rolesource.Kind(row.Kind), AdapterVersion: row.AdapterVersion,
		ConfigSummary: summary, Policy: policy, State: row.State,
		CurrentSnapshotDigest: util.TextToPtr(row.CurrentSnapshotDigest), Version: row.Version,
		CreatedAt: util.TimestampToString(row.CreatedAt), UpdatedAt: util.TimestampToString(row.UpdatedAt),
	}, nil
}

func roleSourceScanToResponse(row db.RoleSourceScanRequest) roleSourceScanResponse {
	return roleSourceScanResponse{
		ID: util.UUIDToString(row.ID), SourceID: util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Status: row.Status, ExpectedAdapterVersion: row.ExpectedAdapterVersion,
		SnapshotDigest: util.TextToPtr(row.SnapshotDigest), ErrorCode: util.TextToPtr(row.ErrorCode),
		RequestedAt: util.TimestampToString(row.RequestedAt), ClaimedAt: util.TimestampToPtr(row.ClaimedAt), CompletedAt: util.TimestampToPtr(row.CompletedAt),
	}
}
