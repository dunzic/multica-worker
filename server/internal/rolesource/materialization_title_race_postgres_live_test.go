package rolesource

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/autopilotlock"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const automationTitleRaceRoleName = "Automation title race assignee"

type ordinaryAutopilotWriteResult struct {
	conflict bool
	id       uuid.UUID
	err      error
}

// This opt-in PostgreSQL 17 matrix exercises the shared advisory-title lock
// from two independent connections. Ordinary Autopilots retain duplicate-title
// semantics, while an active Role Source mapping owns its exact title.
func TestRoleSourceAutomationTitleRacesPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_TITLE_RACE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_TITLE_RACE_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	userID, workspaceID, baseAgentID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Title race actor',$2)`, userID, "title-race-"+uuid.NewString()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Title race workspace',$2)`, workspaceID, "title-race-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent (id,workspace_id,name,runtime_mode) VALUES ($1,$2,$3,'local')`, baseAgentID, workspaceID, automationTitleRaceRoleName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupAdoptionPostgresFixture(t, pool, userID, workspaceID, workspaceID) })

	t.Run("ordinary_create_commit_makes_role_source_lose", func(t *testing.T) {
		const automationName = "Ordinary create winner"
		title := automationTitle(automationTitleRaceRoleName, automationName)
		ordinaryPool, _ := newApplyReplicaPool(t, ctx, "title-ordinary-win")
		managedPool, managedName := newApplyReplicaPool(t, ctx, "title-managed-lose")
		ordinaryTx, err := ordinaryPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer ordinaryTx.Rollback(ctx) //nolint:errcheck
		if err := autopilotlock.LockTitles(ctx, ordinaryTx, pgUUID(workspaceID), title); err != nil {
			t.Fatal(err)
		}
		conflict, err := autopilotlock.HasTitleConflict(ctx, ordinaryTx, pgUUID(workspaceID), title, pgtype.UUID{})
		if err != nil || conflict {
			t.Fatalf("ordinary preflight conflict=%t err=%v", conflict, err)
		}
		ordinaryID := uuid.New()
		if _, err := ordinaryTx.Exec(ctx, `
INSERT INTO autopilot (id,workspace_id,title,assignee_id,created_by_type,created_by_id)
VALUES ($1,$2,$3,$4,'member',$5)
`, ordinaryID, workspaceID, title, baseAgentID, userID); err != nil {
			t.Fatal(err)
		}

		managedTx, err := managedPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer managedTx.Rollback(ctx) //nolint:errcheck
		state := newAutomationTitleRaceState(t, managedTx, workspaceID, userID, baseAgentID, automationName)
		result := make(chan error, 1)
		go func() { result <- state.materializeAutomations(ctx) }()
		waitEvent := awaitReplicaLockWait(t, ctx, pool, managedName)
		if err := ordinaryTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertAutomationTitleRaceConflict(t, result)
		if err := managedTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		assertOrdinaryAutomationTitle(t, ctx, pool, workspaceID, title, ordinaryID)
		t.Logf("title_race_evidence case=ordinary_create_commit managed_wait_event=%s winner=ordinary loser=state_conflict", waitEvent)
	})

	t.Run("role_source_commit_blocks_ordinary_create", func(t *testing.T) {
		const automationName = "Managed create winner"
		title := automationTitle(automationTitleRaceRoleName, automationName)
		managedPool, _ := newApplyReplicaPool(t, ctx, "title-managed-win")
		ordinaryPool, ordinaryName := newApplyReplicaPool(t, ctx, "title-ordinary-lose")
		managedTx, err := managedPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer managedTx.Rollback(ctx) //nolint:errcheck
		state := newAutomationTitleRaceState(t, managedTx, workspaceID, userID, baseAgentID, automationName)
		materializeAndFlushAutomation(t, ctx, state)

		ordinaryResult := runOrdinaryAutopilotCreate(ctx, ordinaryPool, workspaceID, userID, baseAgentID, title)
		waitEvent := awaitReplicaLockWait(t, ctx, pool, ordinaryName)
		if err := managedTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		result := awaitOrdinaryAutopilotWrite(t, ordinaryResult)
		if result.err != nil || !result.conflict {
			t.Fatalf("ordinary create result=%+v, want managed-title conflict", result)
		}
		assertManagedAutomationTitle(t, ctx, pool, workspaceID, title)
		t.Logf("title_race_evidence case=managed_commit_ordinary_create managed_winner=true ordinary_wait_event=%s ordinary_conflict=true", waitEvent)
	})

	t.Run("role_source_commit_blocks_ordinary_rename_into_title", func(t *testing.T) {
		const automationName = "Managed rename winner"
		title := automationTitle(automationTitleRaceRoleName, automationName)
		oldTitle := title + " old"
		ordinaryID := insertOrdinaryAutopilot(t, ctx, pool, workspaceID, userID, baseAgentID, oldTitle)
		managedPool, _ := newApplyReplicaPool(t, ctx, "rename-managed-win")
		ordinaryPool, ordinaryName := newApplyReplicaPool(t, ctx, "rename-ordinary-lose")
		managedTx, err := managedPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer managedTx.Rollback(ctx) //nolint:errcheck
		state := newAutomationTitleRaceState(t, managedTx, workspaceID, userID, baseAgentID, automationName)
		materializeAndFlushAutomation(t, ctx, state)

		renameResult := runOrdinaryAutopilotRename(ctx, ordinaryPool, workspaceID, ordinaryID, oldTitle, title)
		waitEvent := awaitReplicaLockWait(t, ctx, pool, ordinaryName)
		if err := managedTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		result := awaitOrdinaryAutopilotWrite(t, renameResult)
		if result.err != nil || !result.conflict {
			t.Fatalf("ordinary rename result=%+v, want managed-title conflict", result)
		}
		assertAutopilotExactTitle(t, ctx, pool, ordinaryID, oldTitle)
		assertManagedAutomationTitle(t, ctx, pool, workspaceID, title)
		t.Logf("title_race_evidence case=managed_commit_ordinary_rename managed_winner=true ordinary_wait_event=%s old_title_preserved=true", waitEvent)
	})

	t.Run("ordinary_rename_away_allows_waiting_role_source", func(t *testing.T) {
		const automationName = "Ordinary rename away"
		title := automationTitle(automationTitleRaceRoleName, automationName)
		awayTitle := title + " moved"
		ordinaryID := insertOrdinaryAutopilot(t, ctx, pool, workspaceID, userID, baseAgentID, title)
		ordinaryPool, _ := newApplyReplicaPool(t, ctx, "away-ordinary-win")
		managedPool, managedName := newApplyReplicaPool(t, ctx, "away-managed-wait")
		ordinaryTx, err := ordinaryPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer ordinaryTx.Rollback(ctx) //nolint:errcheck
		if err := autopilotlock.LockTitles(ctx, ordinaryTx, pgUUID(workspaceID), title, awayTitle); err != nil {
			t.Fatal(err)
		}
		conflict, err := autopilotlock.HasTitleConflict(ctx, ordinaryTx, pgUUID(workspaceID), awayTitle, pgUUID(ordinaryID))
		if err != nil || conflict {
			t.Fatalf("rename-away preflight conflict=%t err=%v", conflict, err)
		}
		if _, err := ordinaryTx.Exec(ctx, `UPDATE autopilot SET title=$2,updated_at=now() WHERE id=$1`, ordinaryID, awayTitle); err != nil {
			t.Fatal(err)
		}

		managedTx, err := managedPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer managedTx.Rollback(ctx) //nolint:errcheck
		state := newAutomationTitleRaceState(t, managedTx, workspaceID, userID, baseAgentID, automationName)
		managedResult := runAutomationMaterialization(ctx, state)
		waitEvent := awaitReplicaLockWait(t, ctx, pool, managedName)
		if err := ordinaryTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := awaitAutomationMaterialization(t, managedResult); err != nil {
			t.Fatalf("waiting role source after rename-away: %v", err)
		}
		if err := managedTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertAutopilotExactTitle(t, ctx, pool, ordinaryID, awayTitle)
		assertManagedAutomationTitle(t, ctx, pool, workspaceID, title)
		t.Logf("title_race_evidence case=ordinary_rename_away managed_wait_event=%s managed_continued=true", waitEvent)
	})

	t.Run("ordinary_rollback_allows_waiting_role_source", func(t *testing.T) {
		const automationName = "Ordinary rollback"
		title := automationTitle(automationTitleRaceRoleName, automationName)
		ordinaryPool, _ := newApplyReplicaPool(t, ctx, "rollback-ordinary")
		managedPool, managedName := newApplyReplicaPool(t, ctx, "rollback-managed")
		ordinaryTx, err := ordinaryPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer ordinaryTx.Rollback(ctx) //nolint:errcheck
		if err := autopilotlock.LockTitles(ctx, ordinaryTx, pgUUID(workspaceID), title); err != nil {
			t.Fatal(err)
		}
		transientID := uuid.New()
		if _, err := ordinaryTx.Exec(ctx, `
INSERT INTO autopilot (id,workspace_id,title,assignee_id,created_by_type,created_by_id)
VALUES ($1,$2,$3,$4,'member',$5)
`, transientID, workspaceID, title, baseAgentID, userID); err != nil {
			t.Fatal(err)
		}

		managedTx, err := managedPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer managedTx.Rollback(ctx) //nolint:errcheck
		state := newAutomationTitleRaceState(t, managedTx, workspaceID, userID, baseAgentID, automationName)
		managedResult := runAutomationMaterialization(ctx, state)
		waitEvent := awaitReplicaLockWait(t, ctx, pool, managedName)
		if err := ordinaryTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := awaitAutomationMaterialization(t, managedResult); err != nil {
			t.Fatalf("waiting role source after ordinary rollback: %v", err)
		}
		if err := managedTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertManagedAutomationTitle(t, ctx, pool, workspaceID, title)
		var transientRows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM autopilot WHERE id=$1`, transientID).Scan(&transientRows); err != nil || transientRows != 0 {
			t.Fatalf("rolled-back ordinary row count=%d err=%v", transientRows, err)
		}
		t.Logf("title_race_evidence case=ordinary_rollback managed_wait_event=%s managed_continued=true transient_rows=0", waitEvent)
	})

	t.Run("reverse_order_multi_title_requests_do_not_deadlock", func(t *testing.T) {
		firstPool, _ := newApplyReplicaPool(t, ctx, "order-first")
		secondPool, secondName := newApplyReplicaPool(t, ctx, "order-second")
		firstTx, err := firstPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer firstTx.Rollback(ctx) //nolint:errcheck
		if err := autopilotlock.LockTitles(ctx, firstTx, pgUUID(workspaceID), "Title Z", "Title A"); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			secondTx, err := secondPool.Begin(ctx)
			if err == nil {
				defer secondTx.Rollback(ctx) //nolint:errcheck
				err = autopilotlock.LockTitles(ctx, secondTx, pgUUID(workspaceID), "Title A", "Title Z")
				if err == nil {
					err = secondTx.Commit(ctx)
				}
			}
			result <- err
		}()
		waitEvent := awaitReplicaLockWait(t, ctx, pool, secondName)
		if err := firstTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("reverse-order title lock: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("reverse-order title lock did not complete")
		}
		t.Logf("title_race_evidence case=reverse_order wait_event=%s deadlock=false", waitEvent)
	})
}

func newAutomationTitleRaceState(t *testing.T, tx pgx.Tx, workspaceID, userID, baseAgentID uuid.UUID, automationName string) *materializationState {
	t.Helper()
	manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{{
		ID: "title-race-role", DisplayName: automationTitleRaceRoleName,
		Instructions: testArtifact("roles/title-race/instructions.md"),
		Automations: []Automation{{
			ID: "title-race-automation", Name: automationName,
			Prompt: testArtifact("roles/title-race/automation.md"), Schedule: "0 9 * * *", Timezone: "UTC",
		}},
	}}}
	snapshot := planTestSnapshot(t, manifest)
	sourceID := uuid.New()
	plan, err := BuildPlan(sourceID.String(), nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	roleRef := ObjectRef{Kind: "role", ID: manifest.Roles[0].ID}
	return &materializationState{
		q: db.New(tx), tx: tx, workspaceID: pgUUID(workspaceID),
		source: db.RoleSource{ID: pgUUID(sourceID), RuntimeID: pgUUID(uuid.New())}, actorID: pgUUID(userID),
		snapshot: snapshot, plan: plan, actions: actionIndex(plan), receipt: &ApplyReceipt{},
		artifacts: map[string]verifiedArtifact{manifest.Roles[0].Automations[0].Prompt.Digest: {body: "managed automation prompt"}},
		mappings: map[string]db.RoleSourceObjectMapping{objectKey(roleRef): {
			SourceID: pgUUID(sourceID), WorkspaceID: pgUUID(workspaceID), SourceKind: "role", SourceObjectID: roleRef.ID,
			TargetKind: "agent", TargetID: pgUUID(baseAgentID),
		}},
		pendingMappings: map[string]pendingRoleSourceMapping{},
	}
}

func materializeAndFlushAutomation(t *testing.T, ctx context.Context, state *materializationState) {
	t.Helper()
	if err := state.materializeAutomations(ctx); err != nil {
		t.Fatalf("materialize automation: %v", err)
	}
	if err := state.flushMappings(ctx); err != nil {
		t.Fatalf("flush automation mapping: %v", err)
	}
}

func runAutomationMaterialization(ctx context.Context, state *materializationState) <-chan error {
	result := make(chan error, 1)
	go func() {
		if err := state.materializeAutomations(ctx); err != nil {
			result <- err
			return
		}
		result <- state.flushMappings(ctx)
	}()
	return result
}

func awaitAutomationMaterialization(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("automation materialization did not finish")
		return nil
	}
}

func assertAutomationTitleRaceConflict(t *testing.T, result <-chan error) {
	t.Helper()
	err := awaitAutomationMaterialization(t, result)
	if !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("automation title-race loser error=%v, want ErrApplyConflict", err)
	}
}

func runOrdinaryAutopilotCreate(ctx context.Context, pool *pgxpool.Pool, workspaceID, userID, assigneeID uuid.UUID, title string) <-chan ordinaryAutopilotWriteResult {
	result := make(chan ordinaryAutopilotWriteResult, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			result <- ordinaryAutopilotWriteResult{err: err}
			return
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err = autopilotlock.LockTitles(ctx, tx, pgUUID(workspaceID), title); err != nil {
			result <- ordinaryAutopilotWriteResult{err: err}
			return
		}
		conflict, err := autopilotlock.HasTitleConflict(ctx, tx, pgUUID(workspaceID), title, pgtype.UUID{})
		if err != nil || conflict {
			result <- ordinaryAutopilotWriteResult{conflict: conflict, err: err}
			return
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `
INSERT INTO autopilot (id,workspace_id,title,assignee_id,created_by_type,created_by_id)
VALUES ($1,$2,$3,$4,'member',$5)
`, id, workspaceID, title, assigneeID, userID)
		if err == nil {
			err = tx.Commit(ctx)
		}
		result <- ordinaryAutopilotWriteResult{id: id, err: err}
	}()
	return result
}

func runOrdinaryAutopilotRename(ctx context.Context, pool *pgxpool.Pool, workspaceID, autopilotID uuid.UUID, oldTitle, newTitle string) <-chan ordinaryAutopilotWriteResult {
	result := make(chan ordinaryAutopilotWriteResult, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			result <- ordinaryAutopilotWriteResult{err: err}
			return
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err = autopilotlock.LockTitles(ctx, tx, pgUUID(workspaceID), oldTitle, newTitle); err != nil {
			result <- ordinaryAutopilotWriteResult{err: err}
			return
		}
		conflict, err := autopilotlock.HasTitleConflict(ctx, tx, pgUUID(workspaceID), newTitle, pgUUID(autopilotID))
		if err != nil || conflict {
			result <- ordinaryAutopilotWriteResult{conflict: conflict, id: autopilotID, err: err}
			return
		}
		_, err = tx.Exec(ctx, `UPDATE autopilot SET title=$2,updated_at=now() WHERE id=$1 AND workspace_id=$3`, autopilotID, newTitle, workspaceID)
		if err == nil {
			err = tx.Commit(ctx)
		}
		result <- ordinaryAutopilotWriteResult{id: autopilotID, err: err}
	}()
	return result
}

func awaitOrdinaryAutopilotWrite(t *testing.T, result <-chan ordinaryAutopilotWriteResult) ordinaryAutopilotWriteResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("ordinary Autopilot write did not finish")
		return ordinaryAutopilotWriteResult{}
	}
}

func insertOrdinaryAutopilot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, userID, assigneeID uuid.UUID, title string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO autopilot (id,workspace_id,title,assignee_id,created_by_type,created_by_id)
VALUES ($1,$2,$3,$4,'member',$5)
`, id, workspaceID, title, assigneeID, userID); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertOrdinaryAutomationTitle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID, title string, ordinaryID uuid.UUID) {
	t.Helper()
	var rows, winners, mappings int
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE id=$3)
FROM autopilot WHERE workspace_id=$1 AND title=$2
`, workspaceID, title, ordinaryID).Scan(&rows, &winners); err != nil || rows != 1 || winners != 1 {
		t.Fatalf("ordinary title rows=%d winners=%d err=%v", rows, winners, err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM role_source_object_mapping mapping
JOIN autopilot target ON target.id=mapping.target_id AND target.workspace_id=mapping.workspace_id
WHERE mapping.workspace_id=$1 AND mapping.target_kind='autopilot'
  AND mapping.archived_at IS NULL AND target.title=$2
`, workspaceID, title).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("ordinary title active mappings=%d err=%v", mappings, err)
	}
}

func assertManagedAutomationTitle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID, title string) {
	t.Helper()
	var rows, managed int
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE EXISTS (
  SELECT 1 FROM role_source_object_mapping mapping
  WHERE mapping.workspace_id=target.workspace_id
    AND mapping.target_kind='autopilot'
    AND mapping.target_id=target.id
    AND mapping.archived_at IS NULL
))
FROM autopilot target
WHERE target.workspace_id=$1 AND target.status <> 'archived' AND target.title=$2
`, workspaceID, title).Scan(&rows, &managed); err != nil || rows != 1 || managed != 1 {
		t.Fatalf("managed title rows=%d mapped=%d err=%v", rows, managed, err)
	}
}

func assertAutopilotExactTitle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, autopilotID uuid.UUID, title string) {
	t.Helper()
	var current string
	if err := pool.QueryRow(ctx, `SELECT title FROM autopilot WHERE id=$1`, autopilotID).Scan(&current); err != nil || current != title {
		t.Fatalf("Autopilot %s title=%q want=%q err=%v", autopilotID, current, title, err)
	}
}
