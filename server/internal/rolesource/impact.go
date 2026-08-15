package rolesource

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	PlanImpactContractVersion = "1.0"
	maxImpactWorkers          = 200
	maxImpactTaskDetails      = 200
)

type PlanImpactSummary struct {
	NewRoles                   int   `json:"new_roles"`
	MandatoryRefreshRoles      int   `json:"mandatory_refresh_roles"`
	ConditionalArchiveRoles    int   `json:"conditional_archive_roles"`
	UnmappedExistingRoles      int   `json:"unmapped_existing_roles"`
	CancelOnApply              int64 `json:"cancel_on_apply"`
	ConditionalCancelOnArchive int64 `json:"conditional_cancel_on_archive"`
	ContinueCurrentVersion     int64 `json:"continue_current_version"`
	WorkerDetailsTruncated     bool  `json:"worker_details_truncated"`
	TaskDetailsTruncated       bool  `json:"task_details_truncated"`
}

type PlanImpactWorker struct {
	SourceRoleID          string `json:"source_role_id"`
	AgentID               string `json:"agent_id"`
	AgentName             string `json:"agent_name"`
	Effect                string `json:"effect"`
	PreStartTasks         int64  `json:"pre_start_tasks"`
	RunningTasks          int64  `json:"running_tasks"`
	CurrentSnapshotDigest string `json:"current_snapshot_digest"`
}

type PlanImpactTask struct {
	TaskID       string `json:"task_id"`
	SourceRoleID string `json:"source_role_id"`
	AgentID      string `json:"agent_id"`
	Status       string `json:"status"`
	Effect       string `json:"effect"`
	CreatedAt    string `json:"created_at"`
}

type PlanImpact struct {
	ContractVersion      string             `json:"contract_version"`
	SourceID             string             `json:"source_id"`
	PlanDigest           string             `json:"plan_digest"`
	TargetSnapshotDigest string             `json:"target_snapshot_digest"`
	Applyable            bool               `json:"applyable"`
	GeneratedAt          string             `json:"generated_at"`
	Summary              PlanImpactSummary  `json:"summary"`
	Workers              []PlanImpactWorker `json:"workers"`
	Tasks                []PlanImpactTask   `json:"tasks"`
}

type impactRoleEffect string

const (
	impactProvenanceRefresh  impactRoleEffect = "provenance_refresh"
	impactConditionalArchive impactRoleEffect = "conditional_archive"
)

func planImpactRoleEffects(plan Plan) (map[string]impactRoleEffect, int) {
	effects := make(map[string]impactRoleEffect)
	newRoles := 0
	for _, action := range plan.Actions {
		if action.Ref.Kind != "role" {
			continue
		}
		operation := action.Operation
		if operation == PlanBlocked && action.ProposedOperation != "" {
			operation = action.ProposedOperation
		}
		switch operation {
		case PlanCreate:
			newRoles++
		case PlanArchiveCandidate:
			effects[action.Ref.ID] = impactConditionalArchive
		case PlanUpdate, PlanUnchanged:
			effects[action.Ref.ID] = impactProvenanceRefresh
		}
	}
	return effects, newRoles
}

func impactRoleIDs(effects map[string]impactRoleEffect) []string {
	ids := make([]string, 0, len(effects))
	for id := range effects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func conditionalImpactRoleIDs(effects map[string]impactRoleEffect) []string {
	ids := make([]string, 0)
	for id, effect := range effects {
		if effect == impactConditionalArchive {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func buildPlanImpact(
	plan Plan,
	roleRows []db.ListRoleSourceRoleImpactRowsRow,
	taskRows []db.ListRoleSourceTaskImpactRowsRow,
	generatedAt time.Time,
) (PlanImpact, error) {
	if err := ValidatePlan(plan); err != nil {
		return PlanImpact{}, err
	}
	effects, newRoles := planImpactRoleEffects(plan)
	impact := PlanImpact{
		ContractVersion: PlanImpactContractVersion,
		SourceID:        plan.SourceID, PlanDigest: plan.PlanDigest,
		TargetSnapshotDigest: plan.ToSnapshotDigest, Applyable: plan.Applyable,
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339Nano),
		Summary:     PlanImpactSummary{NewRoles: newRoles},
		Workers:     []PlanImpactWorker{}, Tasks: []PlanImpactTask{},
	}
	included := make(map[string]impactRoleEffect, len(roleRows))
	mapped := make(map[string]bool, len(roleRows))
	for _, row := range roleRows {
		effect, ok := effects[row.SourceRoleID]
		if !ok {
			continue
		}
		mapped[row.SourceRoleID] = true
		if effect == impactProvenanceRefresh && row.LastSnapshotDigest == plan.ToSnapshotDigest {
			continue
		}
		included[row.SourceRoleID] = effect
		if effect == impactConditionalArchive {
			impact.Summary.ConditionalArchiveRoles++
			impact.Summary.ConditionalCancelOnArchive += row.CancelOnApply
		} else {
			impact.Summary.MandatoryRefreshRoles++
			impact.Summary.CancelOnApply += row.CancelOnApply
		}
		impact.Summary.ContinueCurrentVersion += row.ContinueCurrentVersion
		if len(impact.Workers) < maxImpactWorkers {
			impact.Workers = append(impact.Workers, PlanImpactWorker{
				SourceRoleID: row.SourceRoleID,
				AgentID:      util.UUIDToString(row.AgentID), AgentName: row.AgentName,
				Effect: string(effect), PreStartTasks: row.CancelOnApply,
				RunningTasks:          row.ContinueCurrentVersion,
				CurrentSnapshotDigest: row.LastSnapshotDigest,
			})
		} else {
			impact.Summary.WorkerDetailsTruncated = true
		}
	}
	for roleID := range effects {
		if !mapped[roleID] {
			impact.Summary.UnmappedExistingRoles++
		}
	}

	if len(taskRows) > maxImpactTaskDetails {
		impact.Summary.TaskDetailsTruncated = true
		taskRows = taskRows[:maxImpactTaskDetails]
	}
	for _, row := range taskRows {
		effect, ok := included[row.SourceRoleID]
		if !ok {
			continue
		}
		taskEffect := "continue_current_version"
		if row.Status == "queued" || row.Status == "deferred" || row.Status == "dispatched" {
			if effect == impactConditionalArchive {
				taskEffect = "cancel_if_archived"
			} else {
				taskEffect = "cancel_on_apply"
			}
		}
		impact.Tasks = append(impact.Tasks, PlanImpactTask{
			TaskID: util.UUIDToString(row.TaskID), SourceRoleID: row.SourceRoleID,
			AgentID: util.UUIDToString(row.AgentID), Status: row.Status, Effect: taskEffect,
			CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	return impact, nil
}

func (c *ControlPlane) GetPlanImpact(ctx context.Context, workspaceIDText, sourceIDText, planDigest string) (PlanImpact, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil || !sha256Pattern.MatchString(planDigest) {
		return PlanImpact{}, errors.New("invalid plan identity")
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return PlanImpact{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return PlanImpact{}, err
	}
	qtx := db.New(tx)
	generatedAt := c.now()
	row, err := qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{
		SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: planDigest,
	})
	if err != nil {
		return PlanImpact{}, err
	}
	plan, err := DecodePersistedPlan(row)
	if err != nil {
		return PlanImpact{}, err
	}
	effects, _ := planImpactRoleEffects(plan)
	roleIDs := impactRoleIDs(effects)
	if len(roleIDs) == 0 {
		impact, err := buildPlanImpact(plan, nil, nil, generatedAt)
		if err != nil {
			return PlanImpact{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PlanImpact{}, err
		}
		return impact, nil
	}
	roles, err := qtx.ListRoleSourceRoleImpactRows(ctx, db.ListRoleSourceRoleImpactRowsParams{
		WorkspaceID: workspaceID, SourceID: sourceID, SourceRoleIds: roleIDs,
	})
	if err != nil {
		return PlanImpact{}, err
	}
	tasks, err := qtx.ListRoleSourceTaskImpactRows(ctx, db.ListRoleSourceTaskImpactRowsParams{
		WorkspaceID: workspaceID, SourceID: sourceID, SourceRoleIds: roleIDs,
		TargetSnapshotDigest:      plan.ToSnapshotDigest,
		ConditionalArchiveRoleIds: conditionalImpactRoleIDs(effects),
		ResultLimit:               maxImpactTaskDetails + 1,
	})
	if err != nil {
		return PlanImpact{}, err
	}
	impact, err := buildPlanImpact(plan, roles, tasks, generatedAt)
	if err != nil {
		return PlanImpact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PlanImpact{}, err
	}
	return impact, nil
}
