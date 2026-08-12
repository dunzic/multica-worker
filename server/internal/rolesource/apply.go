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
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"golang.org/x/sync/errgroup"
)

const (
	ApplyReceiptContractVersion = "1.1"
	maxApplyArtifacts           = 20_000
	maxApplyArtifactBytes       = 128 << 20
	maxConcurrentArtifactReads  = 16
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
	control         *ControlPlane
	q               *db.Queries
	workspaceID     pgtype.UUID
	source          db.RoleSource
	actorID         pgtype.UUID
	snapshot        Snapshot
	plan            Plan
	decisions       map[string]ArchiveDecision
	actions         map[string]PlanAction
	artifacts       map[string]verifiedArtifact
	mappings        map[string]db.RoleSourceObjectMapping
	secretPayloads  map[string]SecretEnvelopePayload
	secretTransfers map[string]db.RoleSourceSecretTransfer
	runtimeMode     string
	now             func() time.Time
	receipt         *ApplyReceipt
	pendingMappings map[string]pendingRoleSourceMapping
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

type applyPreflight struct {
	snapshotDigest string
	artifacts      map[string]verifiedArtifact
	existing       *db.RoleSourceApply
	receipt        *ApplyReceipt
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
	receipt := ApplyReceipt{
		ContractVersion: ApplyReceiptContractVersion, Mode: applyMode, ApplyID: util.UUIDToString(applyRow.ID),
		SourceID: input.SourceID, WorkspaceID: input.WorkspaceID, SnapshotDigest: plan.ToSnapshotDigest,
		FromSnapshotDigest: plan.FromSnapshotDigest,
		PlanDigest:         plan.PlanDigest, ApprovalID: input.ApprovalID, Mappings: []ApplyMapping{}, SecretTransfers: []SecretTransferReceipt{},
	}
	state := materializationState{
		control: c, q: qtx, workspaceID: workspaceID, source: source, actorID: actorID, snapshot: snapshot, plan: plan,
		decisions: decisions, actions: actionIndex(plan), artifacts: artifacts, mappings: mappingIndex(mappingRows),
		secretPayloads: secretPayloads, secretTransfers: secretTransfers,
		runtimeMode: runtime.RuntimeMode, now: c.now, receipt: &receipt,
		pendingMappings: make(map[string]pendingRoleSourceMapping),
	}
	if err := state.materialize(ctx); err != nil {
		return db.RoleSourceApply{}, ApplyReceipt{}, err
	}
	if err := state.consumeSecretTransfers(ctx); err != nil {
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
		if len(role.CapabilityBindings) > 0 {
			return fmt.Errorf("%w: capability bindings require a dedicated target contract", ErrMaterializationBlocked)
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
		case "capability_binding":
			if action.Operation == PlanCreate || action.Operation == PlanUpdate {
				return fmt.Errorf("%w: %s objects do not have a safe target contract", ErrMaterializationBlocked, action.Ref.Kind)
			}
		case "environment", "mcp":
		default:
			return fmt.Errorf("%w: unsupported object kind %q", ErrMaterializationBlocked, action.Ref.Kind)
		}
		if action.Operation == PlanArchiveCandidate && decisions[objectKey(action.Ref)] == ArchiveDecisionArchive && action.Ref.Kind != "role" && action.Ref.Kind != "automation" {
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
	decisions, err := decodeApprovedDecisions(plan, approval)
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
		if err := s.materializeRoleSecrets(ctx, role); err != nil {
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
	if err := s.materializeArchives(ctx); err != nil {
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
		return s.advanceExistingMappingSnapshot(ctx, ref)
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
		return s.advanceExistingMappingSnapshot(ctx, ref)
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
		return s.advanceExistingMappingSnapshot(ctx, ref)
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
	body, err := json.Marshal(mutations)
	if err != nil {
		return err
	}
	rows, err := s.q.UpsertRoleSourceObjectMappings(ctx, db.UpsertRoleSourceObjectMappingsParams{
		Mappings: body, SourceID: s.source.ID, WorkspaceID: s.workspaceID,
	})
	if err != nil {
		return err
	}
	if len(rows) != len(mutations) {
		return fmt.Errorf("%w: persisted %d of %d mapping mutations", ErrApplyConflict, len(rows), len(mutations))
	}
	for _, row := range rows {
		ref := ObjectRef{Kind: row.SourceKind, ParentID: row.SourceParentID, ID: row.SourceObjectID}
		s.mappings[objectKey(ref)] = row
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
	legacyModeValid := receipt.ContractVersion == "1.0" && receipt.Mode == "" && row.Mode == "apply"
	currentModeValid := receipt.ContractVersion == ApplyReceiptContractVersion && receipt.Mode == row.Mode && (receipt.Mode == "apply" || receipt.Mode == "rollback")
	if receipt.ReceiptDigest != row.ReceiptDigest.String || digest != row.ReceiptDigest.String || receipt.ApplyID != util.UUIDToString(row.ID) ||
		receipt.PlanDigest != row.PlanDigest || receipt.SnapshotDigest != row.SnapshotDigest || (!legacyModeValid && !currentModeValid) {
		return receipt, errors.New("stored apply receipt does not match indexed apply record")
	}
	return receipt, nil
}
