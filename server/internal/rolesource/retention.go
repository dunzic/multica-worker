package rolesource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/drlock"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	DefaultRetentionMinimumAgeDays          int32 = 90
	DefaultRetentionKeepSuccessfulSnapshots int32 = 10
	RetentionPlanGracePeriod                      = 7 * 24 * time.Hour
	retentionBlockedRetry                         = 24 * time.Hour
	maxRetentionPreviewRows                 int32 = 200
)

var (
	ErrInvalidRetentionPolicy = errors.New("invalid role source retention policy")
	ErrRetentionVersion       = errors.New("role source retention policy version conflict")
)

type RetentionPolicy struct {
	WorkspaceID             string
	SourceID                string
	Version                 int64
	Enabled                 bool
	MinimumAgeDays          int32
	KeepSuccessfulSnapshots int32
	CreatedBy               string
	CreatedAt               string
}

type UpdateRetentionPolicyInput struct {
	WorkspaceID             string
	SourceID                string
	ActorUserID             string
	RequestKey              string
	ExpectedVersion         int64
	Enabled                 bool
	MinimumAgeDays          int32
	KeepSuccessfulSnapshots int32
}

type RetentionCandidatePreview struct {
	SnapshotDigest string
	CreatedAt      string
	EstimatedBytes int64
}

type RetentionPreview struct {
	Policy                   RetentionPolicy
	EligibleCount            int
	EstimatedBytes           int64
	UniquelyReclaimableBytes int64
	Truncated                bool
	Candidates               []RetentionCandidatePreview
}

func (c *ControlPlane) GetRetentionPreview(ctx context.Context, workspaceIDText, sourceIDText string) (RetentionPreview, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return RetentionPreview{}, fmt.Errorf("%w: invalid identity", ErrInvalidRetentionPolicy)
	}
	if _, err := c.queries().GetRoleSourceInWorkspace(ctx, db.GetRoleSourceInWorkspaceParams{ID: sourceID, WorkspaceID: workspaceID}); err != nil {
		return RetentionPreview{}, err
	}
	policy, err := c.queries().GetLatestRoleSourceRetentionPolicy(ctx, db.GetLatestRoleSourceRetentionPolicyParams{
		WorkspaceID: workspaceID, SourceID: sourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		policy = db.RoleSourceRetentionPolicy{
			WorkspaceID: workspaceID, SourceID: sourceID,
			MinimumAgeDays:          DefaultRetentionMinimumAgeDays,
			KeepSuccessfulSnapshots: DefaultRetentionKeepSuccessfulSnapshots,
		}
	} else if err != nil {
		return RetentionPreview{}, err
	}
	rows, err := c.queries().ListEligibleRoleSourceRetentionSnapshots(ctx, db.ListEligibleRoleSourceRetentionSnapshotsParams{
		WorkspaceID: workspaceID, SourceID: sourceID,
		MinimumAgeDays: policy.MinimumAgeDays, KeepSuccessfulSnapshots: policy.KeepSuccessfulSnapshots,
		PlanGracePeriod: retentionInterval(RetentionPlanGracePeriod), ResultLimit: maxRetentionPreviewRows + 1,
	})
	if err != nil {
		return RetentionPreview{}, err
	}
	preview := RetentionPreview{Policy: retentionPolicyFromRow(policy), Candidates: []RetentionCandidatePreview{}}
	if len(rows) > 0 {
		preview.EligibleCount = int(rows[0].EligibleCount)
		preview.EstimatedBytes = rows[0].TotalEstimatedBytes
		preview.UniquelyReclaimableBytes = rows[0].UniquelyReclaimableBytes
	}
	if preview.EligibleCount > int(maxRetentionPreviewRows) {
		preview.Truncated = true
	}
	if len(rows) > int(maxRetentionPreviewRows) {
		rows = rows[:maxRetentionPreviewRows]
	}
	for _, row := range rows {
		preview.Candidates = append(preview.Candidates, RetentionCandidatePreview{
			SnapshotDigest: row.SnapshotDigest, CreatedAt: util.TimestampToString(row.CreatedAt), EstimatedBytes: row.EstimatedBytes,
		})
	}
	return preview, nil
}

func (c *ControlPlane) UpdateRetentionPolicy(ctx context.Context, input UpdateRetentionPolicyInput) (RetentionPolicy, error) {
	if err := validateRetentionPolicyInput(input); err != nil {
		return RetentionPolicy{}, err
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("%w: invalid identity", ErrInvalidRetentionPolicy)
	}
	policyID, err := newPGUUID()
	if err != nil {
		return RetentionPolicy{}, err
	}
	requestDigest := roleSourceRequestKeyDigest(input.RequestKey)
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return RetentionPolicy{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return RetentionPolicy{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return RetentionPolicy{}, err
	}
	if existing, getErr := qtx.GetRoleSourceRetentionPolicyByRequest(ctx, db.GetRoleSourceRetentionPolicyByRequestParams{
		WorkspaceID: workspaceID, SourceID: sourceID, RequestKeyDigest: requestDigest,
	}); getErr == nil {
		if !sameRetentionPolicyRequest(existing, input, actorID) {
			return RetentionPolicy{}, ErrIdempotencyConflict
		}
		return retentionPolicyFromRow(existing), nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return RetentionPolicy{}, getErr
	}
	currentVersion := int64(0)
	if current, getErr := qtx.GetLatestRoleSourceRetentionPolicy(ctx, db.GetLatestRoleSourceRetentionPolicyParams{
		WorkspaceID: workspaceID, SourceID: sourceID,
	}); getErr == nil {
		currentVersion = current.Version
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return RetentionPolicy{}, getErr
	}
	if input.ExpectedVersion != currentVersion {
		return RetentionPolicy{}, ErrRetentionVersion
	}
	row, err := qtx.InsertRoleSourceRetentionPolicy(ctx, db.InsertRoleSourceRetentionPolicyParams{
		ID: policyID, WorkspaceID: workspaceID, SourceID: sourceID, Version: currentVersion + 1,
		RequestKeyDigest: requestDigest, Enabled: input.Enabled, MinimumAgeDays: input.MinimumAgeDays,
		KeepSuccessfulSnapshots: input.KeepSuccessfulSnapshots, CreatedBy: actorID,
	})
	if err != nil {
		return RetentionPolicy{}, err
	}
	result := "disabled"
	if row.Enabled {
		result = "enabled"
	}
	if err := c.appendAudit(ctx, qtx, source, "retention_policy_updated", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: util.UUIDToString(row.ID), Result: result, RetentionPolicyVersion: row.Version,
		RetentionMinimumDays: int(row.MinimumAgeDays), RetentionKeepSucceeded: int(row.KeepSuccessfulSnapshots),
	}); err != nil {
		return RetentionPolicy{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RetentionPolicy{}, err
	}
	return retentionPolicyFromRow(row), nil
}

func (c *ControlPlane) QueueNextRetentionCandidate(ctx context.Context) (db.RoleSourceRetentionCandidate, error) {
	rows, err := c.QueueRetentionCandidates(ctx, 1)
	if err != nil {
		return db.RoleSourceRetentionCandidate{}, err
	}
	if len(rows) == 0 {
		return db.RoleSourceRetentionCandidate{}, pgx.ErrNoRows
	}
	return rows[0], nil
}

// QueueRetentionCandidates performs one bounded global eligibility pass. A
// batch API avoids repeating the expensive policy/rollback scan once per row.
func (c *ControlPlane) QueueRetentionCandidates(ctx context.Context, limit int32) ([]db.RoleSourceRetentionCandidate, error) {
	if limit < 1 || limit > 100 {
		return nil, ErrInvalidRetentionPolicy
	}
	return c.queries().QueueEligibleRoleSourceRetentionCandidates(ctx, db.QueueEligibleRoleSourceRetentionCandidatesParams{
		PlanGracePeriod: retentionInterval(RetentionPlanGracePeriod), CandidateLimit: limit,
	})
}

// PruneRetentionCandidate performs the destructive check and snapshot removal
// in one transaction. The caller must first claim the candidate and supply its
// opaque lease token. A non-empty outcome with nil error means the candidate
// was safely deferred or pruned.
func (c *ControlPlane) PruneRetentionCandidate(ctx context.Context, candidateID, leaseToken pgtype.UUID) (string, error) {
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1)", drlock.AdvisoryLockKey); err != nil {
		return "", err
	}
	candidateIdentity, err := qtx.GetRoleSourceRetentionCandidate(ctx, db.GetRoleSourceRetentionCandidateParams{
		ID: candidateID, LeaseToken: leaseToken,
	})
	if err != nil {
		return "", err
	}
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, candidateIdentity.WorkspaceID); err != nil {
		return "", err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: candidateIdentity.SourceID, WorkspaceID: candidateIdentity.WorkspaceID})
	if err != nil {
		return "", err
	}
	candidate, err := qtx.GetRoleSourceRetentionCandidateForUpdate(ctx, db.GetRoleSourceRetentionCandidateForUpdateParams{
		ID: candidateID, LeaseToken: leaseToken,
	})
	if err != nil {
		return "", err
	}
	if candidate.WorkspaceID != candidateIdentity.WorkspaceID || candidate.SourceID != candidateIdentity.SourceID ||
		candidate.SnapshotDigest != candidateIdentity.SnapshotDigest {
		return "", errors.New("retention candidate identity changed")
	}
	if _, err := qtx.GetRoleSourceSnapshotForUpdate(ctx, db.GetRoleSourceSnapshotForUpdateParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	blocker, err := qtx.GetRoleSourceRetentionBlocker(ctx, db.GetRoleSourceRetentionBlockerParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
		PlanGracePeriod: retentionInterval(RetentionPlanGracePeriod),
	})
	if err != nil {
		return "", err
	}
	if blocker != "" {
		updated, err := qtx.ReleaseRoleSourceRetentionCandidate(ctx, db.ReleaseRoleSourceRetentionCandidateParams{
			RetryDelay: retentionInterval(retentionBlockedRetry), ResultCode: pgtype.Text{String: blocker, Valid: true},
			ID: candidate.ID, LeaseToken: leaseToken,
		})
		if err != nil || updated != 1 {
			return "", fmt.Errorf("release retention candidate: rows=%d: %w", updated, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return blocker, nil
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('multica.role_source_retention_prune', 'on', true)"); err != nil {
		return "", err
	}
	artifactEdges, err := qtx.ListRoleSourceSnapshotArtifacts(ctx, db.ListRoleSourceSnapshotArtifactsParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
	})
	if err != nil {
		return "", err
	}
	artifactDigests := make([]string, 0, len(artifactEdges))
	for _, edge := range artifactEdges {
		artifactDigests = append(artifactDigests, edge.ArtifactDigest)
	}
	lockedArtifactDigests, err := qtx.LockRoleSourceArtifactsByDigestsForUpdate(ctx, db.LockRoleSourceArtifactsByDigestsForUpdateParams{
		WorkspaceID: candidate.WorkspaceID, ArtifactDigests: artifactDigests,
	})
	if err != nil {
		return "", err
	}
	if len(lockedArtifactDigests) != len(artifactDigests) {
		return "", errors.New("retention snapshot artifact ledger is incomplete")
	}
	if err := qtx.DeleteRoleSourceSnapshotArtifacts(ctx, db.DeleteRoleSourceSnapshotArtifactsParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
	}); err != nil {
		return "", err
	}
	uniquelyReclaimableBytes, err := qtx.SumUnreachableRoleSourceArtifactBytesByDigests(ctx, db.SumUnreachableRoleSourceArtifactBytesByDigestsParams{
		WorkspaceID: candidate.WorkspaceID, ArtifactDigests: artifactDigests,
	})
	if err != nil {
		return "", err
	}
	deleted, err := qtx.DeleteRoleSourceSnapshotForRetention(ctx, db.DeleteRoleSourceSnapshotForRetentionParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
	})
	if err != nil || deleted != 1 {
		return "", fmt.Errorf("delete retained snapshot: rows=%d: %w", deleted, err)
	}
	if _, err := qtx.DeleteUnreachableRoleSourceCapabilityVersions(ctx, db.DeleteUnreachableRoleSourceCapabilityVersionsParams{
		WorkspaceID: candidate.WorkspaceID, SourceID: candidate.SourceID, SnapshotDigest: candidate.SnapshotDigest,
	}); err != nil {
		return "", err
	}
	completed, err := qtx.CompleteRoleSourceRetentionCandidate(ctx, db.CompleteRoleSourceRetentionCandidateParams{
		ID: candidate.ID, LeaseToken: leaseToken,
	})
	if err != nil || completed != 1 {
		return "", fmt.Errorf("complete retention candidate: rows=%d: %w", completed, err)
	}
	if err := c.appendAudit(ctx, qtx, source, "snapshot_retention_pruned", AuditActor{Type: "system"}, AuditPayload{
		OperationID: util.UUIDToString(candidate.ID), SnapshotDigest: candidate.SnapshotDigest, Result: "pruned",
		RetentionPolicyVersion: candidate.PolicyVersion, EstimatedBytes: candidate.EstimatedBytes,
		UniquelyReclaimableBytes: uniquelyReclaimableBytes,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return "pruned", nil
}

func validateRetentionPolicyInput(input UpdateRetentionPolicyInput) error {
	if input.ExpectedVersion < 0 || !validLegalHoldRequestKey(input.RequestKey) ||
		input.MinimumAgeDays < 30 || input.MinimumAgeDays > 3650 ||
		input.KeepSuccessfulSnapshots < 2 || input.KeepSuccessfulSnapshots > 100 {
		return ErrInvalidRetentionPolicy
	}
	return nil
}

func sameRetentionPolicyRequest(row db.RoleSourceRetentionPolicy, input UpdateRetentionPolicyInput, actorID pgtype.UUID) bool {
	return row.Version == input.ExpectedVersion+1 && row.Enabled == input.Enabled &&
		row.MinimumAgeDays == input.MinimumAgeDays && row.KeepSuccessfulSnapshots == input.KeepSuccessfulSnapshots &&
		row.CreatedBy == actorID && row.RequestKeyDigest == roleSourceRequestKeyDigest(input.RequestKey)
}

func retentionPolicyFromRow(row db.RoleSourceRetentionPolicy) RetentionPolicy {
	return RetentionPolicy{
		WorkspaceID: util.UUIDToString(row.WorkspaceID), SourceID: util.UUIDToString(row.SourceID), Version: row.Version,
		Enabled: row.Enabled, MinimumAgeDays: row.MinimumAgeDays, KeepSuccessfulSnapshots: row.KeepSuccessfulSnapshots,
		CreatedBy: util.UUIDToString(row.CreatedBy), CreatedAt: util.TimestampToString(row.CreatedAt),
	}
}

func retentionInterval(duration time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: duration.Microseconds(), Valid: true}
}
