package rolesource

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRoleSourceMaterializationNameRacesPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_NAME_RACE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_NAME_RACE_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	userID, workspaceID, baseAgentID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Name race actor',$2)`, userID, "name-race-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Name race workspace',$2)`, workspaceID, "name-race-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent (id,workspace_id,name,runtime_mode) VALUES ($1,$2,'Skill race assignee','local')`, baseAgentID, workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupAdoptionPostgresFixture(t, pool, userID, workspaceID, workspaceID) })

	t.Run("agent_name_claim", func(t *testing.T) {
		const targetName = "Concurrent Role Name"
		winnerPool, _ := newApplyReplicaPool(t, ctx, "agent-name-winner")
		loserPool, loserName := newApplyReplicaPool(t, ctx, "agent-name-loser")
		winnerTx, err := winnerPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer winnerTx.Rollback(ctx) //nolint:errcheck
		winnerID := uuid.New()
		if _, err := winnerTx.Exec(ctx, `INSERT INTO agent (id,workspace_id,name,runtime_mode) VALUES ($1,$2,$3,'local')`, winnerID, workspaceID, targetName); err != nil {
			t.Fatal(err)
		}
		loserTx, err := loserPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer loserTx.Rollback(ctx) //nolint:errcheck
		manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{{
			ID: "name-race-role", DisplayName: targetName, Instructions: testArtifact("roles/name-race/instructions.md"),
		}}}
		snapshot := planTestSnapshot(t, manifest)
		plan, err := BuildPlan(uuid.NewString(), nil, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		state := materializationState{
			q: db.New(loserTx), tx: loserTx, workspaceID: util.MustParseUUID(workspaceID.String()),
			source:  db.RoleSource{ID: util.MustParseUUID(uuid.NewString()), RuntimeID: util.MustParseUUID(uuid.NewString())},
			actorID: util.MustParseUUID(userID.String()), snapshot: snapshot, plan: plan, actions: actionIndex(plan),
			artifacts: map[string]verifiedArtifact{manifest.Roles[0].Instructions.Digest: {body: "managed role instructions"}},
			mappings:  map[string]db.RoleSourceObjectMapping{}, pendingMappings: map[string]pendingRoleSourceMapping{},
			runtimeMode: "local", receipt: &ApplyReceipt{},
		}
		result := make(chan error, 1)
		go func() { result <- state.materializeRoles(ctx) }()
		waitEvent := awaitReplicaLockWait(t, ctx, pool, loserName)
		if err := winnerTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertNameRaceResult(t, result)
		if err := loserTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertSingleNamedTarget(t, ctx, pool, "agent", workspaceID, targetName, winnerID)
		t.Logf("name_race_evidence target=agent wait_event=%s winner_rows=1 loser_error=state_conflict", waitEvent)
	})

	t.Run("skill_name_claim", func(t *testing.T) {
		const targetName = "Concurrent Skill Name"
		winnerPool, _ := newApplyReplicaPool(t, ctx, "skill-name-winner")
		loserPool, loserName := newApplyReplicaPool(t, ctx, "skill-name-loser")
		winnerTx, err := winnerPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer winnerTx.Rollback(ctx) //nolint:errcheck
		winnerID := uuid.New()
		if _, err := winnerTx.Exec(ctx, `INSERT INTO skill (id,workspace_id,name,created_by) VALUES ($1,$2,$3,$4)`, winnerID, workspaceID, targetName, userID); err != nil {
			t.Fatal(err)
		}
		loserTx, err := loserPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer loserTx.Rollback(ctx) //nolint:errcheck
		manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{{
			ID: "skill-race-role", DisplayName: "Skill race role", Instructions: testArtifact("roles/skill-race/instructions.md"),
			Skills: []Skill{{ID: "skill-race", Name: targetName, Version: "1.0.0", Entrypoint: testArtifact("roles/skill-race/SKILL.md")}},
		}}}
		snapshot := planTestSnapshot(t, manifest)
		plan, err := BuildPlan(uuid.NewString(), nil, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		roleRef := ObjectRef{Kind: "role", ID: manifest.Roles[0].ID}
		state := materializationState{
			q: db.New(loserTx), tx: loserTx, workspaceID: util.MustParseUUID(workspaceID.String()),
			source:  db.RoleSource{ID: util.MustParseUUID(uuid.NewString()), RuntimeID: util.MustParseUUID(uuid.NewString())},
			actorID: util.MustParseUUID(userID.String()), snapshot: snapshot, plan: plan, actions: actionIndex(plan),
			artifacts: map[string]verifiedArtifact{manifest.Roles[0].Skills[0].Entrypoint.Digest: {body: "managed skill body"}},
			mappings: map[string]db.RoleSourceObjectMapping{objectKey(roleRef): {
				SourceKind: "role", SourceObjectID: roleRef.ID, TargetKind: "agent", TargetID: util.MustParseUUID(baseAgentID.String()),
			}},
			pendingMappings: map[string]pendingRoleSourceMapping{}, pendingAgentSkills: map[string]pendingRoleSourceAgentSkill{},
			receipt: &ApplyReceipt{},
		}
		result := make(chan error, 1)
		go func() { result <- state.materializeSkills(ctx) }()
		waitEvent := awaitReplicaLockWait(t, ctx, pool, loserName)
		if err := winnerTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertNameRaceResult(t, result)
		if err := loserTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertSingleNamedTarget(t, ctx, pool, "skill", workspaceID, targetName, winnerID)
		t.Logf("name_race_evidence target=skill wait_event=%s winner_rows=1 loser_error=state_conflict", waitEvent)
	})
}

func assertNameRaceResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, ErrApplyConflict) {
			t.Fatalf("name-race loser error=%v, want ErrApplyConflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("name-race loser did not finish after winner commit")
	}
}

func assertSingleNamedTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, workspaceID uuid.UUID, name string, winnerID uuid.UUID) {
	t.Helper()
	table := map[string]string{"agent": "agent", "skill": "skill"}[kind]
	if table == "" {
		t.Fatalf("unsupported target kind %q", kind)
	}
	var total, winners, mappings int
	query := `SELECT count(*), count(*) FILTER (WHERE id=$3) FROM ` + table + ` WHERE workspace_id=$1 AND name=$2`
	if err := pool.QueryRow(ctx, query, workspaceID, name, winnerID).Scan(&total, &winners); err != nil || total != 1 || winners != 1 {
		t.Fatalf("%s name-race rows=%d winners=%d err=%v", kind, total, winners, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_object_mapping WHERE workspace_id=$1`, workspaceID).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("%s name-race mapping residue=%d err=%v", kind, mappings, err)
	}
}
