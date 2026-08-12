package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
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
	RenewScanLease(context.Context, string, string, string, string, string, time.Duration) (db.RoleSourceScanRequest, error)
	ReportScanSuccess(context.Context, rolesource.ReportScanSuccessInput) (db.RoleSourceSnapshot, error)
	ReportScanFailure(context.Context, rolesource.ReportScanFailureInput) (db.RoleSourceScanRequest, error)
	CreatePlan(context.Context, rolesource.CreatePlanInput) (db.RoleSourcePlan, error)
	RecordPlanApproval(context.Context, rolesource.RecordPlanApprovalInput) (db.RoleSourcePlanApproval, error)
	ApplyPlan(context.Context, rolesource.ApplyPlanInput) (db.RoleSourceApply, rolesource.ApplyReceipt, error)
	GetPlan(context.Context, string, string, string) (db.RoleSourcePlan, error)
	ListPlans(context.Context, string, string, int32) ([]db.RoleSourcePlan, error)
	ListSnapshots(context.Context, string, string, int32) ([]db.RoleSourceSnapshot, error)
	ListPlanApprovals(context.Context, string, string, string, int32) ([]db.RoleSourcePlanApproval, error)
	ListMissingArtifacts(context.Context, rolesource.ArtifactLeaseInput, []rolesource.ArtifactRef) ([]rolesource.ArtifactRef, error)
	StoreArtifactRecord(context.Context, rolesource.StoreArtifactInput) (db.RoleSourceArtifact, bool, error)
}

const maxRoleSourceArtifactUploadBytes = 8 << 20

var roleSourceArtifactSpoolSlots = make(chan struct{}, 16)

var errRoleSourceArtifactMismatch = errors.New("artifact body does not match declared digest and size")

type roleSourceArtifactStreamStorage interface {
	UploadStream(context.Context, string, io.Reader, int64, string, string) (string, error)
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

type roleSourceSnapshotResponse struct {
	SourceID            string              `json:"source_id"`
	WorkspaceID         string              `json:"workspace_id"`
	Snapshot            rolesource.Snapshot `json:"snapshot"`
	ReportedByRuntimeID string              `json:"reported_by_runtime_id"`
	CreatedAt           string              `json:"created_at"`
}

type roleSourcePlanResponse struct {
	SourceID    string          `json:"source_id"`
	WorkspaceID string          `json:"workspace_id"`
	Plan        rolesource.Plan `json:"plan"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   string          `json:"created_at"`
}

type roleSourceApprovalResponse struct {
	ID          string                        `json:"id"`
	SourceID    string                        `json:"source_id"`
	WorkspaceID string                        `json:"workspace_id"`
	PlanDigest  string                        `json:"plan_digest"`
	Decision    string                        `json:"decision"`
	Decisions   *rolesource.ApprovalDecisions `json:"decisions,omitempty"`
	ActorUserID string                        `json:"actor_user_id"`
	CreatedAt   string                        `json:"created_at"`
}

type roleSourceApplyResponse struct {
	ID          string                  `json:"id"`
	SourceID    string                  `json:"source_id"`
	WorkspaceID string                  `json:"workspace_id"`
	Status      string                  `json:"status"`
	Receipt     rolesource.ApplyReceipt `json:"receipt"`
	CompletedAt *string                 `json:"completed_at"`
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
	previousSnapshotDigest := ""
	if claimed.Source.CurrentSnapshotDigest.Valid {
		previousSnapshotDigest = claimed.Source.CurrentSnapshotDigest.String
	}
	return &protocol.DaemonHeartbeatPendingRoleSourceScan{
		RequestID: util.UUIDToString(claimed.Request.ID), SourceID: util.UUIDToString(claimed.Request.SourceID),
		WorkspaceID: util.UUIDToString(claimed.Request.WorkspaceID), Kind: claimed.Source.Kind,
		AdapterVersion: claimed.Request.ExpectedAdapterVersion, DaemonConfigID: claimed.Source.DaemonConfigID,
		LeaseToken: util.UUIDToString(claimed.Request.LeaseToken), LeaseExpiresAt: util.TimestampToString(claimed.Request.LeaseExpiresAt),
		PreviousSnapshotDigest: previousSnapshotDigest,
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
	claimed, err := h.RoleSources.ClaimNextScan(ctx, runtimeID, 15*time.Minute)
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

func (h *Handler) ListRoleSourceSnapshots(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	rows, err := h.RoleSources.ListSnapshots(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), 50)
	if err != nil {
		writeRoleSourceReadError(w, err, "failed to list snapshots")
		return
	}
	items := make([]roleSourceSnapshotResponse, 0, len(rows))
	for _, row := range rows {
		item, err := roleSourceSnapshotToResponse(row)
		if err != nil {
			slog.Error("stored role source snapshot is invalid", "snapshot_digest", row.SnapshotDigest, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list snapshots")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": items})
}

func (h *Handler) ListRoleSourcePlans(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	rows, err := h.RoleSources.ListPlans(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), 50)
	if err != nil {
		writeRoleSourceReadError(w, err, "failed to list plans")
		return
	}
	items := make([]roleSourcePlanResponse, 0, len(rows))
	for _, row := range rows {
		item, err := roleSourcePlanToResponse(row)
		if err != nil {
			slog.Error("stored role source plan is invalid", "plan_digest", row.PlanDigest, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list plans")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": items})
}

func (h *Handler) GetRoleSourcePlan(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	row, err := h.RoleSources.GetPlan(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), chi.URLParam(r, "planDigest"))
	if err != nil {
		writeRoleSourceReadError(w, err, "failed to load plan")
		return
	}
	response, err := roleSourcePlanToResponse(row)
	if err != nil {
		slog.Error("stored role source plan is invalid", "plan_digest", row.PlanDigest, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) CreateRoleSourcePlan(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot create role source plans")
		return
	}
	var body struct {
		TargetSnapshotDigest string `json:"target_snapshot_digest"`
	}
	if err := decodeStrictRoleSourceJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.RoleSources.CreatePlan(r.Context(), rolesource.CreatePlanInput{
		WorkspaceID: workspaceID, SourceID: chi.URLParam(r, "sourceId"),
		TargetSnapshotDigest: body.TargetSnapshotDigest, ActorUserID: userID,
	})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "role source or snapshot not found")
		case errors.Is(err, rolesource.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "plan identity conflicts with persisted content")
		case errors.Is(err, rolesource.ErrInvalidPlanRequest):
			writeError(w, http.StatusBadRequest, "cannot create plan")
		default:
			slog.Error("create role source plan failed", "workspace_id", workspaceID, "source_id", chi.URLParam(r, "sourceId"), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create plan")
		}
		return
	}
	response, err := roleSourcePlanToResponse(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode plan")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) RecordRoleSourcePlanApproval(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot approve role source plans")
		return
	}
	var body struct {
		RequestKey string                        `json:"request_key"`
		Decision   string                        `json:"decision"`
		Decisions  *rolesource.ApprovalDecisions `json:"decisions,omitempty"`
	}
	if err := decodeStrictRoleSourceJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.RoleSources.RecordPlanApproval(r.Context(), rolesource.RecordPlanApprovalInput{
		WorkspaceID: workspaceID, SourceID: chi.URLParam(r, "sourceId"), PlanDigest: chi.URLParam(r, "planDigest"),
		RequestKey: body.RequestKey, Decision: body.Decision, Decisions: body.Decisions, ActorUserID: userID,
	})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "role source or plan not found")
		case errors.Is(err, rolesource.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "request_key was already used for another approval")
		case errors.Is(err, rolesource.ErrInvalidApprovalRequest):
			writeError(w, http.StatusBadRequest, "cannot record plan approval")
		default:
			slog.Error("record role source approval failed", "workspace_id", workspaceID, "source_id", chi.URLParam(r, "sourceId"), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to record plan approval")
		}
		return
	}
	response, err := roleSourceApprovalToResponse(row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode approval")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) ApplyRoleSourcePlan(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceApply) {
		return
	}
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents cannot apply role source plans")
		return
	}
	var body struct {
		RequestKey string `json:"request_key"`
		ApprovalID string `json:"approval_id"`
	}
	if err := decodeStrictRoleSourceJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, receipt, err := h.RoleSources.ApplyPlan(r.Context(), rolesource.ApplyPlanInput{
		WorkspaceID: workspaceID, SourceID: chi.URLParam(r, "sourceId"), PlanDigest: chi.URLParam(r, "planDigest"),
		ApprovalID: body.ApprovalID, RequestKey: body.RequestKey, ActorUserID: userID,
	})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "role source, plan, approval, snapshot or target not found")
		case errors.Is(err, rolesource.ErrInvalidApplyRequest):
			writeError(w, http.StatusBadRequest, "cannot apply role source plan")
		case errors.Is(err, rolesource.ErrIdempotencyConflict), errors.Is(err, rolesource.ErrApplyConflict):
			writeError(w, http.StatusConflict, "role source apply conflicts with persisted or current state")
		case errors.Is(err, rolesource.ErrMaterializationBlocked):
			writeError(w, http.StatusUnprocessableEntity, "snapshot uses fields that are not safely materializable")
		case errors.Is(err, rolesource.ErrMaterializationOverload):
			writeError(w, http.StatusTooManyRequests, "role source apply capacity is exhausted")
		default:
			slog.Error("apply role source plan failed", "workspace_id", workspaceID, "source_id", chi.URLParam(r, "sourceId"), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to apply role source plan")
		}
		return
	}
	writeJSON(w, http.StatusOK, roleSourceApplyResponse{
		ID: util.UUIDToString(row.ID), SourceID: util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Status: row.Status, Receipt: receipt, CompletedAt: util.TimestampToPtr(row.CompletedAt),
	})
}

func (h *Handler) ListRoleSourcePlanApprovals(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if !h.requireRoleSourceFeature(w, r, workspaceID, rolesource.FeatureFlagRoleSourceScan) {
		return
	}
	rows, err := h.RoleSources.ListPlanApprovals(r.Context(), workspaceID, chi.URLParam(r, "sourceId"), chi.URLParam(r, "planDigest"), 50)
	if err != nil {
		writeRoleSourceReadError(w, err, "failed to list approvals")
		return
	}
	items := make([]roleSourceApprovalResponse, 0, len(rows))
	for _, row := range rows {
		item, err := roleSourceApprovalToResponse(row)
		if err != nil {
			slog.Error("stored role source approval is invalid", "approval_id", util.UUIDToString(row.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list approvals")
			return
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": items})
}

func writeRoleSourceReadError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, message)
}

func (h *Handler) CheckRoleSourceArtifacts(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		LeaseToken string                   `json:"lease_token"`
		Artifacts  []rolesource.ArtifactRef `json:"artifacts"`
	}
	if err := decodeStrictRoleSourceJSONLimit(w, r, &body, 2<<20); err != nil || len(body.Artifacts) > 1_000 {
		writeError(w, http.StatusBadRequest, "invalid artifact preflight")
		return
	}
	missing, err := h.RoleSources.ListMissingArtifacts(r.Context(), rolesource.ArtifactLeaseInput{
		WorkspaceID: workspaceID, SourceID: chi.URLParam(r, "sourceId"), RequestID: chi.URLParam(r, "requestId"),
		RuntimeID: runtimeID, LeaseToken: body.LeaseToken,
	}, body.Artifacts)
	if err != nil {
		writeRoleSourceArtifactLeaseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"missing": missing})
}

func (h *Handler) UploadRoleSourceArtifact(w http.ResponseWriter, r *http.Request) {
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
	streamStore, ok := h.Storage.(roleSourceArtifactStreamStorage)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "artifact storage unavailable")
		return
	}
	digest := chi.URLParam(r, "artifactDigest")
	leaseToken := strings.TrimSpace(r.Header.Get("X-Role-Source-Lease-Token"))
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 || leaseToken == "" ||
		r.ContentLength < 0 || r.ContentLength > maxRoleSourceArtifactUploadBytes {
		writeError(w, http.StatusBadRequest, "invalid artifact upload metadata")
		return
	}
	lease := rolesource.ArtifactLeaseInput{
		WorkspaceID: workspaceID, SourceID: chi.URLParam(r, "sourceId"), RequestID: chi.URLParam(r, "requestId"),
		RuntimeID: runtimeID, LeaseToken: leaseToken,
	}
	probe := rolesource.ArtifactRef{Digest: digest, Path: "artifact", MediaType: "application/octet-stream", SizeBytes: r.ContentLength}
	missing, err := h.RoleSources.ListMissingArtifacts(r.Context(), lease, []rolesource.ArtifactRef{probe})
	if err != nil {
		writeRoleSourceArtifactLeaseError(w, err)
		return
	}
	if len(missing) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_present"})
		return
	}
	select {
	case roleSourceArtifactSpoolSlots <- struct{}{}:
		defer func() { <-roleSourceArtifactSpoolSlots }()
	case <-r.Context().Done():
		writeError(w, http.StatusRequestTimeout, "artifact upload cancelled")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRoleSourceArtifactUploadBytes)
	temporary, written, err := spoolRoleSourceArtifact(r.Body, r.ContentLength, digest)
	if err != nil {
		if errors.Is(err, errRoleSourceArtifactMismatch) {
			writeError(w, http.StatusBadRequest, "artifact body does not match declared digest and size")
		} else {
			slog.Error("spool role source artifact failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to receive artifact")
		}
		return
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName) //nolint:errcheck
	defer temporary.Close()        //nolint:errcheck
	storageKey := fmt.Sprintf("role-source-artifacts/%s/%s", workspaceID, strings.TrimPrefix(digest, "sha256:"))
	var uploadErr error
	if written == 0 {
		_, uploadErr = h.Storage.Upload(r.Context(), storageKey, []byte{}, "application/octet-stream", "")
	} else {
		_, uploadErr = streamStore.UploadStream(r.Context(), storageKey, temporary, written, "application/octet-stream", "")
	}
	if uploadErr != nil {
		slog.Error("store role source artifact failed", "workspace_id", workspaceID, "digest", digest, "error", uploadErr)
		writeError(w, http.StatusServiceUnavailable, "failed to store artifact")
		return
	}
	_, created, err := h.RoleSources.StoreArtifactRecord(r.Context(), rolesource.StoreArtifactInput{
		ArtifactLeaseInput: lease, Digest: digest, SizeBytes: written, StorageKey: storageKey,
	})
	if err != nil {
		writeRoleSourceArtifactLeaseError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]string{"status": "ready"})
}

func spoolRoleSourceArtifact(body io.Reader, declaredSize int64, declaredDigest string) (*os.File, int64, error) {
	if declaredSize < 0 || declaredSize > maxRoleSourceArtifactUploadBytes {
		return nil, 0, errRoleSourceArtifactMismatch
	}
	temporary, err := os.CreateTemp("", "multica-role-source-artifact-*")
	if err != nil {
		return nil, 0, err
	}
	cleanup := func() {
		name := temporary.Name()
		temporary.Close() //nolint:errcheck
		os.Remove(name)   //nolint:errcheck
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(body, declaredSize+1))
	if err != nil {
		cleanup()
		return nil, written, err
	}
	if written != declaredSize || "sha256:"+fmt.Sprintf("%x", hasher.Sum(nil)) != declaredDigest {
		cleanup()
		return nil, written, errRoleSourceArtifactMismatch
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, written, err
	}
	return temporary, written, nil
}

func writeRoleSourceArtifactLeaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rolesource.ErrScanLeaseLost):
		writeError(w, http.StatusConflict, "scan lease is stale or no longer owned")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "scan not found")
	case errors.Is(err, rolesource.ErrInvalidArtifactRequest):
		writeError(w, http.StatusBadRequest, "invalid artifact request")
	default:
		slog.Error("role source artifact control-plane request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "artifact control plane failed")
	}
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

func (h *Handler) RenewRoleSourceScanLease(w http.ResponseWriter, r *http.Request) {
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
		LeaseToken string `json:"lease_token"`
	}
	if err := decodeStrictRoleSourceJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, err := util.ParseUUID(body.LeaseToken); err != nil {
		writeError(w, http.StatusBadRequest, "invalid lease_token")
		return
	}
	row, err := h.RoleSources.RenewScanLease(r.Context(), workspaceID, sourceID, requestID, runtimeID, body.LeaseToken, 15*time.Minute)
	if err != nil {
		switch {
		case errors.Is(err, rolesource.ErrScanLeaseLost):
			writeError(w, http.StatusConflict, "scan lease is stale or no longer owned")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "scan not found")
		default:
			slog.Error("renew role source scan lease failed", "runtime_id", runtimeID, "request_id", requestID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to renew scan lease")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"lease_expires_at": util.TimestampToString(row.LeaseExpiresAt)})
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

func roleSourceSnapshotToResponse(row db.RoleSourceSnapshot) (roleSourceSnapshotResponse, error) {
	snapshot, err := rolesource.DecodePersistedSnapshot(row)
	if err != nil {
		return roleSourceSnapshotResponse{}, err
	}
	return roleSourceSnapshotResponse{
		SourceID: util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Snapshot: snapshot, ReportedByRuntimeID: util.UUIDToString(row.ReportedByRuntimeID),
		CreatedAt: util.TimestampToString(row.CreatedAt),
	}, nil
}

func roleSourcePlanToResponse(row db.RoleSourcePlan) (roleSourcePlanResponse, error) {
	plan, err := rolesource.DecodePersistedPlan(row)
	if err != nil {
		return roleSourcePlanResponse{}, err
	}
	return roleSourcePlanResponse{
		SourceID: util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Plan: plan, CreatedBy: util.UUIDToString(row.CreatedBy), CreatedAt: util.TimestampToString(row.CreatedAt),
	}, nil
}

func roleSourceApprovalToResponse(row db.RoleSourcePlanApproval) (roleSourceApprovalResponse, error) {
	var decisions *rolesource.ApprovalDecisions
	if row.Decision == "approved" {
		var decoded rolesource.ApprovalDecisions
		if err := json.Unmarshal(row.Decisions, &decoded); err != nil {
			return roleSourceApprovalResponse{}, err
		}
		if decoded.ContractVersion != rolesource.PlanContractVersion {
			return roleSourceApprovalResponse{}, errors.New("stored approval has invalid contract version")
		}
		rolesource.CanonicalizeApprovalDecisions(&decoded)
		decisions = &decoded
	}
	return roleSourceApprovalResponse{
		ID: util.UUIDToString(row.ID), SourceID: util.UUIDToString(row.SourceID), WorkspaceID: util.UUIDToString(row.WorkspaceID),
		PlanDigest: row.PlanDigest, Decision: row.Decision, Decisions: decisions,
		ActorUserID: util.UUIDToString(row.ActorUserID), CreatedAt: util.TimestampToString(row.CreatedAt),
	}, nil
}
