package rolesource

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This opt-in test requires a disposable, migrated PostgreSQL 17 database.
// It proves that the generated adoption query runs against the real schema,
// resolves all three target kinds, preserves Autopilot ambiguity and holds a
// target row lock until the adoption transaction ends.
func TestRoleSourceAdoptionPostgresResolutionAndLocking(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_ADOPTION_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_ADOPTION_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := uuid.NewString()
	userID, workspaceID, foreignWorkspaceID := uuid.New(), uuid.New(), uuid.New()
	agentID, systemAgentID, skillID, foreignSkillID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	autopilotID, duplicateAutopilotID := uuid.New(), uuid.New()
	setupTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setupTx.Rollback(context.Background()) //nolint:errcheck
	if _, err := setupTx.Exec(ctx, `INSERT INTO "user" (id, name, email) VALUES ($1, 'Adoption tester', $2)`, userID, "adoption-"+suffix+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, 'Adoption workspace', $2)`, workspaceID, "adoption-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, 'Foreign adoption workspace', $2)`, foreignWorkspaceID, "foreign-adoption-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO agent (id, workspace_id, name, runtime_mode) VALUES ($1, $2, 'Adopt Writer', 'local')`, agentID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO agent (id, workspace_id, name, runtime_mode, kind) VALUES ($1, $2, 'Reserved Writer', 'local', 'system')`, systemAgentID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO skill (id, workspace_id, name) VALUES ($1, $2, 'Adopt Draft')`, skillID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO skill (id, workspace_id, name) VALUES ($1, $2, 'Adopt Draft')`, foreignSkillID, foreignWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `INSERT INTO autopilot (id, workspace_id, title, assignee_id, created_by_type, created_by_id) VALUES ($1, $2, 'Adopt Schedule', $3, 'member', $4)`, autopilotID, workspaceID, agentID, userID); err != nil {
		t.Fatal(err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupAdoptionPostgresFixture(t, pool, userID, workspaceID, foreignWorkspaceID) })

	queries := db.New(pool)
	requests := []adoptionTargetRequest{
		{TargetKind: "agent", Name: "Adopt Writer"},
		{TargetKind: "skill", Name: "Adopt Draft"},
		{TargetKind: "autopilot", Name: "Adopt Schedule"},
	}
	body, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: body, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("resolved %d targets, want 3: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if row.TargetKind == "autopilot" && util.UUIDToString(row.DependencyTargetID) != agentID.String() {
			t.Fatalf("autopilot dependency target=%s, want %s", util.UUIDToString(row.DependencyTargetID), agentID)
		}
	}
	ineligibleBody, _ := json.Marshal([]adoptionTargetRequest{{TargetKind: "agent", Name: "Reserved Writer"}})
	ineligible, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: ineligibleBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(ineligible) != 1 || !ineligible[0].AdoptionEligible.Valid || ineligible[0].AdoptionEligible.Bool {
		t.Fatalf("ineligible system agent=%+v err=%v", ineligible, err)
	}
	foreignBody, _ := json.Marshal([]adoptionTargetRequest{{TargetKind: "skill", Name: "Adopt Draft", TargetID: foreignSkillID.String()}})
	foreign, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: foreignBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-tenant target resolved: rows=%+v err=%v", foreign, err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	targetBody, err := json.Marshal([]adoptionTargetRequest{{TargetKind: "skill", Name: "Adopt Draft", TargetID: skillID.String()}})
	if err != nil {
		t.Fatal(err)
	}
	locked, err := db.New(tx).ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: targetBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(locked) != 1 || util.UUIDToString(locked[0].TargetID) != skillID.String() {
		t.Fatalf("exact locked target=%+v err=%v", locked, err)
	}
	before, err := adoptionVersionCommitment(locked[0].UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	expectAdoptionLockTimeout(t, ctx, pool, `UPDATE skill SET updated_at = clock_timestamp() WHERE id = $1`, skillID)
	expectAdoptionLockTimeout(t, ctx, pool, `DELETE FROM skill WHERE id = $1`, skillID)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE skill SET name = 'Adopt Draft Renamed', updated_at = clock_timestamp() + interval '1 second' WHERE id = $1`, skillID); err != nil {
		t.Fatal(err)
	}
	disappeared, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: targetBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(disappeared) != 0 {
		t.Fatalf("renamed target still resolved under approved name: rows=%+v err=%v", disappeared, err)
	}
	renamedBody, _ := json.Marshal([]adoptionTargetRequest{{TargetKind: "skill", Name: "Adopt Draft Renamed", TargetID: skillID.String()}})
	changed, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: renamedBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(changed) != 1 {
		t.Fatalf("changed target=%+v err=%v", changed, err)
	}
	after, err := adoptionVersionCommitment(changed[0].UpdatedAt)
	if err != nil || before == after {
		t.Fatalf("target mutation did not change version commitment: before=%q after=%q err=%v", before, after, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM skill WHERE id = $1`, skillID); err != nil {
		t.Fatal(err)
	}
	deleted, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: renamedBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(deleted) != 0 {
		t.Fatalf("deleted target still resolved: rows=%+v err=%v", deleted, err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO autopilot (id, workspace_id, title, assignee_id, created_by_type, created_by_id)
		VALUES ($1, $2, 'Adopt Schedule', $3, 'member', $4)
	`, duplicateAutopilotID, workspaceID, agentID, userID); err != nil {
		t.Fatal(err)
	}
	autopilotBody, _ := json.Marshal([]adoptionTargetRequest{{TargetKind: "autopilot", Name: "Adopt Schedule"}})
	ambiguous, err := queries.ListRoleSourceAdoptionTargetsForUpdate(ctx, db.ListRoleSourceAdoptionTargetsForUpdateParams{
		Targets: autopilotBody, WorkspaceID: util.MustParseUUID(workspaceID.String()),
	})
	if err != nil || len(ambiguous) != 2 {
		t.Fatalf("ambiguous targets=%+v err=%v", ambiguous, err)
	}
}

func expectAdoptionLockTimeout(t *testing.T, ctx context.Context, pool *pgxpool.Pool, statement string, targetID uuid.UUID) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '250ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, statement, targetID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("concurrent target mutation did not hit PostgreSQL lock timeout: %v", err)
	}
}

func cleanupAdoptionPostgresFixture(t *testing.T, pool *pgxpool.Pool, userID, workspaceID, foreignWorkspaceID uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin adoption cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := db.New(tx).SetWorkspaceTeardownMode(ctx); err != nil {
		t.Errorf("set adoption teardown mode: %v", err)
		return
	}
	for _, cleanup := range []struct {
		name      string
		statement string
	}{
		{name: "autopilots", statement: `DELETE FROM autopilot WHERE workspace_id IN ($1, $2)`},
		{name: "skills", statement: `DELETE FROM skill WHERE workspace_id IN ($1, $2)`},
		{name: "agents", statement: `DELETE FROM agent WHERE workspace_id IN ($1, $2)`},
		{name: "workspaces", statement: `DELETE FROM workspace WHERE id IN ($1, $2)`},
	} {
		if _, err := tx.Exec(ctx, cleanup.statement, workspaceID, foreignWorkspaceID); err != nil {
			t.Errorf("delete adoption %s: %v", cleanup.name, err)
			return
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID); err != nil {
		t.Errorf("delete adoption actor: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit adoption cleanup: %v", err)
	}
}
