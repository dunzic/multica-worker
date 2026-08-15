package rolesource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/runtimehealth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var (
	ErrSourceVersionConflict       = errors.New("role source version conflict")
	ErrInvalidLifecycleTransition  = errors.New("invalid role source lifecycle transition")
	ErrLifecycleRuntimeUnavailable = errors.New("role source runtime is unavailable")
	ErrLifecycleConfigNotLoaded    = errors.New("role source runtime configuration is not loaded")
)

type SourceLifecycleAction string

const (
	SourceLifecyclePause  SourceLifecycleAction = "pause"
	SourceLifecycleResume SourceLifecycleAction = "resume"
	SourceLifecycleDetach SourceLifecycleAction = "detach"
	SourceLifecycleRebind SourceLifecycleAction = "rebind"
)

type UpdateSourceLifecycleInput struct {
	WorkspaceID     string
	SourceID        string
	ActorUserID     string
	ExpectedVersion int64
	Action          SourceLifecycleAction
	RuntimeID       string
	DaemonConfigID  string
}

func validRoleSourceState(state string) bool {
	switch state {
	case "registered", "active", "paused", "error", "detached":
		return true
	default:
		return false
	}
}

func (c *ControlPlane) UpdateSourceLifecycle(ctx context.Context, input UpdateSourceLifecycleInput) (db.RoleSource, error) {
	if input.ExpectedVersion <= 0 {
		return db.RoleSource{}, fmt.Errorf("%w: expected_version must be positive", ErrInvalidLifecycleTransition)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSource{}, fmt.Errorf("%w: invalid identity", ErrInvalidLifecycleTransition)
	}
	if input.Action != SourceLifecycleRebind && (strings.TrimSpace(input.RuntimeID) != "" || strings.TrimSpace(input.DaemonConfigID) != "") {
		return db.RoleSource{}, fmt.Errorf("%w: runtime fields are accepted only for rebind", ErrInvalidLifecycleTransition)
	}

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSource{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSource{}, err
	}
	current, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSource{}, err
	}
	if current.Version != input.ExpectedVersion {
		return db.RoleSource{}, ErrSourceVersionConflict
	}

	var updated db.RoleSource
	var eventType string
	var cancelledScans, cancelledTransfers int
	previousRuntimeID := util.UUIDToString(current.RuntimeID)

	switch input.Action {
	case SourceLifecyclePause:
		if current.State != "registered" && current.State != "active" && current.State != "error" {
			return db.RoleSource{}, lifecycleTransitionError(current.State, input.Action)
		}
		cancelledScans, cancelledTransfers, err = cancelRoleSourceWork(ctx, qtx, current, "source_paused")
		if err != nil {
			return db.RoleSource{}, err
		}
		updated, err = qtx.UpdateRoleSourceState(ctx, db.UpdateRoleSourceStateParams{
			State: "paused", ActorUserID: actorID, ID: sourceID, WorkspaceID: workspaceID, ExpectedVersion: input.ExpectedVersion,
		})
		eventType = "source_paused"
	case SourceLifecycleResume:
		if current.State != "paused" {
			return db.RoleSource{}, lifecycleTransitionError(current.State, input.Action)
		}
		if err := c.requireResumeReady(ctx, qtx, current); err != nil {
			return db.RoleSource{}, err
		}
		targetState := "registered"
		if current.CurrentSnapshotDigest.Valid {
			targetState = "active"
		}
		updated, err = qtx.UpdateRoleSourceState(ctx, db.UpdateRoleSourceStateParams{
			State: targetState, ActorUserID: actorID, ID: sourceID, WorkspaceID: workspaceID, ExpectedVersion: input.ExpectedVersion,
		})
		eventType = "source_resumed"
	case SourceLifecycleDetach:
		if current.State != "paused" {
			return db.RoleSource{}, lifecycleTransitionError(current.State, input.Action)
		}
		cancelledScans, cancelledTransfers, err = cancelRoleSourceWork(ctx, qtx, current, "source_detached")
		if err != nil {
			return db.RoleSource{}, err
		}
		updated, err = qtx.UpdateRoleSourceState(ctx, db.UpdateRoleSourceStateParams{
			State: "detached", ActorUserID: actorID, ID: sourceID, WorkspaceID: workspaceID, ExpectedVersion: input.ExpectedVersion,
		})
		eventType = "source_detached"
	case SourceLifecycleRebind:
		if current.State != "detached" {
			return db.RoleSource{}, lifecycleTransitionError(current.State, input.Action)
		}
		newRuntimeID, parseErr := util.ParseUUID(strings.TrimSpace(input.RuntimeID))
		if parseErr != nil {
			return db.RoleSource{}, fmt.Errorf("%w: invalid rebind runtime", ErrInvalidLifecycleTransition)
		}
		configID := strings.TrimSpace(input.DaemonConfigID)
		if _, digestErr := protocol.RoleSourceConfigIDDigest(util.UUIDToString(newRuntimeID), configID); digestErr != nil {
			return db.RoleSource{}, fmt.Errorf("%w: invalid daemon_config_id", ErrInvalidLifecycleTransition)
		}
		// The previous adapter summary belongs to another daemon/config binding.
		// Clear its attributes instead of presenting stale path-derived labels.
		// Loaded attestation and the next scan remain the actual readiness gates.
		configSummary, marshalErr := json.Marshal(ConfigSummary{Configured: true})
		if marshalErr != nil {
			return db.RoleSource{}, marshalErr
		}
		if _, err := qtx.LockRoleSourceRuntimeForRegistration(ctx, db.LockRoleSourceRuntimeForRegistrationParams{
			RuntimeID: newRuntimeID, WorkspaceID: workspaceID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return db.RoleSource{}, ErrLifecycleRuntimeUnavailable
			}
			return db.RoleSource{}, err
		}
		updated, err = qtx.RebindDetachedRoleSource(ctx, db.RebindDetachedRoleSourceParams{
			NewRuntimeID: newRuntimeID, DaemonConfigID: configID, ConfigRedacted: configSummary, ActorUserID: actorID,
			ID: sourceID, WorkspaceID: workspaceID, ExpectedVersion: input.ExpectedVersion,
		})
		eventType = "source_rebound"
	default:
		return db.RoleSource{}, fmt.Errorf("%w: unsupported action", ErrInvalidLifecycleTransition)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.RoleSource{}, ErrSourceVersionConflict
	}
	if err != nil {
		return db.RoleSource{}, err
	}
	if err := c.appendAudit(ctx, qtx, updated, eventType, AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		AdapterKind: Kind(updated.Kind), AdapterVersion: updated.AdapterVersion,
		PreviousRuntimeID: previousRuntimeID, RuntimeID: util.UUIDToString(updated.RuntimeID),
		PreviousState: current.State, State: updated.State, Result: "succeeded",
		CancelledScanCount: cancelledScans, CancelledTransferCount: cancelledTransfers,
	}); err != nil {
		return db.RoleSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSource{}, err
	}
	return updated, nil
}

func lifecycleTransitionError(state string, action SourceLifecycleAction) error {
	return fmt.Errorf("%w: cannot %s source in %s state", ErrInvalidLifecycleTransition, action, state)
}

func cancelRoleSourceWork(ctx context.Context, qtx *db.Queries, source db.RoleSource, errorCode string) (int, int, error) {
	code := pgtype.Text{String: errorCode, Valid: true}
	scans, err := qtx.CancelActiveRoleSourceScans(ctx, db.CancelActiveRoleSourceScansParams{
		ErrorCode: code, SourceID: source.ID, WorkspaceID: source.WorkspaceID,
	})
	if err != nil {
		return 0, 0, err
	}
	transfers, err := qtx.CancelActiveRoleSourceSecretTransfers(ctx, db.CancelActiveRoleSourceSecretTransfersParams{
		ErrorCode: code, SourceID: source.ID, WorkspaceID: source.WorkspaceID,
	})
	if err != nil {
		return 0, 0, err
	}
	if scans > int64(maxNormalizedObjects) || transfers > int64(maxNormalizedObjects) {
		return 0, 0, errors.New("role source lifecycle cancellation count exceeds audit bound")
	}
	return int(scans), int(transfers), nil
}

func (c *ControlPlane) requireResumeReady(ctx context.Context, qtx *db.Queries, source db.RoleSource) error {
	if _, err := qtx.LockRoleSourceRuntimeForRegistration(ctx, db.LockRoleSourceRuntimeForRegistrationParams{
		RuntimeID: source.RuntimeID, WorkspaceID: source.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLifecycleRuntimeUnavailable
		}
		return err
	}
	runtime, err := qtx.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID: source.RuntimeID, WorkspaceID: source.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLifecycleRuntimeUnavailable
		}
		return err
	}
	if runtime.Status != "online" || !runtime.LastSeenAt.Valid || c.now().Sub(runtime.LastSeenAt.Time) > runtimehealth.StaleThreshold {
		return ErrLifecycleRuntimeUnavailable
	}
	attestation, err := qtx.GetRoleSourceRuntimeAttestationForShare(ctx, db.GetRoleSourceRuntimeAttestationForShareParams{
		RuntimeID: source.RuntimeID, WorkspaceID: source.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLifecycleConfigNotLoaded
		}
		return err
	}
	if RuntimeConfigAttestationStatus(source, attestation) != RuntimeConfigLoaded {
		return ErrLifecycleConfigNotLoaded
	}
	return nil
}
