package rolesource

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBuildPlanImpactSeparatesMandatoryConditionalAndRunningWork(t *testing.T) {
	fromManifest := planTestManifest()
	fromManifest.Roles = append(fromManifest.Roles, Role{
		ID: "obsolete", DisplayName: "Obsolete",
		Instructions: testArtifact("roles/obsolete/instructions.md"),
	})
	from := planTestSnapshot(t, fromManifest)
	toManifest := planTestManifest()
	toManifest.Roles[0].DisplayName = "Writer v2"
	to := planTestSnapshot(t, toManifest)
	plan, err := BuildPlan("source-1", &from, to)
	if err != nil {
		t.Fatal(err)
	}
	writerAgent := util.MustParseUUID("00000000-0000-4000-8000-000000000011")
	obsoleteAgent := util.MustParseUUID("00000000-0000-4000-8000-000000000012")
	createdAt := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC), Valid: true}
	impact, err := buildPlanImpact(plan, []db.ListRoleSourceRoleImpactRowsRow{
		{SourceRoleID: "obsolete", AgentID: obsoleteAgent, AgentName: "Obsolete", LastSnapshotDigest: from.SnapshotDigest, CancelOnApply: 3, ContinueCurrentVersion: 1},
		{SourceRoleID: "writer", AgentID: writerAgent, AgentName: "Writer", LastSnapshotDigest: from.SnapshotDigest, CancelOnApply: 2, ContinueCurrentVersion: 4},
	}, []db.ListRoleSourceTaskImpactRowsRow{
		{TaskID: util.MustParseUUID("00000000-0000-4000-8000-000000000021"), SourceRoleID: "writer", AgentID: writerAgent, Status: "queued", CreatedAt: createdAt},
		{TaskID: util.MustParseUUID("00000000-0000-4000-8000-000000000022"), SourceRoleID: "obsolete", AgentID: obsoleteAgent, Status: "dispatched", CreatedAt: createdAt},
		{TaskID: util.MustParseUUID("00000000-0000-4000-8000-000000000023"), SourceRoleID: "writer", AgentID: writerAgent, Status: "running", CreatedAt: createdAt},
	}, createdAt.Time)
	if err != nil {
		t.Fatal(err)
	}
	if impact.Summary.MandatoryRefreshRoles != 1 || impact.Summary.ConditionalArchiveRoles != 1 ||
		impact.Summary.CancelOnApply != 2 || impact.Summary.ConditionalCancelOnArchive != 3 ||
		impact.Summary.ContinueCurrentVersion != 5 {
		t.Fatalf("unexpected impact summary: %+v", impact.Summary)
	}
	wantEffects := []string{"cancel_on_apply", "cancel_if_archived", "continue_current_version"}
	for index, want := range wantEffects {
		if impact.Tasks[index].Effect != want {
			t.Fatalf("task %d effect=%q want=%q", index, impact.Tasks[index].Effect, want)
		}
	}
}

func TestBuildPlanImpactSkipsAlreadyAdvancedMappingAndReportsMissingMapping(t *testing.T) {
	from := planTestSnapshot(t, planTestManifest())
	toManifest := planTestManifest()
	toManifest.Roles[0].DisplayName = "Writer v2"
	to := planTestSnapshot(t, toManifest)
	plan, err := BuildPlan("source-1", &from, to)
	if err != nil {
		t.Fatal(err)
	}
	impact, err := buildPlanImpact(plan, []db.ListRoleSourceRoleImpactRowsRow{{
		SourceRoleID: "writer", AgentID: util.MustParseUUID("00000000-0000-4000-8000-000000000011"),
		AgentName: "Writer", LastSnapshotDigest: to.SnapshotDigest,
	}}, nil, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Summary.MandatoryRefreshRoles != 0 || impact.Summary.UnmappedExistingRoles != 0 || len(impact.Workers) != 0 {
		t.Fatalf("already advanced mapping was reported as impact: %+v", impact)
	}

	impact, err = buildPlanImpact(plan, nil, nil, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Summary.UnmappedExistingRoles != 1 {
		t.Fatalf("missing mapping was not surfaced: %+v", impact.Summary)
	}
}

func TestBuildPlanImpactRefreshesRoleWhenOnlyChildSkillChanged(t *testing.T) {
	from := planTestSnapshot(t, planTestManifest())
	toManifest := planTestManifest()
	toManifest.Roles[0].Skills[0].Name = "Draft v2"
	to := planTestSnapshot(t, toManifest)
	plan, err := BuildPlan("source-1", &from, to)
	if err != nil {
		t.Fatal(err)
	}
	roleOperation := PlanOperation("")
	for _, action := range plan.Actions {
		if action.Ref == (ObjectRef{Kind: "role", ID: "writer"}) {
			roleOperation = action.Operation
		}
	}
	if roleOperation != PlanUnchanged {
		t.Fatalf("test precondition: role operation=%q want unchanged", roleOperation)
	}
	impact, err := buildPlanImpact(plan, []db.ListRoleSourceRoleImpactRowsRow{{
		SourceRoleID: "writer", AgentID: util.MustParseUUID("00000000-0000-4000-8000-000000000011"),
		AgentName: "Writer", LastSnapshotDigest: from.SnapshotDigest, CancelOnApply: 2,
	}}, nil, time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Summary.MandatoryRefreshRoles != 1 || impact.Summary.CancelOnApply != 2 {
		t.Fatalf("transitive skill change did not refresh role provenance: %+v", impact.Summary)
	}
}
