package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type ConfigurationChange struct {
	ObjectKind string `json:"object_kind"`
	RoleID     string `json:"role_id"`
	ObjectID   string `json:"object_id"`
	Operation  string `json:"operation"`
}

type ConfigurationChangeReview struct {
	PlanDigest       string                `json:"plan_digest"`
	TotalChanges     int                   `json:"total_changes"`
	EnvironmentCount int                   `json:"environment_count"`
	MCPCount         int                   `json:"mcp_count"`
	Offset           int                   `json:"offset"`
	Limit            int                   `json:"limit"`
	Changes          []ConfigurationChange `json:"changes"`
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

type adoptionTargetRequest struct {
	TargetKind string `json:"target_kind"`
	Name       string `json:"name"`
	TargetID   string `json:"target_id,omitempty"`
}

func adoptionVersionCommitment(updatedAt pgtype.Timestamptz) (string, error) {
	if !updatedAt.Valid {
		return "", errors.New("adoption target has no version timestamp")
	}
	sum := sha256.Sum256([]byte(updatedAt.Time.UTC().Format(time.RFC3339Nano)))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (c *ControlPlane) attachAdoptionCandidates(
	ctx context.Context,
	q *db.Queries,
	workspaceID, sourceID pgtype.UUID,
	snapshot Snapshot,
	plan *Plan,
) error {
	requests, refs, err := collectAdoptionTargetRequests(snapshot, *plan)
	if err != nil || len(requests) == 0 {
		return err
	}
	body, err := json.Marshal(requests)
	if err != nil {
		return err
	}
	rows, err := q.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: body, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	mappings := map[string]db.RoleSourceObjectMapping{}
	needsAutomationDependencies := false
	for _, request := range requests {
		if request.TargetKind == "autopilot" {
			needsAutomationDependencies = true
			break
		}
	}
	if needsAutomationDependencies {
		mappingRows, err := q.ListRoleSourceObjectMappingsForUpdate(ctx, db.ListRoleSourceObjectMappingsForUpdateParams{
			SourceID: sourceID, WorkspaceID: workspaceID,
		})
		if err != nil {
			return err
		}
		mappings = mappingIndex(mappingRows)
	}
	return resolveAdoptionCandidates(plan, refs, rows, mappings)
}

func resolveAdoptionCandidates(
	plan *Plan,
	refs map[string]adoptionTargetRequest,
	rows []db.ListRoleSourceAdoptionTargetsForUpdateRow,
	mappings map[string]db.RoleSourceObjectMapping,
) error {
	byIdentity := make(map[string][]db.ListRoleSourceAdoptionTargetsForUpdateRow)
	for _, row := range rows {
		byIdentity[row.TargetKind+"\x00"+row.RequestedName] = append(byIdentity[row.TargetKind+"\x00"+row.RequestedName], row)
	}
	for index := range plan.Actions {
		action := &plan.Actions[index]
		request, ok := refs[objectKey(action.Ref)]
		if !ok {
			continue
		}
		matches := byIdentity[request.TargetKind+"\x00"+request.Name]
		if len(matches) == 0 {
			continue
		}
		action.Risk = PlanRiskHigh
		if len(matches) > 1 {
			action.Reason = "object is new in the source but its same-name target is ambiguous"
			plan.Blockers = append(plan.Blockers, PlanBlocker{
				Code: "adoption_target_ambiguous", Message: "more than one existing object matches this source object name; rename or consolidate the existing objects before applying",
				Object: action.Ref,
			})
			continue
		}
		if matches[0].ManagedBySourceID.Valid {
			action.Reason = "object is new in the source but its same-name target is already managed"
			plan.Blockers = append(plan.Blockers, PlanBlocker{
				Code: "adoption_target_managed", Message: "the existing same-name object is already managed by a role source and cannot be adopted",
				Object: action.Ref,
			})
			continue
		}
		if !matches[0].AdoptionEligible.Valid || !matches[0].AdoptionEligible.Bool {
			action.Reason = "object is new in the source but its same-name target is not eligible for adoption"
			plan.Blockers = append(plan.Blockers, PlanBlocker{
				Code: "adoption_target_ineligible", Message: "the existing same-name object is archived or reserved for system use and cannot be adopted",
				Object: action.Ref,
			})
			continue
		}
		commitment, err := adoptionVersionCommitment(matches[0].UpdatedAt)
		if err != nil {
			return err
		}
		action.AdoptionCandidate = &AdoptionCandidate{
			TargetKind: request.TargetKind, TargetID: util.UUIDToString(matches[0].TargetID), VersionCommitment: commitment,
		}
		action.Reason = "object is new in the source and has one explicit unmanaged same-name adoption candidate"
	}
	actions := actionIndex(*plan)
	for index := range plan.Actions {
		action := &plan.Actions[index]
		if action.Ref.Kind != "automation" || action.AdoptionCandidate == nil {
			continue
		}
		matches := byIdentity[action.AdoptionCandidate.TargetKind+"\x00"+refs[objectKey(action.Ref)].Name]
		if len(matches) != 1 {
			continue
		}
		expectedAgentID := ""
		roleRef := ObjectRef{Kind: "role", ID: action.Ref.ParentID}
		if mapping, ok := mappings[objectKey(roleRef)]; ok && !mapping.ArchivedAt.Valid && mapping.TargetKind == "agent" {
			expectedAgentID = util.UUIDToString(mapping.TargetID)
		} else if roleAction, ok := actions[objectKey(roleRef)]; ok && roleAction.AdoptionCandidate != nil {
			expectedAgentID = roleAction.AdoptionCandidate.TargetID
		}
		if expectedAgentID == "" || util.UUIDToString(matches[0].DependencyTargetID) != expectedAgentID {
			action.AdoptionCandidate = nil
			action.Reason = "object is new in the source but its same-name Autopilot is assigned to a different Agent"
			plan.Blockers = append(plan.Blockers, PlanBlocker{
				Code: "adoption_dependency_incompatible", Message: "the existing same-name Autopilot is not assigned to this source role's exact Agent target and cannot be adopted without changing workspace-owned assignment",
				Object: action.Ref,
			})
		}
	}
	sortPlanBlockers(plan.Blockers)
	plan.Applyable = len(plan.Blockers) == 0 && plan.Summary.Blocked == 0
	plan.PlanDigest = ""
	var err error
	plan.PlanDigest, err = digestPlan(*plan)
	return err
}

func collectAdoptionTargetRequests(snapshot Snapshot, plan Plan) ([]adoptionTargetRequest, map[string]adoptionTargetRequest, error) {
	actions := actionIndex(plan)
	requests := make([]adoptionTargetRequest, 0)
	refs := make(map[string]adoptionTargetRequest)
	seenIdentities := make(map[string]struct{})
	appendRequest := func(ref ObjectRef, targetKind, name string) error {
		action, ok := actions[objectKey(ref)]
		if !ok || action.Operation != PlanCreate {
			return nil
		}
		identity := targetKind + "\x00" + name
		if _, duplicate := seenIdentities[identity]; duplicate {
			return fmt.Errorf("%w: source objects request the same %s name %q", ErrInvalidPlanRequest, targetKind, name)
		}
		seenIdentities[identity] = struct{}{}
		request := adoptionTargetRequest{TargetKind: targetKind, Name: name}
		requests = append(requests, request)
		refs[objectKey(ref)] = request
		return nil
	}
	for _, role := range snapshot.Manifest.Roles {
		if err := appendRequest(ObjectRef{Kind: "role", ID: role.ID}, "agent", role.DisplayName); err != nil {
			return nil, nil, err
		}
		for _, skill := range role.Skills {
			if err := appendRequest(ObjectRef{Kind: "skill", ParentID: role.ID, ID: skill.ID}, "skill", skill.Name); err != nil {
				return nil, nil, err
			}
		}
		for _, automation := range role.Automations {
			if err := appendRequest(ObjectRef{Kind: "automation", ParentID: role.ID, ID: automation.ID}, "autopilot", automationTitle(role.DisplayName, automation.Name)); err != nil {
				return nil, nil, err
			}
		}
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].TargetKind+"\x00"+requests[i].Name < requests[j].TargetKind+"\x00"+requests[j].Name
	})
	return requests, refs, nil
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
	if err := c.attachAdoptionCandidates(ctx, qtx, workspaceID, sourceID, target, &plan); err != nil {
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
	if err := c.attachAdoptionCandidates(ctx, qtx, workspaceID, sourceID, target, &plan); err != nil {
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

func (c *ControlPlane) GetPlanConfigurationReview(ctx context.Context, workspaceIDText, sourceIDText, planDigest string, offset, limit int) (ConfigurationChangeReview, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil || !sha256Pattern.MatchString(planDigest) || offset < 0 || offset > maxNormalizedObjects || limit < 1 || limit > snapshotComparisonPageSize {
		return ConfigurationChangeReview{}, ErrInvalidPlanRequest
	}
	row, err := c.queries().GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: planDigest,
	})
	if err != nil {
		return ConfigurationChangeReview{}, err
	}
	plan, err := DecodePersistedPlan(row)
	if err != nil {
		return ConfigurationChangeReview{}, fmt.Errorf("validate configuration review plan: %w", err)
	}
	return configurationChangeReview(plan, offset, limit), nil
}

func configurationChangeReview(plan Plan, offset, limit int) ConfigurationChangeReview {
	changes := make([]ConfigurationChange, 0)
	environmentCount, mcpCount := 0, 0
	for _, action := range plan.Actions {
		if action.Operation == PlanUnchanged || (action.Ref.Kind != "environment" && action.Ref.Kind != "mcp") {
			continue
		}
		if action.Ref.Kind == "environment" {
			environmentCount++
		} else {
			mcpCount++
		}
		changes = append(changes, ConfigurationChange{
			ObjectKind: action.Ref.Kind, RoleID: action.Ref.ParentID,
			ObjectID: action.Ref.ID, Operation: string(action.Operation),
		})
	}
	end := offset + limit
	if end > len(changes) {
		end = len(changes)
	}
	page := []ConfigurationChange{}
	if offset < len(changes) {
		page = changes[offset:end]
	}
	return ConfigurationChangeReview{
		PlanDigest: plan.PlanDigest, TotalChanges: len(changes), EnvironmentCount: environmentCount, MCPCount: mcpCount,
		Offset: offset, Limit: limit, Changes: page,
	}
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
