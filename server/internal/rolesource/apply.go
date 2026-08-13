package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/autopilotlock"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"golang.org/x/sync/errgroup"
)

const (
	ApplyReceiptContractVersion      = "1.2"
	maxApplyArtifacts                = 20_000
	maxApplyArtifactBytes            = 128 << 20
	maxConcurrentArtifactReads       = 16
	capabilityVersionBatchSize       = 500
	capabilityVersionBatchBytes      = 2 << 20
	materializedAgentBatchSize       = 250
	materializedAgentBatchBytes      = 64 << 20
	materializedSkillBatchSize       = 250
	materializedSkillBatchBytes      = 64 << 20
	materializedSkillFileBatchSize   = 250
	materializedSkillFileBatchBytes  = 64 << 20
	materializedSkillFileTargetLimit = 40_000
	materializedSkillFileTargetBytes = 16 << 20
	materializedAutomationBatchSize  = 250
	materializedAutomationBatchBytes = 64 << 20
	materializedMappingBatchSize     = 500
	materializedMappingBatchBytes    = 8 << 20
	materializedAgentSkillBatchSize  = 1_000
	materializedAgentSkillBatchBytes = 2 << 20
	materializationNameMaxBytes      = 16 << 20
)

var (
	ErrInvalidApplyRequest     = errors.New("invalid role source apply request")
	ErrApplyConflict           = errors.New("role source apply conflicts with current state")
	ErrMaterializationBlocked  = errors.New("role source materialization is not supported for this snapshot")
	ErrMaterializationOverload = errors.New("role source materialization capacity is exhausted")
)

type ApplyPlanInput struct {
	WorkspaceID       string
	SourceID          string
	PlanDigest        string
	ApprovalID        string
	RequestKey        string
	ActorUserID       string
	SecretTransferIDs map[string]string
}

type ApplyCounts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Adopted   int `json:"adopted,omitempty"`
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
	ContractVersion    string                  `json:"contract_version"`
	Mode               string                  `json:"mode,omitempty"`
	ApplyID            string                  `json:"apply_id"`
	SourceID           string                  `json:"source_id"`
	WorkspaceID        string                  `json:"workspace_id"`
	SnapshotDigest     string                  `json:"snapshot_digest"`
	FromSnapshotDigest string                  `json:"from_snapshot_digest,omitempty"`
	PlanDigest         string                  `json:"plan_digest"`
	ApprovalID         string                  `json:"approval_id"`
	Counts             ApplyCounts             `json:"counts"`
	Mappings           []ApplyMapping          `json:"mappings"`
	SecretTransfers    []SecretTransferReceipt `json:"secret_transfers,omitempty"`
	ReceiptDigest      string                  `json:"receipt_digest"`
}

type SecretTransferReceipt struct {
	RoleID         string `json:"role_id"`
	TransferID     string `json:"transfer_id"`
	EnvelopeDigest string `json:"envelope_digest"`
}

type ApplyHistoryItem struct {
	Row     db.RoleSourceApply
	Receipt ApplyReceipt
}

type verifiedArtifact struct {
	ref        ArtifactRef
	storageKey string
	body       string
}

type materializationState struct {
	control            *ControlPlane
	q                  *db.Queries
	tx                 pgx.Tx
	workspaceID        pgtype.UUID
	source             db.RoleSource
	actorID            pgtype.UUID
	snapshot           Snapshot
	plan               Plan
	decisions          map[string]ArchiveDecision
	adoptions          map[string]AdoptionActionDecision
	actions            map[string]PlanAction
	artifacts          map[string]verifiedArtifact
	capabilities       map[string]Capability
	mappings           map[string]db.RoleSourceObjectMapping
	secretPayloads     map[string]SecretEnvelopePayload
	secretTransfers    map[string]db.RoleSourceSecretTransfer
	runtimeMode        string
	now                func() time.Time
	receipt            *ApplyReceipt
	pendingMappings    map[string]pendingRoleSourceMapping
	pendingAgentSkills map[string]pendingRoleSourceAgentSkill
	skillFilesReady    bool
	skillFiles         map[string]map[string]bool
	skillFilesInitial  map[string]map[string]bool
	pendingSkillFiles  map[string]pendingRoleSourceSkillFileMutation
}

type pendingRoleSourceMapping struct {
	SourceKind         string     `json:"source_kind"`
	SourceParentID     string     `json:"source_parent_id"`
	SourceObjectID     string     `json:"source_object_id"`
	TargetKind         string     `json:"target_kind"`
	TargetID           string     `json:"target_id"`
	OwnershipMask      []string   `json:"ownership_mask"`
	LastAppliedDigest  string     `json:"last_applied_digest"`
	LastSnapshotDigest string     `json:"last_snapshot_digest"`
	ArchivedAt         *time.Time `json:"archived_at"`
}

type pendingRoleSourceSkillFileMutation struct {
	SkillID   string `json:"skill_id"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Content   string `json:"content"`
}

type pendingRoleSourceSkillFileTarget struct {
	SkillID string `json:"skill_id"`
	Path    string `json:"path"`
}

type pendingRoleSourceAgentSkill struct {
	AgentID string `json:"agent_id"`
	SkillID string `json:"skill_id"`
}

type pendingRoleSourceName struct {
	TargetKind string `json:"target_kind"`
	Name       string `json:"name"`
	AllowedID  string `json:"allowed_id,omitempty"`
}

type pendingRoleSourceCapabilityVersion struct {
	CapabilityID string          `json:"capability_id"`
	Version      string          `json:"version"`
	ObjectDigest string          `json:"object_digest"`
	Definition   json.RawMessage `json:"definition"`
}

type pendingRoleSourceAgent struct {
	Ref          ObjectRef `json:"-"`
	ID           string    `json:"id"`
	Operation    string    `json:"operation"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	RuntimeMode  string    `json:"runtime_mode"`
	RuntimeID    string    `json:"runtime_id"`
	OwnerID      string    `json:"owner_id"`
	Instructions string    `json:"instructions"`
	ObjectDigest string    `json:"-"`
}

type pendingRoleSourceSkill struct {
	Ref          ObjectRef         `json:"-"`
	AgentID      pgtype.UUID       `json:"-"`
	ID           string            `json:"id"`
	Operation    string            `json:"operation"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Content      string            `json:"content"`
	Config       json.RawMessage   `json:"config"`
	CreatedBy    string            `json:"created_by"`
	ObjectDigest string            `json:"-"`
	DesiredFiles map[string]string `json:"-"`
}

type pendingRoleSourceAutomation struct {
	Ref            ObjectRef `json:"-"`
	ID             string    `json:"id"`
	Operation      string    `json:"operation"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	AssigneeID     string    `json:"assignee_id"`
	CreatedByID    string    `json:"created_by_id"`
	TriggerID      string    `json:"trigger_id"`
	CronExpression string    `json:"cron_expression"`
	Timezone       string    `json:"timezone"`
	Label          string    `json:"label"`
	ObjectDigest   string    `json:"-"`
}

type applyPreflight struct {
	snapshotDigest string
	artifacts      map[string]verifiedArtifact
	existing       *db.RoleSourceApply
	receipt        *ApplyReceipt
}

type applyAttemptTracker struct {
	recordable       bool
	workspaceID      pgtype.UUID
	sourceID         pgtype.UUID
	approvalID       pgtype.UUID
	actorID          pgtype.UUID
	planDigest       string
	requestKeyDigest string
	mode             string
	stage            string
}

func (c *ControlPlane) ApplyPlan(ctx context.Context, input ApplyPlanInput) (db.RoleSourceApply, ApplyReceipt, error) {
	tracker := applyAttemptTracker{mode: "unknown", stage: "preflight"}
	row, receipt, err := c.applyPlan(ctx, input, &tracker)
	if err != nil && tracker.recordable && tracker.stage == "commit" {
		reconciledRow, reconciledReceipt, outcome, reconcileErr := c.reconcileApplyCommit(ctx, input, tracker)
		if c.applyMetrics != nil {
			c.applyMetrics.RecordApplyCommitReconciliation(outcome)
		}
		if reconcileErr == nil {
			return reconciledRow, reconciledReceipt, nil
		}
		if errors.Is(reconcileErr, ErrIdempotencyConflict) {
			err = reconcileErr
		}
	}
	if err != nil && tracker.recordable {
		failureCode := classifyApplyFailure(err)
		if c.applyMetrics != nil {
			c.applyMetrics.RecordApplyError(tracker.mode, tracker.stage, failureCode)
		}
		c.recordApplyFailure(ctx, tracker, failureCode)
	}
	return row, receipt, err
}

func (c *ControlPlane) reconcileApplyCommit(parent context.Context, input ApplyPlanInput, tracker applyAttemptTracker) (db.RoleSourceApply, ApplyReceipt, string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	row, err := c.queries().GetRoleSourceApplyByRequest(ctx, db.GetRoleSourceApplyByRequestParams{
		SourceID: tracker.sourceID, WorkspaceID: tracker.workspaceID, RequestKey: strings.TrimSpace(input.RequestKey),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.RoleSourceApply{}, ApplyReceipt{}, "not_found", err
	}
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, "query_failed", err
	}
	input.WorkspaceID = util.UUIDToString(tracker.workspaceID)
	input.SourceID = util.UUIDToString(tracker.sourceID)
	input.ActorUserID = util.UUIDToString(tracker.actorID)
	input.ApprovalID = util.UUIDToString(tracker.approvalID)
	if err := normalizeApplySecretTransferIDs(&input); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, "conflict", ErrIdempotencyConflict
	}
	receipt, err := matchReconciledApply(row, input, tracker)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, "conflict", err
	}
	return row, receipt, "confirmed_succeeded", nil
}

func matchReconciledApply(row db.RoleSourceApply, input ApplyPlanInput, tracker applyAttemptTracker) (ApplyReceipt, error) {
	if row.SourceID != tracker.sourceID || row.WorkspaceID != tracker.workspaceID || row.ActorUserID != tracker.actorID ||
		row.PlanDigest != tracker.planDigest || row.Mode != tracker.mode || row.Status != "succeeded" {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	receipt, err := decodeApplyReceipt(row)
	if err != nil || receipt.ApprovalID != input.ApprovalID || receipt.SourceID != input.SourceID || receipt.WorkspaceID != input.WorkspaceID ||
		receipt.PlanDigest != tracker.planDigest || !receiptSecretTransfersMatchInput(receipt.SecretTransfers, input.SecretTransferIDs) {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	return receipt, nil
}

func (c *ControlPlane) applyPlan(ctx context.Context, input ApplyPlanInput, tracker *applyAttemptTracker) (db.RoleSourceApply, ApplyReceipt, error) {
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
	requestKeyDigest := sha256.Sum256([]byte(input.RequestKey))
	*tracker = applyAttemptTracker{
		recordable: true, workspaceID: workspaceID, sourceID: sourceID,
		approvalID: approvalID, actorID: actorID, planDigest: input.PlanDigest,
		requestKeyDigest: "sha256:" + hex.EncodeToString(requestKeyDigest[:]),
		mode:             "unknown", stage: "preflight",
	}
	if err := normalizeApplySecretTransferIDs(&input); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if c.artifacts == nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, errors.New("role source artifact reader is not configured")
	}
	if !c.acquireMaterializeSlot() {
		return db.RoleSourceApply{}, ApplyReceipt{}, ErrMaterializationOverload
	}
	defer c.releaseMaterializeSlot()

	// Object storage can be slow and a large source can reference thousands of
	// small entrypoints. Verify every materialized body before taking
	// workspace/source row locks; the transaction reloads and revalidates all
	// authoritative rows and the exact content-addressed snapshot before use.
	preflight, err := c.preflightApply(ctx, input, workspaceID, sourceID, actorID, approvalID)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if preflight.existing != nil && preflight.receipt != nil {
		return *preflight.existing, *preflight.receipt, nil
	}

	tracker.stage = "transaction"
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
	tracker.mode = "apply"
	if plan.Mode == PlanModeRollback {
		tracker.mode = "rollback"
	}
	existing, existingErr := qtx.GetRoleSourceApplyByRequest(ctx, db.GetRoleSourceApplyByRequestParams{
		SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
	})
	if existingErr == nil {
		receipt, matchErr := matchIdempotentApply(existing, plan, input, actorID)
		if matchErr != nil {
			return db.RoleSourceApply{}, ApplyReceipt{}, matchErr
		}
		tracker.stage = "commit"
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
	decisions, adoptions, err := decodeApprovedDecisions(plan, approval)
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
	if snapshot.SnapshotDigest != preflight.snapshotDigest {
		return db.RoleSourceApply{}, ApplyReceipt{}, fmt.Errorf("%w: preflight snapshot changed", ErrApplyConflict)
	}
	applyMode := "apply"
	if plan.Mode == PlanModeRollback {
		applyMode = "rollback"
	}
	secretPayloads, secretTransfers, err := c.loadApplySecretTransfers(ctx, qtx, source, snapshot, plan, approvalID, input.SecretTransferIDs)
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	defer clearSecretPayloadMap(secretPayloads)

	applyID, err := newPGUUID()
	if err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	applyRow, err := qtx.InsertRoleSourceApply(ctx, db.InsertRoleSourceApplyParams{
		ID: applyID, SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
		Mode: applyMode, SnapshotDigest: plan.ToSnapshotDigest, PlanDigest: plan.PlanDigest, ActorUserID: actorID,
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
		tracker.stage = "commit"
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

	tracker.stage = "materialization"
	artifacts := preflight.artifacts
	if err := validatePreflightArtifactLedger(ctx, qtx, workspaceID, artifacts); err != nil {
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
	if err := lockAndValidateAdoptionTargets(ctx, qtx, workspaceID, snapshot, plan, adoptions); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	mappings := mappingIndex(mappingRows)
	if err := applyAdoptionMappings(mappings, adoptions, sourceID, workspaceID); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if err := validateMaterializationNames(ctx, qtx, workspaceID, snapshot, plan, mappings); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	receipt := ApplyReceipt{
		ContractVersion: ApplyReceiptContractVersion, Mode: applyMode, ApplyID: util.UUIDToString(applyRow.ID),
		SourceID: input.SourceID, WorkspaceID: input.WorkspaceID, SnapshotDigest: plan.ToSnapshotDigest,
		FromSnapshotDigest: plan.FromSnapshotDigest,
		PlanDigest:         plan.PlanDigest, ApprovalID: input.ApprovalID, Mappings: []ApplyMapping{}, SecretTransfers: []SecretTransferReceipt{},
	}
	state := materializationState{
		control: c, q: qtx, tx: tx, workspaceID: workspaceID, source: source, actorID: actorID, snapshot: snapshot, plan: plan,
		decisions: decisions, adoptions: adoptions, actions: actionIndex(plan), artifacts: artifacts, capabilities: capabilityIndex(snapshot.Manifest.Capabilities), mappings: mappings,
		secretPayloads: secretPayloads, secretTransfers: secretTransfers,
		runtimeMode: runtime.RuntimeMode, now: c.now, receipt: &receipt,
		pendingMappings:    make(map[string]pendingRoleSourceMapping),
		pendingAgentSkills: make(map[string]pendingRoleSourceAgentSkill),
	}
	if err := state.materialize(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if err := state.consumeSecretTransfers(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	tracker.stage = "finalize"
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
	sort.Slice(receipt.SecretTransfers, func(i, j int) bool { return receipt.SecretTransfers[i].RoleID < receipt.SecretTransfers[j].RoleID })
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
	eventType := "apply_succeeded"
	if plan.Mode == PlanModeRollback {
		eventType = "rollback_succeeded"
	}
	if err := c.appendAudit(ctx, qtx, source, eventType, AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: receipt.ApplyID, SnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest,
		ReceiptDigest: receiptDigest, Result: "succeeded", CreateCount: receipt.Counts.Created,
		UpdateCount: receipt.Counts.Updated, AdoptCount: receipt.Counts.Adopted, UnchangedCount: receipt.Counts.Unchanged,
		ArchiveCount: receipt.Counts.Archived,
	}); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	tracker.stage = "commit"
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	return applyRow, receipt, nil
}

func classifyApplyFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request_cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrMaterializationOverload):
		return "capacity_exhausted"
	case errors.Is(err, ErrMaterializationBlocked):
		return "materialization_blocked"
	case errors.Is(err, ErrApplyConflict), errors.Is(err, ErrIdempotencyConflict):
		return "state_conflict"
	case errors.Is(err, ErrInvalidApplyRequest):
		return "invalid_request"
	case errors.Is(err, ErrInvalidSecretEnvelope), errors.Is(err, ErrExpiredSecretEnvelope):
		return "invalid_secret_transfer"
	case errors.Is(err, ErrSecretStoreUnavailable):
		return "dependency_unavailable"
	case errors.Is(err, pgx.ErrNoRows):
		return "resource_not_found"
	default:
		return "internal_failure"
	}
}

func (c *ControlPlane) recordApplyFailure(parent context.Context, tracker applyAttemptTracker, failureCode string) {
	id, err := newPGUUID()
	if err != nil {
		slog.Warn("role source apply failure audit id generation failed", "stage", tracker.stage)
		c.recordApplyFailureAuditMetric(tracker, failureCode, "id_generation_failed")
		return
	}
	params := newApplyFailureParams(id, tracker, failureCode, c.now())
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer cancel()
	_, err = db.New(c.database).InsertRoleSourceApplyFailure(ctx, params)
	if err != nil {
		slog.Warn("role source apply failure audit persist failed", "stage", params.FailureStage, "failure_code", params.FailureCode, "error", err)
		c.recordApplyFailureAuditMetric(tracker, failureCode, "persist_failed")
		return
	}
	c.recordApplyFailureAuditMetric(tracker, failureCode, "persisted")
}

func (c *ControlPlane) recordApplyFailureAuditMetric(tracker applyAttemptTracker, failureCode, outcome string) {
	if c.applyMetrics != nil {
		c.applyMetrics.RecordApplyFailureAudit(tracker.mode, tracker.stage, failureCode, outcome)
	}
}

func newApplyFailureParams(id pgtype.UUID, tracker applyAttemptTracker, failureCode string, occurredAt time.Time) db.InsertRoleSourceApplyFailureParams {
	return db.InsertRoleSourceApplyFailureParams{
		ID: id, WorkspaceID: tracker.workspaceID, SourceID: tracker.sourceID,
		PlanDigest: tracker.planDigest, ApprovalID: tracker.approvalID, ActorUserID: tracker.actorID,
		RequestKeyDigest: tracker.requestKeyDigest, Mode: tracker.mode,
		FailureStage: tracker.stage, FailureCode: failureCode,
		OccurredAt: pgtype.Timestamptz{Time: occurredAt.UTC(), Valid: true},
	}
}

func (c *ControlPlane) ListApplyHistory(ctx context.Context, workspaceIDText, sourceIDText string, limit int32) ([]ApplyHistoryItem, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("apply history limit must be between 1 and 100")
	}
	rows, err := c.queries().ListSucceededRoleSourceApplies(ctx, db.ListSucceededRoleSourceAppliesParams{
		SourceID: sourceID, WorkspaceID: workspaceID, ResultLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ApplyHistoryItem, 0, len(rows))
	for _, row := range rows {
		receipt, err := decodeApplyReceipt(row)
		if err != nil {
			return nil, fmt.Errorf("validate apply history receipt %s: %w", util.UUIDToString(row.ID), err)
		}
		items = append(items, ApplyHistoryItem{Row: row, Receipt: receipt})
	}
	return items, nil
}

func (c *ControlPlane) ListApplyFailures(ctx context.Context, workspaceIDText, sourceIDText string, limit int32) ([]db.RoleSourceApplyFailure, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("apply failure limit must be between 1 and 100")
	}
	return c.queries().ListRoleSourceApplyFailures(ctx, db.ListRoleSourceApplyFailuresParams{
		WorkspaceID: workspaceID, SourceID: sourceID, ResultLimit: limit,
	})
}

func matchIdempotentApply(existing db.RoleSourceApply, plan Plan, input ApplyPlanInput, actorID pgtype.UUID) (ApplyReceipt, error) {
	expectedMode := "apply"
	if plan.Mode == PlanModeRollback {
		expectedMode = "rollback"
	}
	if existing.Mode != expectedMode || existing.SnapshotDigest != plan.ToSnapshotDigest || existing.PlanDigest != plan.PlanDigest || existing.ActorUserID != actorID || existing.Status != "succeeded" {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	receipt, err := decodeApplyReceipt(existing)
	if err != nil || receipt.ApprovalID != input.ApprovalID || receipt.SourceID != input.SourceID || receipt.WorkspaceID != input.WorkspaceID ||
		!receiptSecretTransfersMatchInput(receipt.SecretTransfers, input.SecretTransferIDs) {
		return ApplyReceipt{}, ErrIdempotencyConflict
	}
	return receipt, nil
}

func normalizeApplySecretTransferIDs(input *ApplyPlanInput) error {
	if len(input.SecretTransferIDs) > maxSecretEnvelopeValues {
		return fmt.Errorf("%w: too many secret transfers", ErrInvalidApplyRequest)
	}
	normalized := make(map[string]string, len(input.SecretTransferIDs))
	for roleID, transferIDText := range input.SecretTransferIDs {
		if !stableIDPattern.MatchString(roleID) {
			return fmt.Errorf("%w: invalid secret transfer role id", ErrInvalidApplyRequest)
		}
		transferID, err := util.ParseUUID(transferIDText)
		if err != nil {
			return fmt.Errorf("%w: invalid secret transfer id", ErrInvalidApplyRequest)
		}
		normalized[roleID] = util.UUIDToString(transferID)
	}
	input.SecretTransferIDs = normalized
	return nil
}

func receiptSecretTransfersMatchInput(receipts []SecretTransferReceipt, input map[string]string) bool {
	if len(receipts) != len(input) {
		return false
	}
	seen := make(map[string]bool, len(receipts))
	for _, receipt := range receipts {
		if seen[receipt.RoleID] || input[receipt.RoleID] != receipt.TransferID || !sha256Pattern.MatchString(receipt.EnvelopeDigest) {
			return false
		}
		seen[receipt.RoleID] = true
	}
	return true
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

func decodeApprovedDecisions(plan Plan, approval db.RoleSourcePlanApproval) (map[string]ArchiveDecision, map[string]AdoptionActionDecision, error) {
	if approval.Decision != "approved" {
		return nil, nil, errors.New("apply requires an approved plan")
	}
	var decisions ApprovalDecisions
	if err := json.Unmarshal(approval.Decisions, &decisions); err != nil {
		return nil, nil, err
	}
	if err := ValidateApprovalDecisions(plan, approval.Decision, &decisions); err != nil {
		return nil, nil, err
	}
	archives := make(map[string]ArchiveDecision, len(decisions.Archives))
	for _, decision := range decisions.Archives {
		archives[objectKey(decision.Ref)] = decision.Decision
	}
	adoptions := make(map[string]AdoptionActionDecision, len(decisions.Adoptions))
	for _, decision := range decisions.Adoptions {
		adoptions[objectKey(decision.Ref)] = decision
	}
	return archives, adoptions, nil
}

func validateMaterializationScope(snapshot Snapshot, plan Plan, decisions map[string]ArchiveDecision) error {
	capabilities := make(map[string]Capability, len(snapshot.Manifest.Capabilities))
	for _, capability := range snapshot.Manifest.Capabilities {
		capabilities[capability.ID] = capability
	}
	for _, role := range snapshot.Manifest.Roles {
		if role.Profile != nil {
			return fmt.Errorf("%w: role profiles require a dedicated target field", ErrMaterializationBlocked)
		}
		for _, binding := range role.CapabilityBindings {
			if binding.PermissionMode != "read-only" {
				return fmt.Errorf("%w: capability binding %s requests %s without a hard runtime permission boundary", ErrMaterializationBlocked, binding.CapabilityID, binding.PermissionMode)
			}
			for _, requirement := range capabilities[binding.CapabilityID].Requirements.Adapters {
				if requirement.Required {
					return fmt.Errorf("%w: capability binding %s requires unresolved adapter %s", ErrMaterializationBlocked, binding.CapabilityID, requirement.ID)
				}
			}
		}
	}
	for _, action := range plan.Actions {
		switch action.Ref.Kind {
		case "role", "skill", "automation", "capability", "capability_binding":
		case "environment", "mcp":
		default:
			return fmt.Errorf("%w: unsupported object kind %q", ErrMaterializationBlocked, action.Ref.Kind)
		}
		if action.Operation == PlanArchiveCandidate && decisions[objectKey(action.Ref)] == ArchiveDecisionArchive && action.Ref.Kind != "role" && action.Ref.Kind != "automation" && action.Ref.Kind != "capability_binding" {
			return fmt.Errorf("%w: %s archive is not reversible yet; choose retain", ErrMaterializationBlocked, action.Ref.Kind)
		}
	}
	return nil
}

func (c *ControlPlane) loadApplySecretTransfers(ctx context.Context, q *db.Queries, source db.RoleSource, snapshot Snapshot, plan Plan, approvalID pgtype.UUID, transferIDs map[string]string) (map[string]SecretEnvelopePayload, map[string]db.RoleSourceSecretTransfer, error) {
	payloads := make(map[string]SecretEnvelopePayload)
	transfers := make(map[string]db.RoleSourceSecretTransfer)
	required := 0
	for _, role := range snapshot.Manifest.Roles {
		if !roleNeedsSecretTransfer(role) {
			continue
		}
		required++
		transferIDText, ok := transferIDs[role.ID]
		if !ok {
			clearSecretPayloadMap(payloads)
			return nil, nil, fmt.Errorf("%w: role %s requires a submitted secret transfer", ErrInvalidApplyRequest, role.ID)
		}
		if len(c.secretBoxes) == 0 || c.secretKeyID == "" {
			clearSecretPayloadMap(payloads)
			return nil, nil, ErrSecretStoreUnavailable
		}
		transferID, err := util.ParseUUID(transferIDText)
		if err != nil {
			clearSecretPayloadMap(payloads)
			return nil, nil, fmt.Errorf("%w: invalid secret transfer id", ErrInvalidApplyRequest)
		}
		row, err := q.GetRoleSourceSecretTransferForApply(ctx, db.GetRoleSourceSecretTransferForApplyParams{
			ID: transferID, SourceID: source.ID, WorkspaceID: source.WorkspaceID, PlanDigest: plan.PlanDigest,
			ApprovalID: approvalID, SnapshotDigest: snapshot.SnapshotDigest, RoleID: role.ID,
		})
		if err != nil {
			clearSecretPayloadMap(payloads)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, fmt.Errorf("%w: secret transfer is missing, expired, consumed or does not match approval", ErrInvalidApplyRequest)
			}
			return nil, nil, err
		}
		claims, err := validatedStoredSecretTransfer(row)
		secretBox, keyAvailable := c.secretBoxFor(row.KeyID)
		if err != nil || claims.SnapshotDigest != snapshot.SnapshotDigest || !keyAvailable {
			clearSecretPayloadMap(payloads)
			return nil, nil, fmt.Errorf("%w: secret transfer key or claims are unavailable", ErrInvalidApplyRequest)
		}
		envelope, err := decodeApplySecretEnvelope(row.Envelope)
		if err != nil || envelope.Claims != claims {
			clearSecretPayloadMap(payloads)
			return nil, nil, fmt.Errorf("%w: stored secret envelope is invalid", ErrInvalidApplyRequest)
		}
		privateKey, err := secretBox.OpenWithAAD(row.PrivateKeyCiphertext, row.Claims)
		if err != nil {
			clearSecretPayloadMap(payloads)
			return nil, nil, fmt.Errorf("open apply secret transfer key: %w", err)
		}
		payload, openErr := OpenSecretEnvelope(privateKey, envelope, c.now().UTC())
		clear(privateKey)
		if openErr != nil {
			clearSecretPayloadMap(payloads)
			return nil, nil, openErr
		}
		if err := validateRoleSecretPayload(role, payload); err != nil {
			ClearSecretEnvelopePayload(&payload)
			clearSecretPayloadMap(payloads)
			return nil, nil, err
		}
		payloads[role.ID] = payload
		transfers[role.ID] = row
	}
	if len(transferIDs) != required {
		clearSecretPayloadMap(payloads)
		return nil, nil, fmt.Errorf("%w: secret transfer set contains an unrelated role", ErrInvalidApplyRequest)
	}
	return payloads, transfers, nil
}

func decodeApplySecretEnvelope(body []byte) (SecretEnvelope, error) {
	var envelope SecretEnvelope
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return envelope, errors.New("secret envelope contains trailing JSON")
	}
	return envelope, nil
}

func validateRoleSecretPayload(role Role, payload SecretEnvelopePayload) error {
	expectedEnvironment := make(map[string]EnvironmentKey)
	for _, declaration := range role.Environment {
		if declaration.Configured {
			expectedEnvironment[declaration.Name] = declaration
		}
	}
	if len(payload.Environment) != len(expectedEnvironment) {
		return fmt.Errorf("%w: secret environment set does not match snapshot", ErrInvalidSecretEnvelope)
	}
	for name := range payload.Environment {
		if _, ok := expectedEnvironment[name]; !ok {
			return fmt.Errorf("%w: secret environment key is absent from snapshot", ErrInvalidSecretEnvelope)
		}
	}
	expectedMCP := make(map[string]MCPServer, len(role.MCP))
	for _, declaration := range role.MCP {
		expectedMCP[declaration.ID] = declaration
	}
	if len(payload.MCPServers) != len(expectedMCP) {
		return fmt.Errorf("%w: secret MCP set does not match snapshot", ErrInvalidSecretEnvelope)
	}
	for id, definition := range payload.MCPServers {
		declaration, ok := expectedMCP[id]
		if !ok {
			return fmt.Errorf("%w: secret MCP server is absent from snapshot", ErrInvalidSecretEnvelope)
		}
		var value any
		if err := json.Unmarshal(definition, &value); err != nil {
			return fmt.Errorf("%w: invalid MCP definition", ErrInvalidSecretEnvelope)
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return err
		}
		digestValue := sha256.Sum256(canonical)
		clear(canonical)
		if declaration.DefinitionHash != "sha256:"+hex.EncodeToString(digestValue[:]) {
			return fmt.Errorf("%w: MCP definition does not match snapshot", ErrInvalidSecretEnvelope)
		}
	}
	return nil
}

func clearSecretPayloadMap(payloads map[string]SecretEnvelopePayload) {
	for roleID, payload := range payloads {
		ClearSecretEnvelopePayload(&payload)
		delete(payloads, roleID)
	}
}

func (c *ControlPlane) preflightApply(
	ctx context.Context,
	input ApplyPlanInput,
	workspaceID, sourceID, actorID, approvalID pgtype.UUID,
) (applyPreflight, error) {
	q := c.queries()
	planRow, err := q.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: input.PlanDigest,
	})
	if err != nil {
		return applyPreflight{}, err
	}
	plan, err := DecodePersistedPlan(planRow)
	if err != nil || !plan.Applyable {
		return applyPreflight{}, fmt.Errorf("%w: plan is invalid or blocked", ErrInvalidApplyRequest)
	}
	if existing, existingErr := q.GetRoleSourceApplyByRequest(ctx, db.GetRoleSourceApplyByRequestParams{
		SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
	}); existingErr == nil {
		receipt, matchErr := matchIdempotentApply(existing, plan, input, actorID)
		if matchErr != nil {
			return applyPreflight{}, matchErr
		}
		return applyPreflight{existing: &existing, receipt: &receipt}, nil
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return applyPreflight{}, existingErr
	}
	source, err := q.GetRoleSourceInWorkspace(ctx, db.GetRoleSourceInWorkspaceParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return applyPreflight{}, err
	}
	if source.State == "detached" || source.State == "paused" {
		return applyPreflight{}, fmt.Errorf("%w: source state %q cannot be applied", ErrApplyConflict, source.State)
	}
	if !snapshotCASMatches(source.CurrentSnapshotDigest, plan.FromSnapshotDigest) {
		return applyPreflight{}, fmt.Errorf("%w: plan base snapshot is stale", ErrApplyConflict)
	}
	approval, err := q.GetRoleSourcePlanApprovalByID(ctx, db.GetRoleSourcePlanApprovalByIDParams{
		ID: approvalID, SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
	})
	if err != nil {
		return applyPreflight{}, err
	}
	decisions, _, err := decodeApprovedDecisions(plan, approval)
	if err != nil {
		return applyPreflight{}, fmt.Errorf("%w: %v", ErrInvalidApplyRequest, err)
	}
	snapshotRow, err := q.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: plan.ToSnapshotDigest,
	})
	if err != nil {
		return applyPreflight{}, err
	}
	snapshot, err := DecodePersistedSnapshot(snapshotRow)
	if err != nil {
		return applyPreflight{}, fmt.Errorf("validate apply snapshot: %w", err)
	}
	if err := validateMaterializationScope(snapshot, plan, decisions); err != nil {
		return applyPreflight{}, err
	}
	artifacts, err := c.readAndVerifyArtifacts(ctx, q, workspaceID, snapshot)
	if err != nil {
		return applyPreflight{}, err
	}
	return applyPreflight{snapshotDigest: snapshot.SnapshotDigest, artifacts: artifacts}, nil
}

func (c *ControlPlane) readAndVerifyArtifacts(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, snapshot Snapshot) (map[string]verifiedArtifact, error) {
	refs, err := collectMaterializationArtifactRefs(snapshot)
	if err != nil {
		return nil, err
	}
	if len(refs) > maxApplyArtifacts {
		return nil, fmt.Errorf("%w: artifact count exceeds %d", ErrMaterializationBlocked, maxApplyArtifacts)
	}
	digests := make([]string, len(refs))
	var total int64
	for index, ref := range refs {
		digests[index] = ref.Digest
		total += ref.SizeBytes
		if total > maxApplyArtifactBytes {
			return nil, fmt.Errorf("%w: artifact bytes exceed %d", ErrMaterializationBlocked, maxApplyArtifactBytes)
		}
	}
	rows, err := q.ListRoleSourceArtifactsByDigests(ctx, db.ListRoleSourceArtifactsByDigestsParams{
		WorkspaceID: workspaceID, Digests: digests,
	})
	if err != nil {
		return nil, fmt.Errorf("load materialization artifact ledger: %w", err)
	}
	ledger := make(map[string]db.RoleSourceArtifact, len(rows))
	for _, row := range rows {
		ledger[row.Digest] = row
	}
	if len(ledger) != len(refs) {
		return nil, fmt.Errorf("%w: materialization artifact ledger is incomplete", ErrArtifactMissing)
	}
	return c.verifyMaterializationArtifactBodies(ctx, refs, ledger)
}

func (c *ControlPlane) verifyMaterializationArtifactBodies(
	ctx context.Context,
	refs []ArtifactRef,
	ledger map[string]db.RoleSourceArtifact,
) (map[string]verifiedArtifact, error) {
	result := make(map[string]verifiedArtifact, len(refs))
	var resultMu sync.Mutex
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxConcurrentArtifactReads)
	for _, ref := range refs {
		if groupCtx.Err() != nil {
			break
		}
		ref := ref
		group.Go(func() error {
			row, ok := ledger[ref.Digest]
			if !ok {
				return fmt.Errorf("%w: artifact %s is absent from ledger", ErrArtifactMissing, ref.Digest)
			}
			if row.SizeBytes != ref.SizeBytes {
				return fmt.Errorf("artifact ledger size mismatch for %s", ref.Digest)
			}
			reader, err := c.artifacts.GetReader(groupCtx, row.StorageKey)
			if err != nil {
				return fmt.Errorf("read artifact %s: %w", ref.Digest, err)
			}
			body, readErr := io.ReadAll(io.LimitReader(reader, ref.SizeBytes+1))
			closeErr := reader.Close()
			if readErr != nil {
				return fmt.Errorf("read artifact %s: %w", ref.Digest, readErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close artifact %s: %w", ref.Digest, closeErr)
			}
			sum := sha256.Sum256(body)
			actual := "sha256:" + hex.EncodeToString(sum[:])
			if int64(len(body)) != ref.SizeBytes || actual != ref.Digest {
				return fmt.Errorf("artifact body mismatch for %s", ref.Digest)
			}
			if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
				return fmt.Errorf("%w: only UTF-8 text artifacts are materialized", ErrMaterializationBlocked)
			}
			resultMu.Lock()
			result[ref.Digest] = verifiedArtifact{ref: ref, storageKey: row.StorageKey, body: string(body)}
			resultMu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

func validatePreflightArtifactLedger(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, artifacts map[string]verifiedArtifact) error {
	digests := make([]string, 0, len(artifacts))
	for digest := range artifacts {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	rows, err := q.ListRoleSourceArtifactsForApplyByDigests(ctx, db.ListRoleSourceArtifactsForApplyByDigestsParams{
		WorkspaceID: workspaceID, Digests: digests,
	})
	if err != nil {
		return fmt.Errorf("lock materialization artifact ledger: %w", err)
	}
	if len(rows) != len(artifacts) {
		return fmt.Errorf("%w: materialization artifact ledger changed after preflight", ErrArtifactMissing)
	}
	for _, row := range rows {
		artifact, ok := artifacts[row.Digest]
		if !ok || row.SizeBytes != artifact.ref.SizeBytes || row.StorageKey != artifact.storageKey {
			return fmt.Errorf("%w: materialization artifact ledger changed for %s", ErrApplyConflict, row.Digest)
		}
	}
	return nil
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

func validateMaterializationNames(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	snapshot Snapshot,
	plan Plan,
	mappings map[string]db.RoleSourceObjectMapping,
) error {
	names, err := collectMaterializationNames(snapshot, plan, mappings)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	body, err := json.Marshal(names)
	if err != nil {
		return err
	}
	if len(body) > materializationNameMaxBytes {
		return fmt.Errorf("%w: materialization name preflight exceeds %d bytes", ErrMaterializationBlocked, materializationNameMaxBytes)
	}
	conflicts, err := q.CountRoleSourceMaterializationNameConflicts(ctx, db.CountRoleSourceMaterializationNameConflictsParams{
		Names: body, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	if conflicts != 0 {
		return fmt.Errorf("%w: %d materialization target names conflict", ErrApplyConflict, conflicts)
	}
	return nil
}

func collectMaterializationNames(snapshot Snapshot, plan Plan, mappings map[string]db.RoleSourceObjectMapping) ([]pendingRoleSourceName, error) {
	actions := actionIndex(plan)
	names := make([]pendingRoleSourceName, 0, len(snapshot.Manifest.Roles))
	seen := make(map[string]ObjectRef)
	appendName := func(ref ObjectRef, targetKind, name string) error {
		action, ok := actions[objectKey(ref)]
		if !ok || action.Operation == PlanArchiveCandidate || action.Operation == PlanBlocked || action.Operation == PlanUnchanged {
			return nil
		}
		identity := targetKind + "\x00" + name
		if prior, duplicate := seen[identity]; duplicate && prior != ref {
			return fmt.Errorf("%w: source objects request the same %s name %q", ErrApplyConflict, targetKind, name)
		}
		seen[identity] = ref
		request := pendingRoleSourceName{TargetKind: targetKind, Name: name}
		if mapping, ok := mappings[objectKey(ref)]; ok && !mapping.ArchivedAt.Valid {
			if mapping.TargetKind != targetKind {
				return fmt.Errorf("%w: mapped %s name target changed kind", ErrApplyConflict, targetKind)
			}
			request.AllowedID = util.UUIDToString(mapping.TargetID)
		}
		names = append(names, request)
		return nil
	}
	for _, role := range snapshot.Manifest.Roles {
		if err := appendName(ObjectRef{Kind: "role", ID: role.ID}, "agent", role.DisplayName); err != nil {
			return nil, err
		}
		for _, skill := range role.Skills {
			if err := appendName(ObjectRef{Kind: "skill", ParentID: role.ID, ID: skill.ID}, "skill", skill.Name); err != nil {
				return nil, err
			}
		}
		for _, automation := range role.Automations {
			if err := appendName(ObjectRef{Kind: "automation", ParentID: role.ID, ID: automation.ID}, "autopilot", automationTitle(role.DisplayName, automation.Name)); err != nil {
				return nil, err
			}
		}
	}
	return names, nil
}

func lockAndValidateAdoptionTargets(
	ctx context.Context,
	q *db.Queries,
	workspaceID pgtype.UUID,
	snapshot Snapshot,
	plan Plan,
	adoptions map[string]AdoptionActionDecision,
) error {
	if len(adoptions) == 0 {
		return nil
	}
	_, refs, err := collectAdoptionTargetRequests(snapshot, plan)
	if err != nil {
		return err
	}
	requests := make([]adoptionTargetRequest, 0, len(adoptions))
	for key, decision := range adoptions {
		request, ok := refs[key]
		if !ok || request.TargetKind != decision.TargetKind {
			return fmt.Errorf("%w: adoption decision has no matching create target", ErrApplyConflict)
		}
		request.TargetID = decision.TargetID
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].TargetKind+"\x00"+requests[i].Name+"\x00"+requests[i].TargetID < requests[j].TargetKind+"\x00"+requests[j].Name+"\x00"+requests[j].TargetID
	})
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
	if len(rows) != len(requests) {
		return fmt.Errorf("%w: an approved adoption target disappeared, was renamed or archived", ErrApplyConflict)
	}
	byID := make(map[string]db.ListRoleSourceAdoptionTargetsForUpdateRow, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row.TargetID)
		if _, duplicate := byID[id]; duplicate {
			return fmt.Errorf("%w: adoption query returned a duplicate target", ErrApplyConflict)
		}
		if row.ManagedBySourceID.Valid {
			return fmt.Errorf("%w: adoption target is already managed by a source", ErrApplyConflict)
		}
		if !row.AdoptionEligible.Valid || !row.AdoptionEligible.Bool {
			return fmt.Errorf("%w: adoption target is no longer eligible", ErrApplyConflict)
		}
		byID[id] = row
	}
	for _, decision := range adoptions {
		row, ok := byID[decision.TargetID]
		if !ok || row.TargetKind != decision.TargetKind {
			return fmt.Errorf("%w: adoption target identity changed", ErrApplyConflict)
		}
		commitment, err := adoptionVersionCommitment(row.UpdatedAt)
		if err != nil || commitment != decision.VersionCommitment {
			return fmt.Errorf("%w: adoption target changed after plan creation", ErrApplyConflict)
		}
	}
	return nil
}

func applyAdoptionMappings(
	mappings map[string]db.RoleSourceObjectMapping,
	adoptions map[string]AdoptionActionDecision,
	sourceID, workspaceID pgtype.UUID,
) error {
	for key, decision := range adoptions {
		if _, exists := mappings[key]; exists {
			return fmt.Errorf("%w: adoption source object already has a mapping", ErrApplyConflict)
		}
		targetID, err := util.ParseUUID(decision.TargetID)
		if err != nil {
			return fmt.Errorf("%w: invalid adoption target", ErrApplyConflict)
		}
		mappings[key] = db.RoleSourceObjectMapping{
			SourceID: sourceID, WorkspaceID: workspaceID,
			SourceKind: decision.Ref.Kind, SourceParentID: decision.Ref.ParentID, SourceObjectID: decision.Ref.ID,
			TargetKind: decision.TargetKind, TargetID: targetID, OwnershipMask: []byte(`[]`),
		}
	}
	return nil
}

func (s *materializationState) materialize(ctx context.Context) error {
	if err := s.materializeCapabilities(ctx); err != nil {
		return err
	}
	if err := s.materializeRoles(ctx); err != nil {
		return err
	}
	for _, role := range s.snapshot.Manifest.Roles {
		if err := s.materializeRoleSecrets(ctx, role); err != nil {
			return err
		}
	}
	if err := s.materializeSkills(ctx); err != nil {
		return err
	}
	if err := s.flushAgentSkillBindings(ctx); err != nil {
		return err
	}
	for _, role := range s.snapshot.Manifest.Roles {
		for _, binding := range role.CapabilityBindings {
			if err := s.materializeCapabilityBinding(ctx, role, binding); err != nil {
				return err
			}
		}
	}
	if err := s.materializeAutomations(ctx); err != nil {
		return err
	}
	if err := s.materializeArchives(ctx); err != nil {
		return err
	}
	if err := s.flushSkillFiles(ctx); err != nil {
		return err
	}
	return s.flushMappings(ctx)
}

func (s *materializationState) materializeRoleSecrets(ctx context.Context, role Role) error {
	roleMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
	if !ok || roleMapping.ArchivedAt.Valid {
		return fmt.Errorf("%w: role mapping is missing for secure materialization", ErrApplyConflict)
	}
	if len(role.Environment) == 0 && len(role.MCP) == 0 {
		return nil
	}
	agent, err := s.q.GetAgentForUpdate(ctx, roleMapping.TargetID)
	if err != nil {
		return err
	}
	if agent.WorkspaceID != s.workspaceID || agent.Kind != "user" || agent.ArchivedAt.Valid {
		return fmt.Errorf("%w: secure target agent is missing or cross-tenant", ErrApplyConflict)
	}
	environment := map[string]string{}
	if len(agent.CustomEnv) > 0 && string(agent.CustomEnv) != "null" {
		if err := json.Unmarshal(agent.CustomEnv, &environment); err != nil {
			return fmt.Errorf("%w: target custom environment is malformed", ErrApplyConflict)
		}
	}
	mcpRoot, mcpServers, err := decodeTargetMCP(agent.McpConfig)
	if err != nil {
		return err
	}
	payload := s.secretPayloads[role.ID]
	needsPayload := roleNeedsSecretTransfer(role)
	if needsPayload {
		if _, ok := s.secretPayloads[role.ID]; !ok {
			return fmt.Errorf("%w: role secret payload is missing", ErrInvalidApplyRequest)
		}
	}

	for _, declaration := range role.Environment {
		ref := ObjectRef{Kind: "environment", ParentID: role.ID, ID: declaration.Name}
		action, ok := s.actions[objectKey(ref)]
		if !ok || action.Operation == PlanArchiveCandidate || action.Operation == PlanBlocked {
			return fmt.Errorf("%w: current environment declaration has no applicable plan action", ErrApplyConflict)
		}
		mapping, mapped := s.mappings[objectKey(ref)]
		owned := mapped && !mapping.ArchivedAt.Valid && mapping.TargetKind == "agent" && mapping.TargetID == agent.ID
		if mapped && !mapping.ArchivedAt.Valid && !owned {
			return fmt.Errorf("%w: environment mapping targets another object", ErrApplyConflict)
		}
		if declaration.Configured {
			value, exists := payload.Environment[declaration.Name]
			if !exists {
				return fmt.Errorf("%w: configured environment value is absent", ErrInvalidSecretEnvelope)
			}
			if _, collision := environment[declaration.Name]; collision && !owned {
				return fmt.Errorf("%w: environment key %s is already user-owned", ErrApplyConflict, declaration.Name)
			}
			environment[declaration.Name] = value
		} else if owned {
			delete(environment, declaration.Name)
		}
	}
	for _, declaration := range role.MCP {
		ref := ObjectRef{Kind: "mcp", ParentID: role.ID, ID: declaration.ID}
		action, ok := s.actions[objectKey(ref)]
		if !ok || action.Operation == PlanArchiveCandidate || action.Operation == PlanBlocked {
			return fmt.Errorf("%w: current MCP declaration has no applicable plan action", ErrApplyConflict)
		}
		mapping, mapped := s.mappings[objectKey(ref)]
		owned := mapped && !mapping.ArchivedAt.Valid && mapping.TargetKind == "agent" && mapping.TargetID == agent.ID
		if mapped && !mapping.ArchivedAt.Valid && !owned {
			return fmt.Errorf("%w: MCP mapping targets another object", ErrApplyConflict)
		}
		definition, exists := payload.MCPServers[declaration.ID]
		if !exists {
			return fmt.Errorf("%w: MCP definition is absent", ErrInvalidSecretEnvelope)
		}
		if _, collision := mcpServers[declaration.ID]; collision && !owned {
			return fmt.Errorf("%w: MCP server %s is already user-owned", ErrApplyConflict, declaration.ID)
		}
		mcpServers[declaration.ID] = append(json.RawMessage(nil), definition...)
	}
	environmentBody, err := json.Marshal(environment)
	if err != nil {
		return err
	}
	mcpBody := append([]byte(nil), agent.McpConfig...)
	if len(role.MCP) > 0 {
		mcpBody, err = encodeTargetMCP(mcpRoot, mcpServers)
		if err != nil {
			return err
		}
	}
	if _, err := s.q.UpdateRoleSourceAgentSecrets(ctx, db.UpdateRoleSourceAgentSecretsParams{
		CustomEnv: environmentBody, McpConfig: mcpBody, ID: agent.ID, WorkspaceID: s.workspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApplyConflict
		}
		return err
	}
	for _, declaration := range role.Environment {
		ref := ObjectRef{Kind: "environment", ParentID: role.ID, ID: declaration.Name}
		action := s.actions[objectKey(ref)]
		mask := []string{}
		if declaration.Configured {
			mask = []string{"custom_env:" + declaration.Name}
		}
		if err := s.upsertMapping(ctx, ref, "agent", agent.ID, action.AfterDigest, mask, pgtype.Timestamptz{}); err != nil {
			return err
		}
		s.incrementAppliedAction(action.Operation)
	}
	for _, declaration := range role.MCP {
		ref := ObjectRef{Kind: "mcp", ParentID: role.ID, ID: declaration.ID}
		action := s.actions[objectKey(ref)]
		if err := s.upsertMapping(ctx, ref, "agent", agent.ID, action.AfterDigest, []string{"mcp_config:mcpServers:" + declaration.ID}, pgtype.Timestamptz{}); err != nil {
			return err
		}
		s.incrementAppliedAction(action.Operation)
	}
	return nil
}

func decodeTargetMCP(body []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	root := map[string]json.RawMessage{}
	if len(body) > 0 && string(body) != "null" {
		if err := json.Unmarshal(body, &root); err != nil {
			return nil, nil, fmt.Errorf("%w: target MCP configuration is malformed", ErrApplyConflict)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, fmt.Errorf("%w: target MCP server map is malformed", ErrApplyConflict)
		}
	}
	return root, servers, nil
}

func encodeTargetMCP(root map[string]json.RawMessage, servers map[string]json.RawMessage) ([]byte, error) {
	serverBody, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	root["mcpServers"] = serverBody
	return json.Marshal(root)
}

func (s *materializationState) incrementAppliedAction(operation PlanOperation) {
	switch operation {
	case PlanCreate:
		s.receipt.Counts.Created++
	case PlanUpdate:
		s.receipt.Counts.Updated++
	case PlanUnchanged:
		s.receipt.Counts.Unchanged++
	}
}

func (s *materializationState) materializeCapabilities(ctx context.Context) error {
	versions, counts, err := collectCapabilityVersions(s.snapshot, s.actions, s.decisions)
	if err != nil {
		return err
	}
	s.receipt.Counts.Created += counts.Created
	s.receipt.Counts.Updated += counts.Updated
	s.receipt.Counts.Unchanged += counts.Unchanged
	s.receipt.Counts.Retained += counts.Retained
	batches, err := capabilityVersionBatches(versions)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.EnsureRoleSourceCapabilityVersions(ctx, db.EnsureRoleSourceCapabilityVersionsParams{
			Versions: body, WorkspaceID: s.workspaceID, SourceID: s.source.ID,
			SnapshotDigest: s.snapshot.SnapshotDigest,
		})
		if err != nil {
			return err
		}
		if len(rows) != len(batch) {
			return fmt.Errorf("%w: persisted %d of %d capability versions in batch", ErrApplyConflict, len(rows), len(batch))
		}
	}
	return nil
}

func collectCapabilityVersions(snapshot Snapshot, actions map[string]PlanAction, decisions map[string]ArchiveDecision) ([]pendingRoleSourceCapabilityVersion, ApplyCounts, error) {
	versions := make([]pendingRoleSourceCapabilityVersion, 0, len(snapshot.Manifest.Capabilities))
	var counts ApplyCounts
	for _, capability := range snapshot.Manifest.Capabilities {
		ref := ObjectRef{Kind: "capability", ID: capability.ID}
		action := actions[objectKey(ref)]
		if action.Operation == PlanUnchanged {
			counts.Unchanged++
			continue
		}
		if action.Operation == PlanArchiveCandidate {
			if decisions[objectKey(ref)] == ArchiveDecisionRetain {
				counts.Retained++
			}
			continue
		}
		definition, err := json.Marshal(capability)
		if err != nil {
			return nil, ApplyCounts{}, err
		}
		versions = append(versions, pendingRoleSourceCapabilityVersion{
			CapabilityID: capability.ID, Version: capability.Version,
			ObjectDigest: action.AfterDigest, Definition: definition,
		})
		if action.Operation == PlanCreate {
			counts.Created++
		} else {
			counts.Updated++
		}
	}
	return versions, counts, nil
}

func capabilityVersionBatches(versions []pendingRoleSourceCapabilityVersion) ([][]pendingRoleSourceCapabilityVersion, error) {
	if len(versions) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceCapabilityVersion, 0, (len(versions)+capabilityVersionBatchSize-1)/capabilityVersionBatchSize)
	start, encodedBytes := 0, 2 // JSON array brackets.
	for index, version := range versions {
		body, err := json.Marshal(version)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > capabilityVersionBatchBytes {
			return nil, fmt.Errorf("%w: capability version %q exceeds batch byte limit", ErrMaterializationBlocked, version.CapabilityID)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= capabilityVersionBatchSize || encodedBytes+separator+len(body) > capabilityVersionBatchBytes) {
			batches = append(batches, versions[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, versions[start:])
	return batches, nil
}

func (s *materializationState) materializeRoles(ctx context.Context) error {
	pending := make([]pendingRoleSourceAgent, 0, len(s.snapshot.Manifest.Roles))
	for _, role := range s.snapshot.Manifest.Roles {
		ref := ObjectRef{Kind: "role", ID: role.ID}
		action := s.actions[objectKey(ref)]
		if action.Operation == PlanUnchanged {
			s.receipt.Counts.Unchanged++
			if err := s.advanceExistingMappingSnapshot(ctx, ref); err != nil {
				return err
			}
			continue
		}
		if action.Operation == PlanArchiveCandidate {
			continue
		}
		mapping, mapped := s.mappings[objectKey(ref)]
		if mapped && mapping.ArchivedAt.Valid {
			return fmt.Errorf("%w: archived role mapping cannot be reused", ErrApplyConflict)
		}
		targetID := mapping.TargetID
		operation := "update"
		if !mapped {
			var err error
			targetID, err = newPGUUID()
			if err != nil {
				return err
			}
			operation = "create"
		}
		pending = append(pending, pendingRoleSourceAgent{
			Ref: ref, ID: util.UUIDToString(targetID), Operation: operation,
			Name: role.DisplayName, Description: roleDescription(role), RuntimeMode: s.runtimeMode,
			RuntimeID: util.UUIDToString(s.source.RuntimeID), OwnerID: util.UUIDToString(s.actorID),
			Instructions: s.artifacts[role.Instructions.Digest].body, ObjectDigest: action.AfterDigest,
		})
	}
	batches, err := materializedAgentBatches(pending)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.MaterializeRoleSourceAgents(ctx, db.MaterializeRoleSourceAgentsParams{
			Agents: body, WorkspaceID: s.workspaceID,
		})
		if err != nil {
			return err
		}
		_, err = exactMaterializedAgentIDs(batch, rows)
		if err != nil {
			return err
		}
		for _, item := range batch {
			targetID, err := util.ParseUUID(item.ID)
			if err != nil {
				return err
			}
			if err := s.upsertMapping(ctx, item.Ref, "agent", targetID, item.ObjectDigest, []string{"name", "description", "runtime_binding", "instructions"}, pgtype.Timestamptz{}); err != nil {
				return err
			}
			if item.Operation == "create" {
				s.receipt.Counts.Created++
			} else if _, adopted := s.adoptions[objectKey(item.Ref)]; adopted {
				s.receipt.Counts.Adopted++
			} else {
				s.receipt.Counts.Updated++
			}
		}
	}
	return nil
}

func exactMaterializedAgentIDs(requested []pendingRoleSourceAgent, rows []pgtype.UUID) (map[string]bool, error) {
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row)
		if returned[id] {
			return nil, fmt.Errorf("%w: role target batch returned duplicate %s", ErrApplyConflict, id)
		}
		returned[id] = true
	}
	if len(returned) != len(requested) {
		return nil, fmt.Errorf("%w: persisted %d of %d role targets", ErrApplyConflict, len(returned), len(requested))
	}
	for _, item := range requested {
		if !returned[item.ID] {
			return nil, fmt.Errorf("%w: role target %s was not persisted", ErrApplyConflict, item.ID)
		}
	}
	return returned, nil
}

func materializedAgentBatches(agents []pendingRoleSourceAgent) ([][]pendingRoleSourceAgent, error) {
	if len(agents) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceAgent, 0, (len(agents)+materializedAgentBatchSize-1)/materializedAgentBatchSize)
	start, encodedBytes := 0, 2
	for index, agent := range agents {
		body, err := json.Marshal(agent)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedAgentBatchBytes {
			return nil, fmt.Errorf("%w: role target %q exceeds batch byte limit", ErrMaterializationBlocked, agent.Ref.ID)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedAgentBatchSize || encodedBytes+separator+len(body) > materializedAgentBatchBytes) {
			batches = append(batches, agents[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, agents[start:])
	return batches, nil
}

func (s *materializationState) materializeSkills(ctx context.Context) error {
	pending := make([]pendingRoleSourceSkill, 0)
	for _, role := range s.snapshot.Manifest.Roles {
		for _, skill := range role.Skills {
			ref := ObjectRef{Kind: "skill", ParentID: role.ID, ID: skill.ID}
			action := s.actions[objectKey(ref)]
			if action.Operation == PlanUnchanged {
				s.receipt.Counts.Unchanged++
				if err := s.advanceExistingMappingSnapshot(ctx, ref); err != nil {
					return err
				}
				continue
			}
			if action.Operation == PlanArchiveCandidate {
				continue
			}
			roleMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
			if !ok || roleMapping.ArchivedAt.Valid {
				return fmt.Errorf("%w: role mapping is missing for skill %s", ErrApplyConflict, skill.ID)
			}
			mapping, mapped := s.mappings[objectKey(ref)]
			if mapped && mapping.ArchivedAt.Valid {
				return fmt.Errorf("%w: archived skill mapping cannot be reused", ErrApplyConflict)
			}
			targetID := mapping.TargetID
			operation := "update"
			if !mapped {
				var err error
				targetID, err = newPGUUID()
				if err != nil {
					return err
				}
				operation = "create"
			}
			config, err := json.Marshal(map[string]any{"managed_by": "role_source", "source_id": util.UUIDToString(s.source.ID), "source_role_id": role.ID, "source_skill_id": skill.ID, "version": skill.Version})
			if err != nil {
				return err
			}
			desiredFiles := make(map[string]string, len(skill.Artifacts))
			for _, artifact := range skill.Artifacts {
				verified, ok := s.artifacts[artifact.Digest]
				if !ok {
					return fmt.Errorf("%w: supporting skill artifact %s is unavailable", ErrArtifactMissing, artifact.Digest)
				}
				desiredFiles[artifact.Path] = verified.body
			}
			pending = append(pending, pendingRoleSourceSkill{
				Ref: ref, AgentID: roleMapping.TargetID, ID: util.UUIDToString(targetID), Operation: operation,
				Name: skill.Name, Description: "Managed by role source", Content: s.artifacts[skill.Entrypoint.Digest].body,
				Config: config, CreatedBy: util.UUIDToString(s.actorID), ObjectDigest: action.AfterDigest, DesiredFiles: desiredFiles,
			})
		}
	}
	batches, err := materializedSkillBatches(pending)
	if err != nil {
		return err
	}
	if err := s.prepareSkillFileState(ctx, pending); err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.MaterializeRoleSourceSkills(ctx, db.MaterializeRoleSourceSkillsParams{
			Skills: body, WorkspaceID: s.workspaceID,
		})
		if err != nil {
			return err
		}
		if _, err := exactMaterializedSkillIDs(batch, rows); err != nil {
			return err
		}
	}
	for _, batch := range batches {
		for _, item := range batch {
			targetID, err := util.ParseUUID(item.ID)
			if err != nil {
				return err
			}
			if err := s.stageAgentSkillBinding(item.AgentID, targetID); err != nil {
				return err
			}
			fileMask, err := s.syncOwnedSkillFiles(ctx, item.Ref, targetID, item.DesiredFiles, item.Operation == "create")
			if err != nil {
				return err
			}
			mask := append([]string{"name", "description", "content", "agent_binding"}, fileMask...)
			if err := s.upsertMapping(ctx, item.Ref, "skill", targetID, item.ObjectDigest, mask, pgtype.Timestamptz{}); err != nil {
				return err
			}
			if item.Operation == "create" {
				s.receipt.Counts.Created++
			} else if _, adopted := s.adoptions[objectKey(item.Ref)]; adopted {
				s.receipt.Counts.Adopted++
			} else {
				s.receipt.Counts.Updated++
			}
		}
	}
	return nil
}

func (s *materializationState) prepareSkillFileState(ctx context.Context, pending []pendingRoleSourceSkill) error {
	existingTargets := make(map[string]pgtype.UUID)
	pendingTargets := make(map[string]pendingRoleSourceSkill, len(pending))
	for _, item := range pending {
		pendingTargets[objectKey(item.Ref)] = item
		if item.Operation != "update" {
			continue
		}
		targetID, err := util.ParseUUID(item.ID)
		if err != nil {
			return err
		}
		existingTargets[item.ID] = targetID
	}
	for _, role := range s.snapshot.Manifest.Roles {
		for _, binding := range role.CapabilityBindings {
			ref := ObjectRef{Kind: "capability_binding", ParentID: role.ID, ID: capabilityBindingObjectID(binding)}
			action := s.actions[objectKey(ref)]
			if action.Operation == PlanUnchanged || action.Operation == PlanArchiveCandidate || action.Operation == PlanBlocked {
				continue
			}
			mapping, ok := s.mappings[objectKey(ObjectRef{Kind: "skill", ParentID: role.ID, ID: binding.SkillID})]
			if !ok {
				pendingTarget, planned := pendingTargets[objectKey(ObjectRef{Kind: "skill", ParentID: role.ID, ID: binding.SkillID})]
				if planned && pendingTarget.Operation == "create" {
					continue
				}
				return fmt.Errorf("%w: capability binding target skill is missing", ErrApplyConflict)
			}
			if mapping.ArchivedAt.Valid || mapping.TargetKind != "skill" {
				return fmt.Errorf("%w: capability binding target skill is missing", ErrApplyConflict)
			}
			existingTargets[util.UUIDToString(mapping.TargetID)] = mapping.TargetID
		}
	}
	for _, action := range s.plan.Actions {
		if action.Ref.Kind != "capability_binding" || action.Operation != PlanArchiveCandidate || s.decisions[objectKey(action.Ref)] != ArchiveDecisionArchive {
			continue
		}
		mapping, ok := s.mappings[objectKey(action.Ref)]
		if !ok || mapping.ArchivedAt.Valid || mapping.TargetKind != "skill" {
			return fmt.Errorf("%w: capability archive target skill is missing", ErrApplyConflict)
		}
		existingTargets[util.UUIDToString(mapping.TargetID)] = mapping.TargetID
	}
	allTargets := make(map[string]pgtype.UUID, len(existingTargets)+len(pending))
	for id, targetID := range existingTargets {
		allTargets[id] = targetID
	}
	for _, item := range pending {
		targetID, err := util.ParseUUID(item.ID)
		if err != nil {
			return err
		}
		allTargets[item.ID] = targetID
	}
	ids := make([]string, 0, len(allTargets))
	for id := range allTargets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	s.skillFiles = make(map[string]map[string]bool, len(ids))
	s.skillFilesInitial = make(map[string]map[string]bool, len(ids))
	s.pendingSkillFiles = make(map[string]pendingRoleSourceSkillFileMutation)
	for _, id := range ids {
		s.skillFiles[id] = map[string]bool{}
		s.skillFilesInitial[id] = map[string]bool{}
	}
	lockIDs := make([]string, 0, len(existingTargets))
	for id := range existingTargets {
		lockIDs = append(lockIDs, id)
	}
	sort.Strings(lockIDs)
	if len(lockIDs) == 0 {
		s.skillFilesReady = true
		return nil
	}
	pgIDs := make([]pgtype.UUID, 0, len(lockIDs))
	for _, id := range lockIDs {
		pgIDs = append(pgIDs, existingTargets[id])
	}
	locked, err := s.q.LockRoleSourceSkillsForFileSync(ctx, db.LockRoleSourceSkillsForFileSyncParams{
		SkillIds: pgIDs, WorkspaceID: s.workspaceID,
	})
	if err != nil {
		return err
	}
	if _, err := exactPGUUIDs("skill-file targets", lockIDs, locked); err != nil {
		return err
	}
	fileTargets, err := s.collectSkillFileTargets(pendingTargets)
	if err != nil {
		return err
	}
	if len(fileTargets) == 0 {
		s.skillFilesReady = true
		return nil
	}
	targetBody, err := json.Marshal(fileTargets)
	if err != nil {
		return err
	}
	if len(fileTargets) > materializedSkillFileTargetLimit || len(targetBody) > materializedSkillFileTargetBytes {
		return fmt.Errorf("%w: skill-file lock target set exceeds bounded request limits", ErrMaterializationBlocked)
	}
	rows, err := s.q.ListRoleSourceSkillFilesForUpdateByTargets(ctx, db.ListRoleSourceSkillFilesForUpdateByTargetsParams{
		Targets: targetBody, WorkspaceID: s.workspaceID,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		targetID := util.UUIDToString(row.SkillID)
		files, ok := s.skillFiles[targetID]
		if !ok {
			return fmt.Errorf("%w: unexpected skill-file target %s", ErrApplyConflict, targetID)
		}
		if files[row.Path] {
			return fmt.Errorf("%w: duplicate skill-file path %s for target %s", ErrApplyConflict, row.Path, targetID)
		}
		files[row.Path] = true
		s.skillFilesInitial[targetID][row.Path] = true
	}
	s.skillFilesReady = true
	return nil
}

func (s *materializationState) collectSkillFileTargets(pendingTargets map[string]pendingRoleSourceSkill) ([]pendingRoleSourceSkillFileTarget, error) {
	targets := make(map[string]pendingRoleSourceSkillFileTarget)
	add := func(targetID pgtype.UUID, paths map[string]bool) {
		id := util.UUIDToString(targetID)
		for filePath := range paths {
			targets[id+"\x00"+filePath] = pendingRoleSourceSkillFileTarget{SkillID: id, Path: filePath}
		}
	}
	ownedPaths := func(mapping db.RoleSourceObjectMapping) map[string]bool {
		paths := make(map[string]bool)
		for _, item := range ownershipMask(mapping) {
			if strings.HasPrefix(item, "skill_file:") {
				paths[strings.TrimPrefix(item, "skill_file:")] = true
			}
		}
		return paths
	}
	for _, item := range pendingTargets {
		targetID, err := util.ParseUUID(item.ID)
		if err != nil {
			return nil, err
		}
		paths := make(map[string]bool, len(item.DesiredFiles))
		for filePath := range item.DesiredFiles {
			paths[filePath] = true
		}
		if mapping, ok := s.mappings[objectKey(item.Ref)]; ok {
			for filePath := range ownedPaths(mapping) {
				paths[filePath] = true
			}
		}
		add(targetID, paths)
	}
	for _, role := range s.snapshot.Manifest.Roles {
		for _, binding := range role.CapabilityBindings {
			ref := ObjectRef{Kind: "capability_binding", ParentID: role.ID, ID: capabilityBindingObjectID(binding)}
			action := s.actions[objectKey(ref)]
			if action.Operation == PlanUnchanged || action.Operation == PlanArchiveCandidate || action.Operation == PlanBlocked {
				continue
			}
			targetID, err := s.skillTargetID(role.ID, binding.SkillID, pendingTargets)
			if err != nil {
				return nil, err
			}
			capability, ok := s.capability(binding.CapabilityID)
			if !ok {
				return nil, fmt.Errorf("%w: capability binding definition is missing", ErrApplyConflict)
			}
			capabilityAction := s.actions[objectKey(ObjectRef{Kind: "capability", ID: capability.ID})]
			desired, err := s.capabilityBundleFiles(capability, binding, capabilityAction.AfterDigest)
			if err != nil {
				return nil, err
			}
			paths := make(map[string]bool, len(desired))
			for filePath := range desired {
				paths[filePath] = true
			}
			if mapping, ok := s.mappings[objectKey(ref)]; ok {
				for filePath := range ownedPaths(mapping) {
					paths[filePath] = true
				}
			}
			add(targetID, paths)
		}
	}
	for _, action := range s.plan.Actions {
		if action.Ref.Kind != "capability_binding" || action.Operation != PlanArchiveCandidate || s.decisions[objectKey(action.Ref)] != ArchiveDecisionArchive {
			continue
		}
		mapping := s.mappings[objectKey(action.Ref)]
		add(mapping.TargetID, ownedPaths(mapping))
	}
	ordered := make([]pendingRoleSourceSkillFileTarget, 0, len(targets))
	for _, target := range targets {
		ordered = append(ordered, target)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SkillID != ordered[j].SkillID {
			return ordered[i].SkillID < ordered[j].SkillID
		}
		return ordered[i].Path < ordered[j].Path
	})
	return ordered, nil
}

func (s *materializationState) skillTargetID(roleID, skillID string, pendingTargets map[string]pendingRoleSourceSkill) (pgtype.UUID, error) {
	ref := ObjectRef{Kind: "skill", ParentID: roleID, ID: skillID}
	if mapping, ok := s.mappings[objectKey(ref)]; ok && !mapping.ArchivedAt.Valid && mapping.TargetKind == "skill" {
		return mapping.TargetID, nil
	}
	if pending, ok := pendingTargets[objectKey(ref)]; ok && pending.Operation == "create" {
		return util.ParseUUID(pending.ID)
	}
	return pgtype.UUID{}, fmt.Errorf("%w: capability binding target skill is missing", ErrApplyConflict)
}

func exactPGUUIDs(label string, requested []string, rows []pgtype.UUID) (map[string]bool, error) {
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row)
		if returned[id] {
			return nil, fmt.Errorf("%w: %s returned duplicate %s", ErrApplyConflict, label, id)
		}
		returned[id] = true
	}
	if len(returned) != len(requested) {
		return nil, fmt.Errorf("%w: locked %d of %d %s", ErrApplyConflict, len(returned), len(requested), label)
	}
	for _, id := range requested {
		if !returned[id] {
			return nil, fmt.Errorf("%w: %s %s was not locked", ErrApplyConflict, label, id)
		}
	}
	return returned, nil
}

func exactMaterializedSkillIDs(requested []pendingRoleSourceSkill, rows []pgtype.UUID) (map[string]bool, error) {
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row)
		if returned[id] {
			return nil, fmt.Errorf("%w: skill target batch returned duplicate %s", ErrApplyConflict, id)
		}
		returned[id] = true
	}
	if len(returned) != len(requested) {
		return nil, fmt.Errorf("%w: persisted %d of %d skill targets", ErrApplyConflict, len(returned), len(requested))
	}
	for _, item := range requested {
		if !returned[item.ID] {
			return nil, fmt.Errorf("%w: skill target %s was not persisted", ErrApplyConflict, item.ID)
		}
	}
	return returned, nil
}

func materializedSkillBatches(skills []pendingRoleSourceSkill) ([][]pendingRoleSourceSkill, error) {
	if len(skills) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceSkill, 0, (len(skills)+materializedSkillBatchSize-1)/materializedSkillBatchSize)
	start, encodedBytes := 0, 2
	for index, skill := range skills {
		body, err := json.Marshal(skill)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedSkillBatchBytes {
			return nil, fmt.Errorf("%w: skill target %q exceeds batch byte limit", ErrMaterializationBlocked, skill.Ref.ID)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedSkillBatchSize || encodedBytes+separator+len(body) > materializedSkillBatchBytes) {
			batches = append(batches, skills[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, skills[start:])
	return batches, nil
}

func (s *materializationState) materializeCapabilityBinding(ctx context.Context, role Role, binding CapabilityBinding) error {
	ref := ObjectRef{Kind: "capability_binding", ParentID: role.ID, ID: capabilityBindingObjectID(binding)}
	action, ok := s.actions[objectKey(ref)]
	if !ok || action.Operation == PlanBlocked {
		return fmt.Errorf("%w: capability binding has no applicable plan action", ErrApplyConflict)
	}
	if action.Operation == PlanArchiveCandidate {
		return nil
	}
	if action.Operation == PlanUnchanged {
		s.receipt.Counts.Unchanged++
		return s.advanceExistingMappingSnapshot(ctx, ref)
	}
	skillMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "skill", ParentID: role.ID, ID: binding.SkillID})]
	if !ok || skillMapping.ArchivedAt.Valid || skillMapping.TargetKind != "skill" {
		return fmt.Errorf("%w: capability binding target skill is missing", ErrApplyConflict)
	}
	capability, ok := s.capability(binding.CapabilityID)
	if !ok {
		return fmt.Errorf("%w: capability binding definition is missing", ErrApplyConflict)
	}
	capabilityAction, ok := s.actions[objectKey(ObjectRef{Kind: "capability", ID: capability.ID})]
	if !ok || capabilityAction.AfterDigest == "" {
		return fmt.Errorf("%w: capability version digest is missing", ErrApplyConflict)
	}
	desiredFiles, err := s.capabilityBundleFiles(capability, binding, capabilityAction.AfterDigest)
	if err != nil {
		return err
	}
	fileMask, err := s.syncOwnedSkillFiles(ctx, ref, skillMapping.TargetID, desiredFiles, false)
	if err != nil {
		return err
	}
	if err := s.upsertMapping(ctx, ref, "skill", skillMapping.TargetID, action.AfterDigest, fileMask, pgtype.Timestamptz{}); err != nil {
		return err
	}
	s.incrementAppliedAction(action.Operation)
	return nil
}

func capabilityIndex(capabilities []Capability) map[string]Capability {
	indexed := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		indexed[capability.ID] = capability
	}
	return indexed
}

func (s *materializationState) capability(id string) (Capability, bool) {
	if s.capabilities == nil {
		s.capabilities = capabilityIndex(s.snapshot.Manifest.Capabilities)
	}
	capability, ok := s.capabilities[id]
	return capability, ok
}

func (s *materializationState) capabilityBundleFiles(capability Capability, binding CapabilityBinding, objectDigest string) (map[string]string, error) {
	markerPath := protocol.RoleSourceCapabilityMarkerPath(capability.ID, binding.SkillID, binding.Profile)
	prefix := strings.TrimSuffix(markerPath, "manifest.json") + "files/"
	refs := append([]ArtifactRef{capability.Entrypoint}, capability.Artifacts...)
	bundle := protocol.RoleSourceCapabilityBundle{
		ContractVersion: protocol.RoleSourceCapabilityBundleContractV1,
		CapabilityID:    capability.ID, SourceSkillID: binding.SkillID, Profile: binding.Profile,
		ResolvedVersion: capability.Version, ObjectDigest: objectDigest,
		PermissionMode: binding.PermissionMode, Required: binding.Required,
		Fallback: binding.Fallback,
		Files:    make([]protocol.RoleSourceCapabilityBundleFile, 0, len(refs)),
	}
	desired := make(map[string]string, len(refs)+1)
	for index, ref := range refs {
		verified, ok := s.artifacts[ref.Digest]
		if !ok {
			return nil, fmt.Errorf("%w: capability artifact %s is unavailable", ErrArtifactMissing, ref.Digest)
		}
		pathDigest := sha256.Sum256([]byte(ref.Path))
		targetPath := prefix + hex.EncodeToString(pathDigest[:]) + ".artifact"
		if index == 0 {
			bundle.EntrypointPath = targetPath
		}
		bundle.Files = append(bundle.Files, protocol.RoleSourceCapabilityBundleFile{
			SourcePath: ref.Path, TargetPath: targetPath, Digest: ref.Digest,
			SizeBytes: ref.SizeBytes, MediaType: ref.MediaType,
		})
		desired[targetPath] = verified.body
	}
	marker, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	desired[markerPath] = string(marker)
	return desired, nil
}

func (s *materializationState) syncOwnedSkillFiles(ctx context.Context, ref ObjectRef, targetID pgtype.UUID, desired map[string]string, newlyCreated bool) ([]string, error) {
	if newlyCreated {
		if _, exists := s.mappings[objectKey(ref)]; exists {
			return nil, fmt.Errorf("%w: new skill-file target already has a mapping", ErrApplyConflict)
		}
		if len(desired) == 0 {
			return []string{}, nil
		}
	}
	if !s.skillFilesReady {
		return nil, fmt.Errorf("%w: skill-file state was not prepared", ErrApplyConflict)
	}
	targetKey := util.UUIDToString(targetID)
	existing, ok := s.skillFiles[targetKey]
	if !ok {
		return nil, fmt.Errorf("%w: skill-file target %s was not locked", ErrApplyConflict, targetKey)
	}
	owned := map[string]bool{}
	if mapping, ok := s.mappings[objectKey(ref)]; ok {
		if mapping.ArchivedAt.Valid || mapping.TargetKind != "skill" || mapping.TargetID != targetID {
			return nil, fmt.Errorf("%w: skill-file ownership mapping is archived or retargeted", ErrApplyConflict)
		}
		for _, item := range ownershipMask(mapping) {
			if strings.HasPrefix(item, "skill_file:") {
				owned[strings.TrimPrefix(item, "skill_file:")] = true
			}
		}
	}
	paths := make([]string, 0, len(desired))
	for filePath := range desired {
		if existing[filePath] && !owned[filePath] {
			return nil, fmt.Errorf("%w: skill file %s is already owned outside this source object", ErrApplyConflict, filePath)
		}
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	remove := make([]string, 0)
	for filePath := range owned {
		if _, keep := desired[filePath]; !keep {
			remove = append(remove, filePath)
		}
	}
	sort.Strings(remove)
	if len(remove) > 0 {
		for _, filePath := range remove {
			delete(existing, filePath)
			s.stageSkillFileMutation(targetKey, filePath, "")
		}
	}
	if len(paths) > 0 {
		for _, filePath := range paths {
			existing[filePath] = true
			s.stageSkillFileMutation(targetKey, filePath, desired[filePath])
		}
	}
	mask := make([]string, 0, len(paths))
	for _, filePath := range paths {
		mask = append(mask, "skill_file:"+filePath)
	}
	return mask, nil
}

func (s *materializationState) stageSkillFileMutation(skillID, filePath, content string) {
	initial := s.skillFilesInitial[skillID][filePath]
	current := s.skillFiles[skillID][filePath]
	key := skillID + "\x00" + filePath
	if initial == current {
		if !current {
			delete(s.pendingSkillFiles, key)
			return
		}
		s.pendingSkillFiles[key] = pendingRoleSourceSkillFileMutation{SkillID: skillID, Path: filePath, Operation: "update", Content: content}
		return
	}
	operation := "delete"
	if current {
		operation = "insert"
	}
	s.pendingSkillFiles[key] = pendingRoleSourceSkillFileMutation{SkillID: skillID, Path: filePath, Operation: operation, Content: content}
}

func (s *materializationState) flushSkillFiles(ctx context.Context) error {
	mutations := make([]pendingRoleSourceSkillFileMutation, 0, len(s.pendingSkillFiles))
	for _, mutation := range s.pendingSkillFiles {
		mutations = append(mutations, mutation)
	}
	sort.Slice(mutations, func(i, j int) bool {
		if mutations[i].SkillID != mutations[j].SkillID {
			return mutations[i].SkillID < mutations[j].SkillID
		}
		return mutations[i].Path < mutations[j].Path
	})
	batches, err := materializedSkillFileBatches(mutations)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.MaterializeRoleSourceSkillFiles(ctx, db.MaterializeRoleSourceSkillFilesParams{
			Files: body, WorkspaceID: s.workspaceID,
		})
		if err != nil {
			return err
		}
		if err := exactMaterializedSkillFiles(batch, rows); err != nil {
			return err
		}
	}
	return nil
}

func exactMaterializedSkillFiles(requested []pendingRoleSourceSkillFileMutation, rows []db.MaterializeRoleSourceSkillFilesRow) error {
	expected := make(map[string]string, len(requested))
	for _, item := range requested {
		key := item.SkillID + "\x00" + item.Path
		if _, duplicate := expected[key]; duplicate {
			return fmt.Errorf("%w: skill-file batch requests duplicate target %s", ErrApplyConflict, item.Path)
		}
		expected[key] = item.Operation
	}
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := util.UUIDToString(row.SkillID) + "\x00" + row.Path
		operation, ok := expected[key]
		if !ok {
			return fmt.Errorf("%w: skill-file batch returned unexpected target %s", ErrApplyConflict, row.Path)
		}
		if operation != row.Operation {
			return fmt.Errorf("%w: skill-file batch returned operation %s for %s, expected %s", ErrApplyConflict, row.Operation, row.Path, operation)
		}
		if returned[key] {
			return fmt.Errorf("%w: skill-file batch returned duplicate target %s", ErrApplyConflict, row.Path)
		}
		returned[key] = true
	}
	if len(returned) != len(expected) {
		return fmt.Errorf("%w: persisted %d of %d source-owned skill-file mutations", ErrApplyConflict, len(returned), len(expected))
	}
	return nil
}

func materializedSkillFileBatches(mutations []pendingRoleSourceSkillFileMutation) ([][]pendingRoleSourceSkillFileMutation, error) {
	if len(mutations) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceSkillFileMutation, 0, (len(mutations)+materializedSkillFileBatchSize-1)/materializedSkillFileBatchSize)
	start, encodedBytes := 0, 2
	for index, mutation := range mutations {
		body, err := json.Marshal(mutation)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedSkillFileBatchBytes {
			return nil, fmt.Errorf("%w: skill file %q exceeds batch byte limit", ErrMaterializationBlocked, mutation.Path)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedSkillFileBatchSize || encodedBytes+separator+len(body) > materializedSkillFileBatchBytes) {
			batches = append(batches, mutations[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, mutations[start:])
	return batches, nil
}

func (s *materializationState) materializeAutomations(ctx context.Context) error {
	if err := s.lockAndRevalidateAutomationTitles(ctx); err != nil {
		return err
	}
	pending := make([]pendingRoleSourceAutomation, 0)
	for _, role := range s.snapshot.Manifest.Roles {
		for _, automation := range role.Automations {
			ref := ObjectRef{Kind: "automation", ParentID: role.ID, ID: automation.ID}
			action := s.actions[objectKey(ref)]
			if action.Operation == PlanUnchanged {
				s.receipt.Counts.Unchanged++
				if err := s.advanceExistingMappingSnapshot(ctx, ref); err != nil {
					return err
				}
				continue
			}
			if action.Operation == PlanArchiveCandidate {
				continue
			}
			roleMapping, ok := s.mappings[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
			if !ok || roleMapping.ArchivedAt.Valid {
				return fmt.Errorf("%w: role mapping is missing for automation %s", ErrApplyConflict, automation.ID)
			}
			mapping, mapped := s.mappings[objectKey(ref)]
			if mapped && mapping.ArchivedAt.Valid {
				return fmt.Errorf("%w: archived automation mapping cannot be reused", ErrApplyConflict)
			}
			targetID := mapping.TargetID
			operation := "update"
			if !mapped {
				var err error
				targetID, err = newPGUUID()
				if err != nil {
					return err
				}
				operation = "create"
			}
			triggerID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(util.UUIDToString(s.source.ID)+"/"+role.ID+"/"+automation.ID))
			pending = append(pending, pendingRoleSourceAutomation{
				Ref: ref, ID: util.UUIDToString(targetID), Operation: operation,
				Title: automationTitle(role.DisplayName, automation.Name), Description: s.artifacts[automation.Prompt.Digest].body,
				AssigneeID: util.UUIDToString(roleMapping.TargetID), CreatedByID: util.UUIDToString(s.actorID),
				TriggerID: triggerID.String(), CronExpression: automation.Schedule, Timezone: automation.Timezone,
				Label: "Managed by role source", ObjectDigest: action.AfterDigest,
			})
		}
	}
	batches, err := materializedAutomationBatches(pending)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.MaterializeRoleSourceAutomations(ctx, db.MaterializeRoleSourceAutomationsParams{
			Automations: body, WorkspaceID: s.workspaceID,
		})
		if err != nil {
			return err
		}
		if _, err := exactMaterializedAutomationIDs(batch, rows); err != nil {
			return err
		}
		for _, item := range batch {
			targetID, err := util.ParseUUID(item.ID)
			if err != nil {
				return err
			}
			if err := s.upsertMapping(ctx, item.Ref, "autopilot", targetID, item.ObjectDigest, []string{"title", "description", "schedule"}, pgtype.Timestamptz{}); err != nil {
				return err
			}
			if item.Operation == "create" {
				s.receipt.Counts.Created++
			} else if _, adopted := s.adoptions[objectKey(item.Ref)]; adopted {
				s.receipt.Counts.Adopted++
			} else {
				s.receipt.Counts.Updated++
			}
		}
	}
	return nil
}

func (s *materializationState) lockAndRevalidateAutomationTitles(ctx context.Context) error {
	names, err := collectMaterializationNames(s.snapshot, s.plan, s.mappings)
	if err != nil {
		return err
	}
	titles := make([]string, 0)
	for _, name := range names {
		if name.TargetKind == "autopilot" {
			titles = append(titles, name.Name)
		}
	}
	if err := autopilotlock.LockTitles(ctx, s.tx, s.workspaceID, titles...); err != nil {
		return err
	}
	// The earlier bounded preflight gives fast diagnostics. This second check
	// runs while every desired automation title is locked, closing the race with
	// ordinary create and rename transactions that use the same lock contract.
	return validateMaterializationNames(ctx, s.q, s.workspaceID, s.snapshot, s.plan, s.mappings)
}

func exactMaterializedAutomationIDs(requested []pendingRoleSourceAutomation, rows []pgtype.UUID) (map[string]bool, error) {
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row)
		if returned[id] {
			return nil, fmt.Errorf("%w: automation batch returned duplicate %s", ErrApplyConflict, id)
		}
		returned[id] = true
	}
	if len(returned) != len(requested) {
		return nil, fmt.Errorf("%w: persisted %d of %d automation targets and triggers", ErrApplyConflict, len(returned), len(requested))
	}
	for _, item := range requested {
		if !returned[item.ID] {
			return nil, fmt.Errorf("%w: automation target or trigger %s was not persisted", ErrApplyConflict, item.ID)
		}
	}
	return returned, nil
}

func materializedAutomationBatches(automations []pendingRoleSourceAutomation) ([][]pendingRoleSourceAutomation, error) {
	if len(automations) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceAutomation, 0, (len(automations)+materializedAutomationBatchSize-1)/materializedAutomationBatchSize)
	start, encodedBytes := 0, 2
	for index, automation := range automations {
		body, err := json.Marshal(automation)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedAutomationBatchBytes {
			return nil, fmt.Errorf("%w: automation target %q exceeds batch byte limit", ErrMaterializationBlocked, automation.Ref.ID)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedAutomationBatchSize || encodedBytes+separator+len(body) > materializedAutomationBatchBytes) {
			batches = append(batches, automations[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, automations[start:])
	return batches, nil
}

func (s *materializationState) materializeArchives(ctx context.Context) error {
	for _, action := range s.plan.Actions {
		if action.Operation != PlanArchiveCandidate {
			continue
		}
		if s.decisions[objectKey(action.Ref)] == ArchiveDecisionRetain {
			s.receipt.Counts.Retained++
			mapping, ok := s.mappings[objectKey(action.Ref)]
			if !ok {
				return fmt.Errorf("%w: retained object has no materialization mapping", ErrApplyConflict)
			}
			if action.Ref.Kind == "environment" || action.Ref.Kind == "mcp" {
				archivedAt := pgtype.Timestamptz{Time: s.now().UTC(), Valid: true}
				if err := s.upsertMapping(ctx, action.Ref, mapping.TargetKind, mapping.TargetID, action.BeforeDigest, ownershipMask(mapping), archivedAt); err != nil {
					return err
				}
			} else if err := s.appendExistingMapping(action.Ref); err != nil {
				return err
			}
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
		case "capability_binding":
			if _, err := s.syncOwnedSkillFiles(ctx, action.Ref, mapping.TargetID, map[string]string{}, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unsupported archive kind %q", ErrMaterializationBlocked, action.Ref.Kind)
		}
		archivedAt := pgtype.Timestamptz{Time: s.now().UTC(), Valid: true}
		mask := ownershipMask(mapping)
		if action.Ref.Kind == "capability_binding" {
			mask = []string{}
		}
		if err := s.upsertMapping(ctx, action.Ref, mapping.TargetKind, mapping.TargetID, action.BeforeDigest, mask, archivedAt); err != nil {
			return err
		}
		s.receipt.Counts.Archived++
	}
	return nil
}

func (s *materializationState) consumeSecretTransfers(ctx context.Context) error {
	roleIDs := make([]string, 0, len(s.secretTransfers))
	for roleID := range s.secretTransfers {
		roleIDs = append(roleIDs, roleID)
	}
	sort.Strings(roleIDs)
	for _, roleID := range roleIDs {
		transfer := s.secretTransfers[roleID]
		consumed, err := s.q.ConsumeRoleSourceSecretTransfer(ctx, db.ConsumeRoleSourceSecretTransferParams{
			ID: transfer.ID, SourceID: s.source.ID, WorkspaceID: s.workspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: secret transfer expired or was already consumed", ErrApplyConflict)
		}
		if err != nil {
			return err
		}
		digest := ""
		if transfer.EnvelopeDigest.Valid {
			digest = transfer.EnvelopeDigest.String
		}
		s.receipt.SecretTransfers = append(s.receipt.SecretTransfers, SecretTransferReceipt{
			RoleID: roleID, TransferID: util.UUIDToString(transfer.ID), EnvelopeDigest: digest,
		})
		if err := s.control.appendAudit(ctx, s.q, s.source, "secret_transfer_consumed", AuditActor{Type: "user", ID: util.UUIDToString(s.actorID)}, AuditPayload{
			OperationID: util.UUIDToString(consumed.ID), SnapshotDigest: consumed.SnapshotDigest,
			PlanDigest: consumed.PlanDigest, Result: digest,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *materializationState) stageAgentSkillBinding(agentID, skillID pgtype.UUID) error {
	if !agentID.Valid || !skillID.Valid {
		return fmt.Errorf("%w: agent-skill binding has an invalid target", ErrApplyConflict)
	}
	if s.pendingAgentSkills == nil {
		s.pendingAgentSkills = make(map[string]pendingRoleSourceAgentSkill)
	}
	key := util.UUIDToString(agentID) + "/" + util.UUIDToString(skillID)
	s.pendingAgentSkills[key] = pendingRoleSourceAgentSkill{
		AgentID: util.UUIDToString(agentID), SkillID: util.UUIDToString(skillID),
	}
	return nil
}

func (s *materializationState) flushAgentSkillBindings(ctx context.Context) error {
	if len(s.pendingAgentSkills) == 0 {
		return nil
	}
	bindings := s.orderedAgentSkillBindings()
	batches, err := materializedAgentSkillBatches(bindings)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.EnsureRoleSourceAgentSkills(ctx, db.EnsureRoleSourceAgentSkillsParams{
			WorkspaceID: s.workspaceID, Bindings: body,
		})
		if err != nil {
			return err
		}
		if err := exactMaterializedAgentSkills(batch, rows); err != nil {
			return err
		}
	}
	return nil
}

func (s *materializationState) orderedAgentSkillBindings() []pendingRoleSourceAgentSkill {
	keys := make([]string, 0, len(s.pendingAgentSkills))
	for key := range s.pendingAgentSkills {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bindings := make([]pendingRoleSourceAgentSkill, 0, len(keys))
	for _, key := range keys {
		bindings = append(bindings, s.pendingAgentSkills[key])
	}
	return bindings
}

func materializedAgentSkillBatches(bindings []pendingRoleSourceAgentSkill) ([][]pendingRoleSourceAgentSkill, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceAgentSkill, 0, (len(bindings)+materializedAgentSkillBatchSize-1)/materializedAgentSkillBatchSize)
	start, encodedBytes := 0, 2
	for index, binding := range bindings {
		body, err := json.Marshal(binding)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedAgentSkillBatchBytes {
			return nil, fmt.Errorf("%w: agent-skill binding exceeds batch byte limit", ErrMaterializationBlocked)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedAgentSkillBatchSize || encodedBytes+separator+len(body) > materializedAgentSkillBatchBytes) {
			batches = append(batches, bindings[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, bindings[start:])
	return batches, nil
}

func exactMaterializedAgentSkills(requested []pendingRoleSourceAgentSkill, rows []db.EnsureRoleSourceAgentSkillsRow) error {
	expected := make(map[string]bool, len(requested))
	for _, item := range requested {
		key := item.AgentID + "/" + item.SkillID
		if expected[key] {
			return fmt.Errorf("%w: agent-skill batch requests duplicate %s", ErrApplyConflict, key)
		}
		expected[key] = true
	}
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := util.UUIDToString(row.AgentID) + "/" + util.UUIDToString(row.SkillID)
		if !expected[key] {
			return fmt.Errorf("%w: agent-skill batch returned unexpected %s", ErrApplyConflict, key)
		}
		if returned[key] {
			return fmt.Errorf("%w: agent-skill batch returned duplicate %s", ErrApplyConflict, key)
		}
		returned[key] = true
	}
	if len(returned) != len(expected) {
		return fmt.Errorf("%w: persisted %d of %d role-source agent-skill bindings", ErrApplyConflict, len(returned), len(expected))
	}
	return nil
}

func (s *materializationState) upsertMapping(_ context.Context, ref ObjectRef, targetKind string, targetID pgtype.UUID, digest string, mask []string, archivedAt pgtype.Timestamptz) error {
	key := objectKey(ref)
	if _, duplicate := s.pendingMappings[key]; duplicate {
		return fmt.Errorf("%w: duplicate mapping mutation for %s", ErrApplyConflict, key)
	}
	body, err := json.Marshal(mask)
	if err != nil {
		return err
	}
	var archivedAtValue *time.Time
	if archivedAt.Valid {
		value := archivedAt.Time.UTC()
		archivedAtValue = &value
	}
	if s.pendingMappings == nil {
		s.pendingMappings = make(map[string]pendingRoleSourceMapping)
	}
	s.pendingMappings[key] = pendingRoleSourceMapping{
		SourceKind: ref.Kind, SourceParentID: ref.ParentID, SourceObjectID: ref.ID,
		TargetKind: targetKind, TargetID: util.UUIDToString(targetID), OwnershipMask: mask,
		LastAppliedDigest: digest, LastSnapshotDigest: s.snapshot.SnapshotDigest, ArchivedAt: archivedAtValue,
	}
	row := s.mappings[key]
	row.SourceID, row.WorkspaceID = s.source.ID, s.workspaceID
	row.SourceKind, row.SourceParentID, row.SourceObjectID = ref.Kind, ref.ParentID, ref.ID
	row.TargetKind, row.TargetID, row.OwnershipMask = targetKind, targetID, body
	row.LastAppliedDigest, row.LastSnapshotDigest, row.ArchivedAt = digest, s.snapshot.SnapshotDigest, archivedAt
	s.mappings[key] = row
	s.receipt.Mappings = append(s.receipt.Mappings, ApplyMapping{Source: ref, Target: targetKind, ID: util.UUIDToString(targetID)})
	return nil
}

func (s *materializationState) flushMappings(ctx context.Context) error {
	if len(s.pendingMappings) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.pendingMappings))
	for key := range s.pendingMappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	mutations := make([]pendingRoleSourceMapping, 0, len(keys))
	for _, key := range keys {
		mutations = append(mutations, s.pendingMappings[key])
	}
	batches, err := materializedMappingBatches(mutations)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		rows, err := s.q.UpsertRoleSourceObjectMappings(ctx, db.UpsertRoleSourceObjectMappingsParams{
			Mappings: body, SourceID: s.source.ID, WorkspaceID: s.workspaceID,
		})
		if err != nil {
			return err
		}
		if err := exactMaterializedMappings(batch, rows); err != nil {
			return err
		}
		for _, row := range rows {
			ref := ObjectRef{Kind: row.SourceKind, ParentID: row.SourceParentID, ID: row.SourceObjectID}
			s.mappings[objectKey(ref)] = row
		}
	}
	return nil
}

func materializedMappingBatches(mappings []pendingRoleSourceMapping) ([][]pendingRoleSourceMapping, error) {
	if len(mappings) == 0 {
		return nil, nil
	}
	batches := make([][]pendingRoleSourceMapping, 0, (len(mappings)+materializedMappingBatchSize-1)/materializedMappingBatchSize)
	start, encodedBytes := 0, 2
	for index, mapping := range mappings {
		body, err := json.Marshal(mapping)
		if err != nil {
			return nil, err
		}
		if len(body)+2 > materializedMappingBatchBytes {
			return nil, fmt.Errorf("%w: mapping %q exceeds batch byte limit", ErrMaterializationBlocked, mapping.SourceObjectID)
		}
		separator := 0
		if index > start {
			separator = 1
		}
		if index > start && (index-start >= materializedMappingBatchSize || encodedBytes+separator+len(body) > materializedMappingBatchBytes) {
			batches = append(batches, mappings[start:index])
			start, encodedBytes, separator = index, 2, 0
		}
		encodedBytes += separator + len(body)
	}
	batches = append(batches, mappings[start:])
	return batches, nil
}

func exactMaterializedMappings(requested []pendingRoleSourceMapping, rows []db.RoleSourceObjectMapping) error {
	expected := make(map[string]bool, len(requested))
	for _, item := range requested {
		key := objectKey(ObjectRef{Kind: item.SourceKind, ParentID: item.SourceParentID, ID: item.SourceObjectID})
		if expected[key] {
			return fmt.Errorf("%w: mapping batch requests duplicate %s", ErrApplyConflict, key)
		}
		expected[key] = true
	}
	returned := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := objectKey(ObjectRef{Kind: row.SourceKind, ParentID: row.SourceParentID, ID: row.SourceObjectID})
		if !expected[key] {
			return fmt.Errorf("%w: mapping batch returned unexpected %s", ErrApplyConflict, key)
		}
		if returned[key] {
			return fmt.Errorf("%w: mapping batch returned duplicate %s", ErrApplyConflict, key)
		}
		returned[key] = true
	}
	if len(returned) != len(expected) {
		return fmt.Errorf("%w: persisted %d of %d mapping mutations", ErrApplyConflict, len(returned), len(expected))
	}
	return nil
}

func (s *materializationState) advanceExistingMappingSnapshot(ctx context.Context, ref ObjectRef) error {
	mapping, ok := s.mappings[objectKey(ref)]
	if !ok {
		return fmt.Errorf("%w: unchanged object has no materialization mapping", ErrApplyConflict)
	}
	// Even when this object is byte-identical, advance its snapshot provenance.
	// Runtime pins use the snapshot as the transitive role dependency boundary:
	// a skill/capability change elsewhere in the new source snapshot must not
	// leave the role mapping pointing at the previous source state.
	return s.upsertMapping(ctx, ref, mapping.TargetKind, mapping.TargetID, mapping.LastAppliedDigest, ownershipMask(mapping), mapping.ArchivedAt)
}

func (s *materializationState) appendExistingMapping(ref ObjectRef) error {
	mapping, ok := s.mappings[objectKey(ref)]
	if !ok {
		return fmt.Errorf("%w: retained object has no materialization mapping", ErrApplyConflict)
	}
	// A retained archive candidate no longer exists in the destination
	// snapshot. Keep its last_snapshot_digest pointing at the last snapshot
	// that actually contained it; advancing would create dangling provenance.
	s.receipt.Mappings = append(s.receipt.Mappings, ApplyMapping{
		Source: ref, Target: mapping.TargetKind, ID: util.UUIDToString(mapping.TargetID),
	})
	return nil
}

func ownershipMask(mapping db.RoleSourceObjectMapping) []string {
	var mask []string
	if err := json.Unmarshal(mapping.OwnershipMask, &mask); err != nil {
		return []string{}
	}
	return mask
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
	legacyModeValid := receipt.ContractVersion == "1.0" && receipt.Mode == "" && row.Mode == "apply"
	currentModeValid := (receipt.ContractVersion == "1.1" || receipt.ContractVersion == ApplyReceiptContractVersion) &&
		receipt.Mode == row.Mode && (receipt.Mode == "apply" || receipt.Mode == "rollback")
	if receipt.ReceiptDigest != row.ReceiptDigest.String || digest != row.ReceiptDigest.String || receipt.ApplyID != util.UUIDToString(row.ID) ||
		receipt.PlanDigest != row.PlanDigest || receipt.SnapshotDigest != row.SnapshotDigest || (!legacyModeValid && !currentModeValid) {
		return receipt, errors.New("stored apply receipt does not match indexed apply record")
	}
	return receipt, nil
}

// DecodePersistedApplyReceipt exposes the same strict receipt validation to
// offline disaster-recovery verification. It never returns raw secret data;
// receipts contain only immutable identifiers, counts and digests.
func DecodePersistedApplyReceipt(row db.RoleSourceApply) (ApplyReceipt, error) {
	return decodeApplyReceipt(row)
}
