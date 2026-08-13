package rolesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrIdempotencyConflict       = errors.New("role source request key was already used for different input")
	ErrInvalidPlanRequest        = errors.New("invalid role source plan request")
	ErrInvalidApprovalRequest    = errors.New("invalid role source approval request")
	ErrInvalidSnapshotComparison = errors.New("invalid role source snapshot comparison")
)

const snapshotComparisonPageSize = 100

type SnapshotSummary struct {
	SnapshotDigest  string `json:"snapshot_digest"`
	ManifestDigest  string `json:"manifest_digest"`
	Kind            Kind   `json:"kind"`
	AdapterVersion  string `json:"adapter_version"`
	Revision        string `json:"revision,omitempty"`
	TreeDigest      string `json:"tree_digest"`
	RoleCount       int    `json:"role_count"`
	CapabilityCount int    `json:"capability_count"`
	DiagnosticCount int    `json:"diagnostic_count"`
	CreatedAt       string `json:"created_at"`
}

type SnapshotChange struct {
	ObjectKind  string `json:"object_kind"`
	ObjectID    string `json:"object_id"`
	ParentID    string `json:"parent_id,omitempty"`
	DisplayName string `json:"display_name"`
	Operation   string `json:"operation"`
}

type SnapshotComparison struct {
	FromSnapshotDigest string           `json:"from_snapshot_digest"`
	ToSnapshotDigest   string           `json:"to_snapshot_digest"`
	TotalChanges       int              `json:"total_changes"`
	Offset             int              `json:"offset"`
	Limit              int              `json:"limit"`
	Changes            []SnapshotChange `json:"changes"`
}

type CreatePlanInput struct {
	WorkspaceID          string
	SourceID             string
	TargetSnapshotDigest string
	ActorUserID          string
}

type CreateRollbackPlanInput struct {
	WorkspaceID          string
	SourceID             string
	TargetSnapshotDigest string
	ActorUserID          string
}

func (c *ControlPlane) CreatePlan(ctx context.Context, input CreatePlanInput) (db.RoleSourcePlan, error) {
	if !sha256Pattern.MatchString(input.TargetSnapshotDigest) {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: invalid target snapshot digest", ErrInvalidPlanRequest)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourcePlan{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	targetRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: input.TargetSnapshotDigest,
	})
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	target, err := DecodePersistedSnapshot(targetRow)
	if err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("validate persisted target snapshot: %w", err)
	}
	var base *Snapshot
	if source.CurrentSnapshotDigest.Valid {
		baseRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
			SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: source.CurrentSnapshotDigest.String,
		})
		if err != nil {
			return db.RoleSourcePlan{}, err
		}
		value, err := DecodePersistedSnapshot(baseRow)
		if err != nil {
			return db.RoleSourcePlan{}, fmt.Errorf("validate persisted base snapshot: %w", err)
		}
		base = &value
	}
	plan, err := BuildPlan(util.UUIDToString(sourceID), base, target)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	fromDigest := pgtype.Text{}
	if plan.FromSnapshotDigest != "" {
		fromDigest = pgtype.Text{String: plan.FromSnapshotDigest, Valid: true}
	}
	row, err := qtx.InsertRoleSourcePlan(ctx, db.InsertRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
		FromSnapshotDigest: fromDigest, ToSnapshotDigest: plan.ToSnapshotDigest, Plan: body, CreatedBy: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{
			SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
		})
		if err != nil {
			return db.RoleSourcePlan{}, err
		}
		stored, err := DecodePersistedPlan(row)
		if err != nil || !reflect.DeepEqual(stored, plan) {
			return db.RoleSourcePlan{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourcePlan{}, err
		}
		return row, nil
	}
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "plan_created", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		SnapshotDigest: plan.ToSnapshotDigest, PlanDigest: plan.PlanDigest, Result: "created",
		CreateCount: plan.Summary.Create, UpdateCount: plan.Summary.Update, UnchangedCount: plan.Summary.Unchanged,
		ArchiveCount: plan.Summary.ArchiveCandidate, BlockedCount: plan.Summary.Blocked,
	}); err != nil {
		return db.RoleSourcePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourcePlan{}, err
	}
	return row, nil
}

func (c *ControlPlane) CreateRollbackPlan(ctx context.Context, input CreateRollbackPlanInput) (db.RoleSourcePlan, error) {
	if !sha256Pattern.MatchString(input.TargetSnapshotDigest) {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: invalid rollback target snapshot digest", ErrInvalidPlanRequest)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: %v", ErrInvalidPlanRequest, err)
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourcePlan{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	if !source.CurrentSnapshotDigest.Valid || source.CurrentSnapshotDigest.String == input.TargetSnapshotDigest {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: rollback requires a different active snapshot", ErrInvalidPlanRequest)
	}
	historicalApply, err := qtx.GetLatestSucceededRoleSourceApplyForSnapshot(ctx, db.GetLatestSucceededRoleSourceApplyForSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: input.TargetSnapshotDigest,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoleSourcePlan{}, fmt.Errorf("%w: rollback target was never successfully applied", ErrInvalidPlanRequest)
		}
		return db.RoleSourcePlan{}, err
	}
	if _, err := decodeApplyReceipt(historicalApply); err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("%w: rollback target apply receipt is invalid", ErrInvalidPlanRequest)
	}
	currentRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: source.CurrentSnapshotDigest.String,
	})
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	current, err := DecodePersistedSnapshot(currentRow)
	if err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("validate active rollback snapshot: %w", err)
	}
	targetRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: input.TargetSnapshotDigest,
	})
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	target, err := DecodePersistedSnapshot(targetRow)
	if err != nil {
		return db.RoleSourcePlan{}, fmt.Errorf("validate rollback target snapshot: %w", err)
	}
	plan, err := BuildRollbackPlan(util.UUIDToString(sourceID), current, target)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return db.RoleSourcePlan{}, err
	}
	row, err := qtx.InsertRoleSourcePlan(ctx, db.InsertRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
		FromSnapshotDigest: pgtype.Text{String: plan.FromSnapshotDigest, Valid: true},
		ToSnapshotDigest:   plan.ToSnapshotDigest, Plan: body, CreatedBy: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest})
		if err != nil {
			return db.RoleSourcePlan{}, err
		}
		stored, decodeErr := DecodePersistedPlan(row)
		if decodeErr != nil || !reflect.DeepEqual(stored, plan) {
			return db.RoleSourcePlan{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourcePlan{}, err
		}
		return row, nil
	} else if err != nil {
		return db.RoleSourcePlan{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "rollback_plan_created", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		SnapshotDigest: plan.ToSnapshotDigest, PlanDigest: plan.PlanDigest, Result: "created",
		CreateCount: plan.Summary.Create, UpdateCount: plan.Summary.Update, UnchangedCount: plan.Summary.Unchanged,
		ArchiveCount: plan.Summary.ArchiveCandidate, BlockedCount: plan.Summary.Blocked,
	}); err != nil {
		return db.RoleSourcePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourcePlan{}, err
	}
	return row, nil
}

type RecordPlanApprovalInput struct {
	WorkspaceID string
	SourceID    string
	PlanDigest  string
	RequestKey  string
	Decision    string
	Decisions   *ApprovalDecisions
	ActorUserID string
}

func (c *ControlPlane) RecordPlanApproval(ctx context.Context, input RecordPlanApprovalInput) (db.RoleSourcePlanApproval, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	if !sha256Pattern.MatchString(input.PlanDigest) || input.RequestKey == "" || len(input.RequestKey) > 200 {
		return db.RoleSourcePlanApproval{}, fmt.Errorf("%w: approval requires valid plan digest and bounded request key", ErrInvalidApprovalRequest)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSourcePlanApproval{}, fmt.Errorf("%w: %v", ErrInvalidApprovalRequest, err)
	}
	var decisionsBody []byte
	if input.Decisions == nil {
		decisionsBody = []byte(`{}`)
	} else {
		body, err := json.Marshal(input.Decisions)
		if err != nil {
			return db.RoleSourcePlanApproval{}, err
		}
		var copy ApprovalDecisions
		if err := json.Unmarshal(body, &copy); err != nil {
			return db.RoleSourcePlanApproval{}, err
		}
		CanonicalizeApprovalDecisions(&copy)
		input.Decisions = &copy
		decisionsBody, err = json.Marshal(copy)
		if err != nil {
			return db.RoleSourcePlanApproval{}, err
		}
	}

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	planRow, err := qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: input.PlanDigest,
	})
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	plan, err := DecodePersistedPlan(planRow)
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	if err := ValidateApprovalDecisions(plan, input.Decision, input.Decisions); err != nil {
		return db.RoleSourcePlanApproval{}, fmt.Errorf("%w: %v", ErrInvalidApprovalRequest, err)
	}
	approvalID, err := newPGUUID()
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	row, err := qtx.InsertRoleSourcePlanApproval(ctx, db.InsertRoleSourcePlanApprovalParams{
		ID: approvalID, SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: input.PlanDigest,
		RequestKey: input.RequestKey, Decision: input.Decision, Decisions: decisionsBody, ActorUserID: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = qtx.GetRoleSourcePlanApprovalByRequest(ctx, db.GetRoleSourcePlanApprovalByRequestParams{
			SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
		})
		if err != nil {
			return db.RoleSourcePlanApproval{}, err
		}
		storedDecisions, err := canonicalStoredJSONObject(row.Decisions)
		if err != nil {
			return db.RoleSourcePlanApproval{}, ErrIdempotencyConflict
		}
		expectedDecisions, err := canonicalStoredJSONObject(decisionsBody)
		if err != nil || row.PlanDigest != input.PlanDigest || row.Decision != input.Decision || row.ActorUserID != actorID || !bytes.Equal(storedDecisions, expectedDecisions) {
			return db.RoleSourcePlanApproval{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourcePlanApproval{}, err
		}
		return row, nil
	}
	if err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	eventType := "plan_approved"
	if input.Decision == "rejected" {
		eventType = "plan_rejected"
	}
	if err := c.appendAudit(ctx, qtx, source, eventType, AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: util.UUIDToString(row.ID), PlanDigest: input.PlanDigest, Result: input.Decision,
	}); err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourcePlanApproval{}, err
	}
	return row, nil
}

func (c *ControlPlane) GetPlan(ctx context.Context, workspaceIDText, sourceIDText, planDigest string) (db.RoleSourcePlan, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil || !sha256Pattern.MatchString(planDigest) {
		return db.RoleSourcePlan{}, errors.New("invalid plan identity")
	}
	return c.queries().GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: planDigest})
}

func (c *ControlPlane) ListPlans(ctx context.Context, workspaceIDText, sourceIDText string, limit int32) ([]db.RoleSourcePlan, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("plan list limit must be between 1 and 100")
	}
	return c.queries().ListRoleSourcePlans(ctx, db.ListRoleSourcePlansParams{SourceID: sourceID, WorkspaceID: workspaceID, ResultLimit: limit})
}

func (c *ControlPlane) ListSnapshots(ctx context.Context, workspaceIDText, sourceIDText string, limit int32) ([]db.RoleSourceSnapshot, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("snapshot list limit must be between 1 and 100")
	}
	return c.queries().ListRoleSourceSnapshots(ctx, db.ListRoleSourceSnapshotsParams{SourceID: sourceID, WorkspaceID: workspaceID, ResultLimit: limit})
}

func SnapshotSummaryFromRow(row db.RoleSourceSnapshot) (SnapshotSummary, error) {
	if !row.CreatedAt.Valid {
		return SnapshotSummary{}, errors.New("snapshot creation time is missing")
	}
	snapshot, err := DecodePersistedSnapshot(row)
	if err != nil {
		return SnapshotSummary{}, err
	}
	return SnapshotSummary{
		SnapshotDigest: snapshot.SnapshotDigest, ManifestDigest: snapshot.ManifestDigest,
		Kind: snapshot.Kind, AdapterVersion: snapshot.AdapterVersion,
		Revision: snapshot.SourceEvidence.Revision, TreeDigest: snapshot.SourceEvidence.TreeDigest,
		RoleCount: len(snapshot.Manifest.Roles), CapabilityCount: len(snapshot.Manifest.Capabilities),
		DiagnosticCount: len(snapshot.Diagnostics), CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}, nil
}

func (c *ControlPlane) CompareSnapshots(ctx context.Context, workspaceIDText, sourceIDText, fromDigest, toDigest string, offset, limit int) (SnapshotComparison, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil || !sha256Pattern.MatchString(fromDigest) || !sha256Pattern.MatchString(toDigest) || fromDigest == toDigest {
		return SnapshotComparison{}, ErrInvalidSnapshotComparison
	}
	if offset < 0 || offset > maxNormalizedObjects*3 || limit < 1 || limit > snapshotComparisonPageSize {
		return SnapshotComparison{}, ErrInvalidSnapshotComparison
	}
	fromRow, err := c.queries().GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: fromDigest,
	})
	if err != nil {
		return SnapshotComparison{}, err
	}
	toRow, err := c.queries().GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: toDigest,
	})
	if err != nil {
		return SnapshotComparison{}, err
	}
	from, err := DecodePersistedSnapshot(fromRow)
	if err != nil {
		return SnapshotComparison{}, fmt.Errorf("validate from snapshot: %w", err)
	}
	to, err := DecodePersistedSnapshot(toRow)
	if err != nil {
		return SnapshotComparison{}, fmt.Errorf("validate to snapshot: %w", err)
	}
	changes := compareSnapshotObjects(from.Manifest, to.Manifest)
	end := offset + limit
	if end > len(changes) {
		end = len(changes)
	}
	page := []SnapshotChange{}
	if offset < len(changes) {
		page = changes[offset:end]
	}
	return SnapshotComparison{
		FromSnapshotDigest: fromDigest, ToSnapshotDigest: toDigest, TotalChanges: len(changes),
		Offset: offset, Limit: limit, Changes: page,
	}, nil
}

type comparableObject struct {
	kind, id, parentID, displayName string
	value                           any
}

func compareSnapshotObjects(from, to Manifest) []SnapshotChange {
	left := comparableSnapshotObjects(from)
	right := comparableSnapshotObjects(to)
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	changes := make([]SnapshotChange, 0)
	for _, key := range ordered {
		before, beforeOK := left[key]
		after, afterOK := right[key]
		operation := "changed"
		object := after
		switch {
		case !beforeOK:
			operation = "added"
		case !afterOK:
			operation, object = "removed", before
		case reflect.DeepEqual(before.value, after.value):
			continue
		}
		changes = append(changes, SnapshotChange{
			ObjectKind: object.kind, ObjectID: object.id, ParentID: object.parentID,
			DisplayName: object.displayName, Operation: operation,
		})
	}
	return changes
}

func comparableSnapshotObjects(manifest Manifest) map[string]comparableObject {
	objects := make(map[string]comparableObject, len(manifest.Roles)+len(manifest.Capabilities))
	for _, role := range manifest.Roles {
		roleWithoutSkills := role
		roleWithoutSkills.Skills = nil
		key := "role\x00" + role.ID
		objects[key] = comparableObject{kind: "role", id: role.ID, displayName: role.DisplayName, value: roleWithoutSkills}
		for _, skill := range role.Skills {
			key := "skill\x00" + role.ID + "\x00" + skill.ID
			objects[key] = comparableObject{kind: "skill", id: skill.ID, parentID: role.ID, displayName: skill.Name, value: skill}
		}
	}
	for _, capability := range manifest.Capabilities {
		key := "capability\x00" + capability.ID
		objects[key] = comparableObject{kind: "capability", id: capability.ID, displayName: capability.Name, value: capability}
	}
	return objects
}

func (c *ControlPlane) ListPlanApprovals(ctx context.Context, workspaceIDText, sourceIDText, planDigest string, limit int32) ([]db.RoleSourcePlanApproval, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil || !sha256Pattern.MatchString(planDigest) {
		return nil, errors.New("invalid plan identity")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("approval list limit must be between 1 and 100")
	}
	return c.queries().ListRoleSourcePlanApprovals(ctx, db.ListRoleSourcePlanApprovalsParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: planDigest, ResultLimit: limit,
	})
}

// DecodePersistedSnapshot reconstructs and fully verifies an immutable
// snapshot before an API, approval, or apply path is allowed to trust it.
func DecodePersistedSnapshot(row db.RoleSourceSnapshot) (Snapshot, error) {
	snapshot := Snapshot{
		Kind: Kind(row.Kind), AdapterVersion: row.AdapterVersion, ContractVersion: row.ContractVersion,
		SnapshotDigest: row.SnapshotDigest, ManifestDigest: row.ManifestDigest,
	}
	if err := json.Unmarshal(row.Manifest, &snapshot.Manifest); err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(row.Diagnostics, &snapshot.Diagnostics); err != nil {
		return Snapshot{}, err
	}
	if err := json.Unmarshal(row.SourceEvidence, &snapshot.SourceEvidence); err != nil {
		return Snapshot{}, err
	}
	return validatedSnapshotCopy(snapshot)
}

// DecodePersistedPlan verifies both the canonical plan digest and the indexed
// persistence columns, detecting JSONB corruption or tampering.
func DecodePersistedPlan(row db.RoleSourcePlan) (Plan, error) {
	var plan Plan
	if err := json.Unmarshal(row.Plan, &plan); err != nil {
		return Plan{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}
	if plan.PlanDigest != row.PlanDigest || plan.ToSnapshotDigest != row.ToSnapshotDigest ||
		(plan.FromSnapshotDigest == "") != !row.FromSnapshotDigest.Valid ||
		(row.FromSnapshotDigest.Valid && plan.FromSnapshotDigest != row.FromSnapshotDigest.String) {
		return Plan{}, errors.New("persisted plan columns do not match plan body")
	}
	return plan, nil
}

func canonicalStoredJSONObject(body []byte) ([]byte, error) {
	return canonicalJSONObject(json.RawMessage(body), 64<<10)
}
