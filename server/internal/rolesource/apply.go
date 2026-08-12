package rolesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	ApplyReceiptContractVersion = "1.0"
	maxApplyArtifacts           = 500
	maxApplyArtifactBytes       = 64 << 20
)

var (
	ErrInvalidApplyRequest     = errors.New("invalid role source apply request")
	ErrApplyConflict           = errors.New("role source apply conflicts with current state")
	ErrMaterializationBlocked  = errors.New("role source materialization is not supported for this snapshot")
	ErrMaterializationOverload = errors.New("role source materialization capacity is exhausted")
)

type ApplyPlanInput struct {
	WorkspaceID string
	SourceID    string
	PlanDigest  string
	ApprovalID  string
	RequestKey  string
	ActorUserID string
}

type ApplyCounts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Archived  int `json:"archived"`
	Retained  int `json:"retained"`
}

type ApplyMapping struct {
	Source ObjectRef `json:"source"`
	Target string    `json:"target"`
	ID     string    `json:"id"`
}

type ApplyReceipt struct {
	ContractVersion string         `json:"contract_version"`
	ApplyID         string         `json:"apply_id"`
	SourceID        string         `json:"source_id"`
	WorkspaceID     string         `json:"workspace_id"`
	SnapshotDigest  string         `json:"snapshot_digest"`
	PlanDigest      string         `json:"plan_digest"`
	ApprovalID      string         `json:"approval_id"`
	Counts          ApplyCounts    `json:"counts"`
	Mappings        []ApplyMapping `json:"mappings"`
	ReceiptDigest   string         `json:"receipt_digest"`
}

type verifiedArtifact struct {
	ref        ArtifactRef
	storageKey string
	body       string
}

type materializationState struct {
	q           *db.Queries
	workspaceID pgtype.UUID
	source      db.RoleSource
	actorID     pgtype.UUID
	snapshot    Snapshot
	plan        Plan
	decisions   map[string]ArchiveDecision
	actions     map[string]PlanAction
	artifacts   map[string]verifiedArtifact
	mappings    map[string]db.RoleSourceObjectMapping
	runtimeMode string
	now         func() time.Time
	receipt     *ApplyReceipt
}

func (c *ControlPlane) ApplyPlan(ctx context.Context, input ApplyPlanInput) (db.RoleSourceApply, ApplyReceipt, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	if !sha256Pattern.MatchString(input.PlanDigest) || input.RequestKey == "" || len(input.RequestKey) > 200 {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: apply requires a valid plan digest and bounded request key", ErrInvalidApplyRequest)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: %v", ErrInvalidApplyRequest, err)
	}
	approvalID, err := util.ParseUUID(input.ApprovalID)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: invalid approval id", ErrInvalidApplyRequest)
	}
	input.WorkspaceID = util.UUIDToString(workspaceID)
	input.SourceID = util.UUIDToString(sourceID)
	input.ActorUserID = util.UUIDToString(actorID)
	input.ApprovalID = util.UUIDToString(approvalID)
	if c.artifacts == nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, errors.New("role source artifact reader is not configured")
	}
	if !c.acquireMaterializeSlot() {
		return db.RoleSourceApply{}, ApplyReceipt{}, ErrMaterializationOverload
	}
	defer c.releaseMaterializeSlot()

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	planRow, err := qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: input.PlanDigest})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	plan, err := DecodePersistedPlan(planRow)
	if err != nil || !plan.Applyable {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: plan is invalid or blocked", ErrInvalidApplyRequest)
	}
	existing, existingErr := qtx.GetRoleSourceApplyByRequest(ctx, db.GetRoleSourceApplyByRequestParams{
		SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
	})
	if existingErr == nil {
		receipt, matchErr := matchIdempotentApply(existing, plan, input, actorID)
		if matchErr != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, matchErr
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, err
		}
		return existing, receipt, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return db.RoleSourceApply{}, ApplyReceipt{}, existingErr
	}
	if source.State == "detached" || source.State == "paused" {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: source state %q cannot be applied", ErrApplyConflict, source.State)
	}
	runtime, err := qtx.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: source.RuntimeID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("validate materialization runtime: %w", err)
	}
	if !snapshotCASMatches(source.CurrentSnapshotDigest, plan.FromSnapshotDigest) {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: plan base snapshot is stale", ErrApplyConflict)
	}
	approval, err := qtx.GetRoleSourcePlanApprovalByID(ctx, db.GetRoleSourcePlanApprovalByIDParams{
		ID: approvalID, SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
	})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	decisions, err := decodeApprovedDecisions(plan, approval)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: %v", ErrInvalidApplyRequest, err)
	}
	snapshotRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: plan.ToSnapshotDigest,
	})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	snapshot, err := DecodePersistedSnapshot(snapshotRow)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("validate apply snapshot: %w", err)
	}
	if err := validateMaterializationScope(snapshot, plan, decisions); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}

	applyID, err := newPGUUID()
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	applyRow, err := qtx.InsertRoleSourceApply(ctx, db.InsertRoleSourceApplyParams{
		ID: applyID, SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
		Mode: "apply", SnapshotDigest: plan.ToSnapshotDigest, PlanDigest: plan.PlanDigest, ActorUserID: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, loadErr := qtx.GetRoleSourceApplyByRequest(ctx, db.GetRoleSourceApplyByRequestParams{
			SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
		})
		if loadErr != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, loadErr
		}
		receipt, matchErr := matchIdempotentApply(existing, plan, input, actorID)
		if matchErr != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, matchErr
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, err
		}
		return existing, receipt, nil
	}
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	applyRow, err = qtx.MarkRoleSourceApplyRunning(ctx, db.MarkRoleSourceApplyRunningParams{ID: applyRow.ID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}

	artifacts, err := c.readAndVerifyArtifacts(ctx, qtx, workspaceID, snapshot)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	mappingRows, err := qtx.ListRoleSourceObjectMappingsForUpdate(ctx, db.ListRoleSourceObjectMappingsForUpdateParams{SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	invalidMappings, err := qtx.CountInvalidRoleSourceObjectMappings(ctx, db.CountInvalidRoleSourceObjectMappingsParams{SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if invalidMappings != 0 {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: %d object mappings have missing, mismatched or cross-tenant targets", ErrApplyConflict, invalidMappings)
	}
	receipt := ApplyReceipt{
		ContractVersion: ApplyReceiptContractVersion, ApplyID: util.UUIDToString(applyRow.ID),
		SourceID: input.SourceID, WorkspaceID: input.WorkspaceID, SnapshotDigest: plan.ToSnapshotDigest,
		PlanDigest: plan.PlanDigest, ApprovalID: input.ApprovalID, Mappings: []ApplyMapping{},
	}
	state := materializationState{
		q: qtx, workspaceID: workspaceID, source: source, actorID: actorID, snapshot: snapshot, plan: plan,
		decisions: decisions, actions: actionIndex(plan), artifacts: artifacts, mappings: mappingIndex(mappingRows),
		runtimeMode: runtime.RuntimeMode, now: c.now, receipt: &receipt,
	}
	if err := state.materialize(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	expected := pgtype.Text{}
	if plan.FromSnapshotDigest != "" {
		expected = pgtype.Text{String: plan.FromSnapshotDigest, Valid: true}
	}
	if _, err := qtx.AdvanceRoleSourceSnapshot(ctx, db.AdvanceRoleSourceSnapshotParams{
		SnapshotDigest: pgtype.Text{String: plan.ToSnapshotDigest, Valid: true}, ActorUserID: actorID,
		ID: sourceID, WorkspaceID: workspaceID, ExpectedSnapshotDigest: expected,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoleSourceApply{}, ApplyReceipt{}, ErrApplyConflict
		}
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	sort.Slice(receipt.Mappings, func(i, j int) bool {
		return objectKey(receipt.Mappings[i].Source) < objectKey(receipt.Mappings[j].Source)
	})
	receiptBody, receiptDigest, err := encodeApplyReceipt(receipt)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	receipt.ReceiptDigest = receiptDigest
	receiptBody, _, err = encodeApplyReceipt(receipt)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	applyRow, err = qtx.CompleteRoleSourceApply(ctx, db.CompleteRoleSourceApplyParams{
		Status: "succeeded", ReceiptDigest: pgtype.Text{String: receiptDigest, Valid: true}, Receipt: receiptBody,
		ID: applyRow.ID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "apply_succeeded", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: receipt.ApplyID, SnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest,
		ReceiptDigest: receiptDigest, Result: "succeeded", CreateCount: receipt.Counts.Created,
		UpdateCount: receipt.Counts.Updated, UnchangedCount: receipt.Counts.Unchanged,
		ArchiveCount: receipt.Counts.Archived,
	}); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	return applyRow, receipt, nil
}

func matchIdempotentApply(existing db.RoleSourceApply, plan Plan, input ApplyPlanInput, actorID pgtype.UUID) (ApplyReceipt, error) {
	if existing.Mode != "apply" || existing.SnapshotDigest != plan.ToSnapshotDigest || existing.PlanDigest != plan.PlanDigest || existing.ActorUserID != actorID || existing.Status != "succeeded" {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	receipt, err := decodeApplyReceipt(existing)
	if err != nil || receipt.ApprovalID != input.ApprovalID || receipt.SourceID != input.SourceID || receipt.WorkspaceID != input.WorkspaceID {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	return receipt, nil
}

func (c *ControlPlane) acquireMaterializeSlot() bool {
	if c.materializeSlots == nil {
		return true
	}
	select {
	case c.materializeSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (c *ControlPlane) releaseMaterializeSlot() {
	if c.materializeSlots != nil {
		<-c.materializeSlots
	}
}

func snapshotCASMatches(current pgtype.Text, expected string) bool {
	return (expected == "" && !current.Valid) || (current.Valid && current.String == expected)
}

func decodeApprovedDecisions(plan Plan, approval db.RoleSourcePlanApproval) (map[string]ArchiveDecision, error) {
	if approval.Decision != "approved" {
		return nil, errors.New("apply requires an approved plan")
	}
	var decisions ApprovalDecisions
	if err := json.Unmarshal(approval.Decisions, &decisions); err != nil {
		return nil, err
	}
	if err := ValidateApprovalDecisions(plan, approval.Decision, &decisions); err != nil {
		return nil, err
	}
	result := make(map[string]ArchiveDecision, len(decisions.Archives))
	for _, decision := range decisions.Archives {
		result[objectKey(decision.Ref)] = decision.Decision
	}
	return result, nil
}

func validateMaterializationScope(snapshot Snapshot, plan Plan, decisions map[string]ArchiveDecision) error {
	for _, role := range snapshot.Manifest.Roles {
		if role.Profile != nil {
			return fmt.Errorf("%w: role profiles require a dedicated target field", ErrMaterializationBlocked)
		}
		if len(role.CapabilityBindings) > 0 || len(role.Environment) > 0 || len(role.MCP) > 0 {
			return fmt.Errorf("%w: capability bindings, environment and MCP require dedicated secure contracts", ErrMaterializationBlocked)
		}
		for _, skill := range role.Skills {
			if len(skill.Artifacts) > 0 {
				return fmt.Errorf("%w: supporting skill files require per-file ownership mappings", ErrMaterializationBlocked)
			}
		}
	}
	for _, action := range plan.Actions {
		switch action.Ref.Kind {
		case "role", "skill", "automation", "capability":
		case "capability_binding", "environment", "mcp":
			if action.Operation == PlanCreate || action.Operation == PlanUpdate {
				return fmt.Errorf("%w: %s objects do not have a safe target contract", ErrMaterializationBlocked, action.Ref.Kind)
			}
		default:
			return fmt.Errorf("%w: unsupported object kind %q", ErrMaterializationBlocked, action.Ref.Kind)
		}
		if action.Operation == PlanArchiveCandidate && decisions[objectKey(action.Ref)] == ArchiveDecisionArchive && action.Ref.Kind != "role" && action.Ref.Kind != "automation" {
			return fmt.Errorf("%w: %s archive is not reversible yet; choose retain", ErrMaterializationBlocked, action.Ref.Kind)
		}
	}
	return nil
}

func (c *ControlPlane) readAndVerifyArtifacts(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, snapshot Snapshot) (map[string]verifiedArtifact, error) {
	refs, err := CollectArtifactRefs(snapshot)
	if err != nil {
		return nil, err
	}
	if len(refs) > maxApplyArtifacts {
		return nil, fmt.Errorf("%w: artifact count exceeds %d", ErrMaterializationBlocked, maxApplyArtifacts)
	}
	result := make(map[string]verifiedArtifact, len(refs))
	var total int64
	for _, ref := range refs {
		total += ref.SizeBytes
		if total > maxApplyArtifactBytes {
			return nil, fmt.Errorf("%w: artifact bytes exceed %d", ErrMaterializationBlocked, maxApplyArtifactBytes)
		}
		row, err := q.GetRoleSourceArtifactForApply(ctx, db.GetRoleSourceArtifactForApplyParams{WorkspaceID: workspaceID, Digest: ref.Digest})
		if err != nil {
			return nil, fmt.Errorf("artifact ledger %s: %w", ref.Digest, err)
		}
		if row.SizeBytes != ref.SizeBytes {
			return nil, fmt.Errorf("artifact ledger size mismatch for %s", ref.Digest)
		}
		reader, err := c.artifacts.GetReader(ctx, row.StorageKey)
		if err != nil {
			return nil, fmt.Errorf("read artifact %s: %w", ref.Digest, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, ref.SizeBytes+1))
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read artifact %s: %w", ref.Digest, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close artifact %s: %w", ref.Digest, closeErr)
		}
		sum := sha256.Sum256(body)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if int64(len(body)) != ref.SizeBytes || actual != ref.Digest {
			return nil, fmt.Errorf("artifact body mismatch for %s", ref.Digest)
		}
		if !utf8.Valid(body) || strings.IndexByte(string(body), 0) >= 0 {
			return nil, fmt.Errorf("%w: only UTF-8 text artifacts are materialized", ErrMaterializationBlocked)
		}
		result[ref.Digest] = verifiedArtifact{ref: ref, storageKey: row.StorageKey, body: string(body)}
	}
	return result, nil
}

func actionIndex(plan Plan) map[string]PlanAction {
	result := make(map[string]PlanAction, len(plan.Actions))
	for _, action := range plan.Actions {
		result[objectKey(action.Ref)] = action
	}
	return result
}

func mappingIndex(rows []db.RoleSourceObjectMapping) map[string]db.RoleSourceObjectMapping {
	result := make(map[string]db.RoleSourceObjectMapping, len(rows))
	for _, row := range rows {
		result[objectKey(ObjectRef{Kind: row.SourceKind, ParentID: row.SourceParentID, ID: row.SourceObjectID})] = row
	}
	return result
}

func (s *materializationState) materialize(ctx context.Context) error {
	for _, capability := range s.snapshot.Manifest.Capabilities {
		if err := s.materializeCapability(ctx, capability); err != nil {
			return err
		}
	}
	for _, role := range s.snapshot.Manifest.Roles {
		if err := s.materializeRole(ctx, role); err != nil {
			return err
		}
	}
	for _, role := range s.snapshot.Manifest.Roles {
		for _, skill := range role.Skills {
			if err := s.materializeSkill(ctx, role, skill); err != nil {
				return err
			}
		}
	}
	for _, role := range s.snapshot.Manifest.Roles {
		for _, automation := range role.Automations {
			if err := s.materializeAutomation(ctx, role, automation); err != nil {
				return err
			}
		}
	}
	return s.materializeArchives(ctx)
}

func (s *materializationState) materializeCapability(ctx context.Context, capability Capability) error {
	ref := ObjectRef{Kind: "capability", ID: capability.ID}
	action := s.actions[objectKey(ref)]
	if action.Operation == PlanUnchanged {
		s.receipt.Counts.Unchanged++
		return nil
	}
	if action.Operation == PlanArchiveCandidate {
		if s.decisions[objectKey(ref)] == ArchiveDecisionRetain {
			s.receipt.Counts.Retained++
		}
		return nil
	}
	definition, err := json.Marshal(capability)
	if err != nil {
		return err
	}
	_, err = s.q.InsertRoleSourceCapabilityVersion(ctx, db.InsertRoleSourceCapabilityVersionParams{
		WorkspaceID: s.workspaceID, SourceID: s.source.ID, CapabilityID: capability.ID, Version: capability.Version,
		ObjectDigest: action.AfterDigest, Definition: definition, SnapshotDigest: s.snapshot.SnapshotDigest,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = s.q.GetRoleSourceCapabilityVersion(ctx, db.GetRoleSourceCapabilityVersionParams{
			SourceID: s.source.ID, CapabilityID: capability.ID, Version: capability.Version, ObjectDigest: action.AfterDigest,
		})
	}
	if err == nil {
		if action.Operation == PlanCreate {
			s.receipt.Counts.Created++
		} else {
			s.receipt.Counts.Updated++
		}
	}
	return err
}

func (s *materializationState) materializeRole(ctx context.Context, role Role) error {
	ref := ObjectRef{Kind: "role", ID: role.ID}
	action := s.actions[objectKey(ref)]
	if action.Operation == PlanUnchanged {
		s.receipt.Counts.Unchanged++
		return s.appendExistingMapping(ref)
	}
	if action.Operation == PlanArchiveCandidate {
		return nil
	}
	instructions := s.artifacts[role.Instructions.Digest].body
	mapping, mapped := s.mappings[objectKey(ref)]
	var target db.Agent
	var err error
	if mapped && mapping.ArchivedAt.Valid {
		return fmt.Errorf("%w: archived role mapping cannot be reused", ErrApplyConflict)
	}
	if mapped {
		if err := s.ensureNameAvailable(ctx, "agent", role.DisplayName, mapping.TargetID); err != nil {
			return err
		}
		target, err = s.q.UpdateRoleSourceAgent(ctx, db.UpdateRoleSourceAgentParams{
			Name: role.DisplayName, Description: roleDescription(role), RuntimeMode: s.runtimeMode, RuntimeID: s.source.RuntimeID,
			Instructions: instructions, ID: mapping.TargetID, WorkspaceID: s.workspaceID,
		})
	} else {
		if err := s.ensureNameAvailable(ctx, "agent", role.DisplayName, pgtype.UUID{}); err != nil {
			return err
		}
		target, err = s.q.CreateRoleSourceAgent(ctx, db.CreateRoleSourceAgentParams{
			WorkspaceID: s.workspaceID, Name: role.DisplayName, Description: roleDescription(role), RuntimeMode: s.runtimeMode,
			RuntimeID: s.source.RuntimeID, OwnerID: s.actorID, Instructions: instructions,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: mapped role target is missing or archived", ErrApplyConflict)
		}
		return err
	}
	if err := s.upsertMapping(ctx, ref, "agent", target.ID, action.AfterDigest, []string{"name", "description", "runtime_binding", "instructions"}, pgtype.Timestamptz{}); err != nil {
		return err
	}
	if mapped {
		s.receipt.Counts.Updated++
	} else {
		s.receipt.Counts.Created++
	}
	return nil
}

func (s *materializationState) materializeSkill(ctx context.Context, role Role, skill Skill) error {
	ref := ObjectRef{Kind: "skill", ParentID: role.ID, ID: skill.ID}
	action := s.actions[objectKey(ref)]
	if action.Operation == PlanUnchanged {
		s.receipt.Counts.Unchanged++
		return s.appendExistingMapping(ref)
	}
	if action.Operation == PlanArchiveCandidate {
		return nil
	}
	roleMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
	if !ok || roleMapping.ArchivedAt.Valid {
		return fmt.Errorf("%w: role mapping is missing for skill %s", ErrApplyConflict, skill.ID)
	}
	mapping, mapped := s.mappings[objectKey(ref)]
	config, err := json.Marshal(map[string]any{"managed_by": "role_source", "source_id": util.UUIDToString(s.source.ID), "source_role_id": role.ID, "source_skill_id": skill.ID, "version": skill.Version})
	if err != nil {
		return err
	}
	var target db.Skill
	if mapped {
		if err := s.ensureNameAvailable(ctx, "skill", skill.Name, mapping.TargetID); err != nil {
			return err
		}
		target, err = s.q.UpdateRoleSourceSkill(ctx, db.UpdateRoleSourceSkillParams{
			Name: skill.Name, Description: "Managed by role source", Content: s.artifacts[skill.Entrypoint.Digest].body,
			ID: mapping.TargetID, WorkspaceID: s.workspaceID,
		})
	} else {
		if err := s.ensureNameAvailable(ctx, "skill", skill.Name, pgtype.UUID{}); err != nil {
			return err
		}
		target, err = s.q.CreateRoleSourceSkill(ctx, db.CreateRoleSourceSkillParams{
			WorkspaceID: s.workspaceID, Name: skill.Name, Description: "Managed by role source",
			Content: s.artifacts[skill.Entrypoint.Digest].body, Config: config, CreatedBy: s.actorID,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: mapped skill target is missing", ErrApplyConflict)
		}
		return err
	}
	if err := s.q.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: roleMapping.TargetID, SkillID: target.ID}); err != nil {
		return err
	}
	if err := s.upsertMapping(ctx, ref, "skill", target.ID, action.AfterDigest, []string{"name", "description", "content", "agent_binding"}, pgtype.Timestamptz{}); err != nil {
		return err
	}
	if mapped {
		s.receipt.Counts.Updated++
	} else {
		s.receipt.Counts.Created++
	}
	return nil
}

func (s *materializationState) materializeAutomation(ctx context.Context, role Role, automation Automation) error {
	ref := ObjectRef{Kind: "automation", ParentID: role.ID, ID: automation.ID}
	action := s.actions[objectKey(ref)]
	if action.Operation == PlanUnchanged {
		s.receipt.Counts.Unchanged++
		return s.appendExistingMapping(ref)
	}
	if action.Operation == PlanArchiveCandidate {
		return nil
	}
	roleMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
	if !ok || roleMapping.ArchivedAt.Valid {
		return fmt.Errorf("%w: role mapping is missing for automation %s", ErrApplyConflict, automation.ID)
	}
	title := automationTitle(role.DisplayName, automation.Name)
	mapping, mapped := s.mappings[objectKey(ref)]
	var target db.Autopilot
	var err error
	description := pgtype.Text{String: s.artifacts[automation.Prompt.Digest].body, Valid: true}
	if mapped {
		if err := s.ensureNameAvailable(ctx, "autopilot", title, mapping.TargetID); err != nil {
			return err
		}
		target, err = s.q.UpdateRoleSourceAutopilot(ctx, db.UpdateRoleSourceAutopilotParams{
			Title: title, Description: description, ID: mapping.TargetID, WorkspaceID: s.workspaceID, AssigneeID: roleMapping.TargetID,
		})
	} else {
		if err := s.ensureNameAvailable(ctx, "autopilot", title, pgtype.UUID{}); err != nil {
			return err
		}
		target, err = s.q.CreateRoleSourceAutopilot(ctx, db.CreateRoleSourceAutopilotParams{
			WorkspaceID: s.workspaceID, Title: title, Description: description, AssigneeID: roleMapping.TargetID, CreatedByID: s.actorID,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: mapped automation target is missing or retargeted", ErrApplyConflict)
		}
		return err
	}
	triggerID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(util.UUIDToString(s.source.ID)+"/"+role.ID+"/"+automation.ID))
	triggerPG, err := util.ParseUUID(triggerID.String())
	if err != nil {
		return err
	}
	if _, err := s.q.UpsertRoleSourceScheduleTrigger(ctx, db.UpsertRoleSourceScheduleTriggerParams{
		ID: triggerPG, AutopilotID: target.ID, CronExpression: pgtype.Text{String: automation.Schedule, Valid: true},
		Timezone: pgtype.Text{String: automation.Timezone, Valid: true}, Label: pgtype.Text{String: "Managed by role source", Valid: true}, PublishedByID: s.actorID,
	}); err != nil {
		return err
	}
	if err := s.upsertMapping(ctx, ref, "autopilot", target.ID, action.AfterDigest, []string{"title", "description", "schedule"}, pgtype.Timestamptz{}); err != nil {
		return err
	}
	if mapped {
		s.receipt.Counts.Updated++
	} else {
		s.receipt.Counts.Created++
	}
	return nil
}

func (s *materializationState) materializeArchives(ctx context.Context) error {
	for _, action := range s.plan.Actions {
		if action.Operation != PlanArchiveCandidate {
			continue
		}
		if s.decisions[objectKey(action.Ref)] == ArchiveDecisionRetain {
			s.receipt.Counts.Retained++
			_ = s.appendExistingMapping(action.Ref)
			continue
		}
		mapping, ok := s.mappings[objectKey(action.Ref)]
		if !ok || mapping.ArchivedAt.Valid {
			return fmt.Errorf("%w: archive target mapping is missing", ErrApplyConflict)
		}
		switch action.Ref.Kind {
		case "automation":
			if err := s.q.ArchiveAutopilot(ctx, mapping.TargetID); err != nil {
				return err
			}
		case "role":
			if _, err := s.q.ArchiveAgent(ctx, db.ArchiveAgentParams{ID: mapping.TargetID, ArchivedBy: s.actorID}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unsupported archive kind %q", ErrMaterializationBlocked, action.Ref.Kind)
		}
		archivedAt := pgtype.Timestamptz{Time: s.now().UTC(), Valid: true}
		if err := s.upsertMapping(ctx, action.Ref, mapping.TargetKind, mapping.TargetID, action.BeforeDigest, ownershipMask(mapping), archivedAt); err != nil {
			return err
		}
		s.receipt.Counts.Archived++
	}
	return nil
}

func (s *materializationState) upsertMapping(ctx context.Context, ref ObjectRef, targetKind string, targetID pgtype.UUID, digest string, mask []string, archivedAt pgtype.Timestamptz) error {
	body, err := json.Marshal(mask)
	if err != nil {
		return err
	}
	row, err := s.q.UpsertRoleSourceObjectMapping(ctx, db.UpsertRoleSourceObjectMappingParams{
		SourceID: s.source.ID, WorkspaceID: s.workspaceID, SourceKind: ref.Kind, SourceParentID: ref.ParentID,
		SourceObjectID: ref.ID, TargetKind: targetKind, TargetID: targetID, OwnershipMask: body,
		LastAppliedDigest: digest, LastSnapshotDigest: s.snapshot.SnapshotDigest, ArchivedAt: archivedAt,
	})
	if err != nil {
		return err
	}
	s.mappings[objectKey(ref)] = row
	s.receipt.Mappings = append(s.receipt.Mappings, ApplyMapping{Source: ref, Target: targetKind, ID: util.UUIDToString(targetID)})
	return nil
}

func (s *materializationState) appendExistingMapping(ref ObjectRef) error {
	mapping, ok := s.mappings[objectKey(ref)]
	if !ok {
		return fmt.Errorf("%w: unchanged object has no materialization mapping", ErrApplyConflict)
	}
	s.receipt.Mappings = append(s.receipt.Mappings, ApplyMapping{Source: ref, Target: mapping.TargetKind, ID: util.UUIDToString(mapping.TargetID)})
	return nil
}

func ownershipMask(mapping db.RoleSourceObjectMapping) []string {
	var mask []string
	if err := json.Unmarshal(mapping.OwnershipMask, &mask); err != nil {
		return []string{}
	}
	return mask
}

func (s *materializationState) ensureNameAvailable(ctx context.Context, kind, name string, allowed pgtype.UUID) error {
	var found pgtype.UUID
	var err error
	switch kind {
	case "agent":
		found, err = s.q.FindRoleSourceAgentNameConflict(ctx, db.FindRoleSourceAgentNameConflictParams{WorkspaceID: s.workspaceID, Name: name})
	case "skill":
		found, err = s.q.FindRoleSourceSkillNameConflict(ctx, db.FindRoleSourceSkillNameConflictParams{WorkspaceID: s.workspaceID, Name: name})
	case "autopilot":
		found, err = s.q.FindRoleSourceAutopilotTitleConflict(ctx, db.FindRoleSourceAutopilotTitleConflictParams{WorkspaceID: s.workspaceID, Title: name})
	default:
		return errors.New("invalid target kind")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if allowed.Valid && found == allowed {
		return nil
	}
	return fmt.Errorf("%w: unmanaged %s already uses name %q", ErrApplyConflict, kind, name)
}

func roleDescription(role Role) string {
	parts := []string{"Managed by role source"}
	if role.Version != "" {
		parts = append(parts, "version "+role.Version)
	}
	if role.Lifecycle != "" {
		parts = append(parts, "lifecycle "+role.Lifecycle)
	}
	value := strings.Join(parts, "; ")
	if len(value) > 255 {
		return value[:255]
	}
	return value
}

func automationTitle(roleName, automationName string) string {
	value := roleName + " · " + automationName
	if len(value) > 200 {
		return value[:200]
	}
	return value
}

func encodeApplyReceipt(receipt ApplyReceipt) ([]byte, string, error) {
	canonical := receipt
	canonical.ReceiptDigest = ""
	canonicalBody, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", err
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonicalBody)
	return body, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeApplyReceipt(row db.RoleSourceApply) (ApplyReceipt, error) {
	var receipt ApplyReceipt
	if row.Status != "succeeded" || !row.ReceiptDigest.Valid || len(row.Receipt) == 0 {
		return receipt, errors.New("apply does not contain a completed receipt")
	}
	if err := json.Unmarshal(row.Receipt, &receipt); err != nil {
		return receipt, err
	}
	_, digest, err := encodeApplyReceipt(receipt)
	if err != nil {
		return receipt, err
	}
	if receipt.ReceiptDigest != row.ReceiptDigest.String || digest != row.ReceiptDigest.String || receipt.ApplyID != util.UUIDToString(row.ID) || receipt.PlanDigest != row.PlanDigest || receipt.SnapshotDigest != row.SnapshotDigest {
		return receipt, errors.New("stored apply receipt does not match indexed apply record")
	}
	return receipt, nil
}
