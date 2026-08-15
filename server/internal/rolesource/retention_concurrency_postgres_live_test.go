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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This opt-in gate proves the three destructive-retention races against a fully
// migrated PostgreSQL database. It deliberately queues both contenders behind
// a real row lock, then releases them in each order. The result must be
// linearizable: a legal hold or task pin that wins protects the snapshot; a
// prune that wins removes the snapshot and the later protector fails closed.
func TestRoleSourceRetentionProtectionRacesPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_RETENTION_RACE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_RETENTION_RACE_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	t.Run("legal_hold_commits_before_prune", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSource(t, ctx, admin, fixture)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		holdPool, holdReplica := newApplyReplicaPool(t, ctx, "hold_first")
		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "prune_second")
		holdControl := newApplyFailureControl(t, holdPool, noArtifactReader{})
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		holdResult := runRetentionLegalHold(ctx, holdControl, fixture, snapshotDigest, "hold-first")
		holdWait := awaitReplicaLockWait(t, ctx, admin, holdReplica)
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		hold := awaitRetentionLegalHold(t, holdResult)
		pruned := awaitRetentionPrune(t, pruneResult)
		if hold.err != nil || !hold.hold.Active() {
			t.Fatalf("legal-hold winner=%+v", hold)
		}
		if pruned.err != nil || pruned.outcome != "legal_hold" {
			t.Fatalf("prune loser=%+v", pruned)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "pending", "legal_hold", 1, 0)
		t.Logf("retention_race_evidence case=legal_hold_first hold_wait=%s prune_wait=%s snapshot_retained=true prune_outcome=legal_hold", holdWait, pruneWait)
	})

	t.Run("prune_commits_before_legal_hold", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSource(t, ctx, admin, fixture)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "prune_first")
		holdPool, holdReplica := newApplyReplicaPool(t, ctx, "hold_second")
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		holdControl := newApplyFailureControl(t, holdPool, noArtifactReader{})
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		holdResult := runRetentionLegalHold(ctx, holdControl, fixture, snapshotDigest, "hold-second")
		holdWait := awaitReplicaLockWait(t, ctx, admin, holdReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		pruned := awaitRetentionPrune(t, pruneResult)
		hold := awaitRetentionLegalHold(t, holdResult)
		if pruned.err != nil || pruned.outcome != "pruned" {
			t.Fatalf("prune winner=%+v", pruned)
		}
		if !errors.Is(hold.err, ErrInvalidLegalHold) {
			t.Fatalf("late legal hold did not fail closed: %+v", hold)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "completed", "pruned", 0, 0)
		t.Logf("retention_race_evidence case=prune_first prune_wait=%s hold_wait=%s snapshot_deleted=true late_hold=invalid_snapshot", pruneWait, holdWait)
	})

	t.Run("policy_revision_commits_before_prune", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSource(t, ctx, admin, fixture)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		policyPool, policyReplica := newApplyReplicaPool(t, ctx, "policy_first")
		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "policy_prune_second")
		policyControl := newApplyFailureControl(t, policyPool, noArtifactReader{})
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		policyResult := runRetentionPolicyDisable(ctx, policyControl, fixture, "policy-first")
		policyWait := awaitReplicaLockWait(t, ctx, admin, policyReplica)
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		policy := awaitRetentionPolicy(t, policyResult)
		pruned := awaitRetentionPrune(t, pruneResult)
		if policy.err != nil || policy.policy.Version != 2 || policy.policy.Enabled {
			t.Fatalf("policy winner=%+v", policy)
		}
		if pruned.err != nil || pruned.outcome != "policy_disabled" {
			t.Fatalf("prune loser=%+v", pruned)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "pending", "policy_disabled", 1, 0)
		t.Logf("retention_race_evidence case=policy_first policy_wait=%s prune_wait=%s snapshot_retained=true prune_outcome=policy_disabled", policyWait, pruneWait)
	})

	t.Run("prune_commits_before_policy_revision", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSource(t, ctx, admin, fixture)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "policy_prune_first")
		policyPool, policyReplica := newApplyReplicaPool(t, ctx, "policy_second")
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		policyControl := newApplyFailureControl(t, policyPool, noArtifactReader{})
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		policyResult := runRetentionPolicyDisable(ctx, policyControl, fixture, "policy-second")
		policyWait := awaitReplicaLockWait(t, ctx, admin, policyReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		pruned := awaitRetentionPrune(t, pruneResult)
		policy := awaitRetentionPolicy(t, policyResult)
		if pruned.err != nil || pruned.outcome != "pruned" {
			t.Fatalf("prune winner=%+v", pruned)
		}
		if policy.err != nil || policy.policy.Version != 2 || policy.policy.Enabled {
			t.Fatalf("late policy revision=%+v", policy)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "completed", "pruned", 0, 0)
		t.Logf("retention_race_evidence case=prune_before_policy prune_wait=%s policy_wait=%s snapshot_deleted=true late_policy_version=2", pruneWait, policyWait)
	})

	t.Run("task_pin_commits_before_prune", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSnapshot(t, ctx, admin, fixture, snapshotDigest)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		pinPool, pinReplica := newApplyReplicaPool(t, ctx, "pin_first")
		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "pin_prune_second")
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		pinResult := runRetentionTaskPin(ctx, pinPool, fixture, snapshotDigest)
		pinWait := awaitReplicaLockWait(t, ctx, admin, pinReplica)
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		if err := awaitRetentionTaskPin(t, pinResult); err != nil {
			t.Fatalf("task-pin winner: %v", err)
		}
		pruned := awaitRetentionPrune(t, pruneResult)
		if pruned.err != nil || pruned.outcome != "task_pin" {
			t.Fatalf("prune loser=%+v", pruned)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "pending", "task_pin", 1, 1)
		t.Logf("retention_race_evidence case=task_pin_first pin_wait=%s prune_wait=%s snapshot_retained=true prune_outcome=task_pin", pinWait, pruneWait)
	})

	t.Run("prune_commits_before_task_pin", func(t *testing.T) {
		fixture, snapshotDigest, candidateID, leaseToken := prepareRetentionRaceFixture(t, ctx, admin)
		blocker := lockRetentionSnapshot(t, ctx, admin, fixture, snapshotDigest)
		defer blocker.Rollback(context.Background()) //nolint:errcheck

		prunePool, pruneReplica := newApplyReplicaPool(t, ctx, "pin_prune_first")
		pinPool, pinReplica := newApplyReplicaPool(t, ctx, "pin_second")
		pruneControl := newApplyFailureControl(t, prunePool, noArtifactReader{})
		pruneResult := runRetentionPrune(ctx, pruneControl, candidateID, leaseToken)
		pruneWait := awaitReplicaLockWait(t, ctx, admin, pruneReplica)
		pinResult := runRetentionTaskPin(ctx, pinPool, fixture, snapshotDigest)
		pinWait := awaitReplicaLockWait(t, ctx, admin, pinReplica)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		pruned := awaitRetentionPrune(t, pruneResult)
		if pruned.err != nil || pruned.outcome != "pruned" {
			t.Fatalf("prune winner=%+v", pruned)
		}
		pinErr := awaitRetentionTaskPin(t, pinResult)
		var pgErr *pgconn.PgError
		if !errors.As(pinErr, &pgErr) || pgErr.Code != "23000" {
			t.Fatalf("late task pin error=%v, want PostgreSQL integrity violation", pinErr)
		}
		assertRetentionRaceState(t, ctx, admin, fixture.sourceID, snapshotDigest, candidateID, "completed", "pruned", 0, 0)
		t.Logf("retention_race_evidence case=prune_before_task_pin prune_wait=%s pin_wait=%s snapshot_deleted=true late_pin=integrity_violation", pruneWait, pinWait)
	})

	var orphanPins int
	if err := admin.QueryRow(ctx, `
SELECT count(*)
FROM role_source_task_pin pin
WHERE NOT EXISTS (
  SELECT 1 FROM role_source source
  WHERE source.workspace_id=pin.workspace_id AND source.id=pin.source_id
)
`).Scan(&orphanPins); err != nil {
		t.Fatal(err)
	}
	if orphanPins != 0 {
		t.Fatalf("orphan role-source task pins after retention races: %d", orphanPins)
	}
}

type retentionRaceLegalHoldResult struct {
	hold LegalHold
	err  error
}

type retentionRacePruneResult struct {
	outcome string
	err     error
}

type retentionRacePolicyResult struct {
	policy RetentionPolicy
	err    error
}

func prepareRetentionRaceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (applyFailureFixture, string, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	fixture := createApplyFailureFixture(t, ctx, pool)
	t.Cleanup(func() { cleanupRetentionRaceFixture(t, pool, fixture) })
	old := emptyApplySnapshot(t, "retention-race-"+uuid.NewString())
	manifest, _ := json.Marshal(old.Manifest)
	diagnostics, _ := json.Marshal(old.Diagnostics)
	evidence, _ := json.Marshal(old.SourceEvidence)
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source_snapshot (
  source_id,workspace_id,snapshot_digest,manifest_digest,kind,adapter_version,
  contract_version,manifest,diagnostics,source_evidence,reported_by_runtime_id,created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now()-interval '120 days')
`, fixture.sourceID, fixture.workspaceID, old.SnapshotDigest, old.ManifestDigest, string(old.Kind), old.AdapterVersion,
		old.ContractVersion, manifest, diagnostics, evidence, fixture.runtimeID); err != nil {
		t.Fatal(err)
	}
	control := newApplyFailureControl(t, pool, noArtifactReader{})
	if _, err := control.UpdateRetentionPolicy(ctx, UpdateRetentionPolicyInput{
		WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), ActorUserID: fixture.actorID.String(),
		RequestKey: "retention-race-policy-" + uuid.NewString(), ExpectedVersion: 0, Enabled: true,
		MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
	}); err != nil {
		t.Fatal(err)
	}
	candidate, err := control.QueueNextRetentionCandidate(ctx)
	if err != nil || candidate.SnapshotDigest != old.SnapshotDigest {
		t.Fatalf("queue race candidate=%+v err=%v", candidate, err)
	}
	leaseToken := pgUUID(uuid.New())
	claimed, err := db.New(pool).ClaimNextRoleSourceRetentionCandidate(ctx, db.ClaimNextRoleSourceRetentionCandidateParams{
		LeaseToken: leaseToken, LeaseDuration: pgtype.Interval{Microseconds: (5 * time.Minute).Microseconds(), Valid: true},
	})
	if err != nil || claimed.ID != candidate.ID {
		t.Fatalf("claim race candidate=%+v err=%v", claimed, err)
	}
	return fixture, old.SnapshotDigest, candidate.ID, leaseToken
}

func cleanupRetentionRaceFixture(t *testing.T, pool *pgxpool.Pool, fixture applyFailureFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source_legal_hold_release (
  hold_id,workspace_id,source_id,request_key_digest,reason_code,released_by
)
SELECT id,workspace_id,source_id,$2,'entered_in_error',$3
FROM role_source_legal_hold hold
WHERE hold.source_id=$1
  AND NOT EXISTS (SELECT 1 FROM role_source_legal_hold_release release WHERE release.hold_id=hold.id)
ON CONFLICT (hold_id) DO NOTHING
`, fixture.sourceID, testSHA256("0"), fixture.actorID); err != nil {
		t.Errorf("release retention-race legal hold: %v", err)
	}
	cleanupApplyFailureFixture(t, pool, fixture)
}

func lockRetentionSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM role_source WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, fixture.workspaceID, fixture.sourceID); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatal(err)
	}
	return tx
}

func lockRetentionSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, snapshotDigest string) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM role_source_snapshot WHERE workspace_id=$1 AND source_id=$2 AND snapshot_digest=$3 FOR UPDATE`, fixture.workspaceID, fixture.sourceID, snapshotDigest); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatal(err)
	}
	return tx
}

func runRetentionLegalHold(ctx context.Context, control *ControlPlane, fixture applyFailureFixture, snapshotDigest, requestSuffix string) <-chan retentionRaceLegalHoldResult {
	result := make(chan retentionRaceLegalHoldResult, 1)
	go func() {
		hold, err := control.CreateLegalHold(ctx, CreateLegalHoldInput{
			WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), ActorUserID: fixture.actorID.String(),
			RequestKey: "retention-race-" + requestSuffix, Scope: LegalHoldScopeSnapshot,
			SnapshotDigest: snapshotDigest, ReasonCode: LegalHoldReasonLitigation,
		})
		result <- retentionRaceLegalHoldResult{hold: hold, err: err}
	}()
	return result
}

func runRetentionPrune(ctx context.Context, control *ControlPlane, candidateID, leaseToken pgtype.UUID) <-chan retentionRacePruneResult {
	result := make(chan retentionRacePruneResult, 1)
	go func() {
		outcome, err := control.PruneRetentionCandidate(ctx, candidateID, leaseToken)
		result <- retentionRacePruneResult{outcome: outcome, err: err}
	}()
	return result
}

func runRetentionPolicyDisable(ctx context.Context, control *ControlPlane, fixture applyFailureFixture, requestSuffix string) <-chan retentionRacePolicyResult {
	result := make(chan retentionRacePolicyResult, 1)
	go func() {
		policy, err := control.UpdateRetentionPolicy(ctx, UpdateRetentionPolicyInput{
			WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), ActorUserID: fixture.actorID.String(),
			RequestKey: "retention-race-" + requestSuffix, ExpectedVersion: 1, Enabled: false,
			MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
		})
		result <- retentionRacePolicyResult{policy: policy, err: err}
	}()
	return result
}

func runRetentionTaskPin(ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, snapshotDigest string) <-chan error {
	result := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `
INSERT INTO role_source_task_pin (
  task_id,workspace_id,agent_id,source_id,source_role_id,snapshot_digest,
  role_object_digest,target_state_digest,capability_pins
) VALUES ($1,$2,$3,$4,'retention-race-role',$5,$6,$7,'[]'::jsonb)
`, uuid.New(), fixture.workspaceID, uuid.New(), fixture.sourceID, snapshotDigest, testSHA256("a"), testSHA256("b"))
		result <- err
	}()
	return result
}

func awaitRetentionLegalHold(t *testing.T, result <-chan retentionRaceLegalHoldResult) retentionRaceLegalHoldResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("legal-hold contender did not finish")
		return retentionRaceLegalHoldResult{}
	}
}

func awaitRetentionPrune(t *testing.T, result <-chan retentionRacePruneResult) retentionRacePruneResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("retention-prune contender did not finish")
		return retentionRacePruneResult{}
	}
}

func awaitRetentionPolicy(t *testing.T, result <-chan retentionRacePolicyResult) retentionRacePolicyResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("retention-policy contender did not finish")
		return retentionRacePolicyResult{}
	}
}

func awaitRetentionTaskPin(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("task-pin contender did not finish")
		return nil
	}
}

func assertRetentionRaceState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, snapshotDigest string, candidateID pgtype.UUID, wantState, wantResult string, wantSnapshots, wantPins int) {
	t.Helper()
	var state, result string
	var snapshots, pins int
	if err := pool.QueryRow(ctx, `SELECT state,result_code FROM role_source_retention_candidate WHERE id=$1`, candidateID).Scan(&state, &result); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_snapshot WHERE source_id=$1 AND snapshot_digest=$2`, sourceID, snapshotDigest).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_task_pin WHERE source_id=$1 AND snapshot_digest=$2`, sourceID, snapshotDigest).Scan(&pins); err != nil {
		t.Fatal(err)
	}
	if state != wantState || result != wantResult || snapshots != wantSnapshots || pins != wantPins {
		t.Fatalf("race state=%q result=%q snapshots=%d pins=%d; want %q/%q/%d/%d", state, result, snapshots, pins, wantState, wantResult, wantSnapshots, wantPins)
	}
}
