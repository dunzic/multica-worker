package rolesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type concurrentApplyResult struct {
	row     db.RoleSourceApply
	receipt ApplyReceipt
	err     error
}

func TestRoleSourceApplyConcurrencyPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_APPLY_CONCURRENCY_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_APPLY_CONCURRENCY_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	t.Run("same_request_across_control_planes_commits_once", func(t *testing.T) {
		fixture := createApplyFailureFixture(t, ctx, admin)
		defer cleanupApplyFailureFixture(t, admin, fixture)
		firstPool, firstName := newApplyReplicaPool(t, ctx, "duplicate-first")
		secondPool, secondName := newApplyReplicaPool(t, ctx, "duplicate-second")
		first := newApplyFailureControl(t, firstPool, fixture.artifactReader)
		second := newApplyFailureControl(t, secondPool, fixture.artifactReader)
		entered, release := holdApplyAfterStart(first)
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		firstResult := runConcurrentApply(ctx, first, fixture.input)
		awaitApplyHook(t, entered)
		secondResult := runConcurrentApply(ctx, second, fixture.input)
		waitEvent := awaitReplicaLockWait(t, ctx, admin, secondName)
		close(release)
		released = true
		left, right := awaitApplyResult(t, firstResult), awaitApplyResult(t, secondResult)
		if left.err != nil || right.err != nil || left.row.ID != right.row.ID ||
			left.receipt.ReceiptDigest == "" || left.receipt.ReceiptDigest != right.receipt.ReceiptDigest {
			t.Fatalf("duplicate results first=%+v second=%+v", left, right)
		}
		assertApplyCommittedOnce(t, ctx, admin, fixture, left.receipt.ReceiptDigest)
		t.Logf("concurrency_evidence case=exact_duplicate first_replica=%s second_replica=%s second_wait_event=%s committed_applies=1", firstName, secondName, waitEvent)
	})

	t.Run("competing_same_source_plan_loses_after_lock", func(t *testing.T) {
		fixture := createApplyFailureFixture(t, ctx, admin)
		defer cleanupApplyFailureFixture(t, admin, fixture)
		competingInput, competingDigest := createCompetingApplyInput(t, ctx, admin, fixture)
		firstPool, firstName := newApplyReplicaPool(t, ctx, "winner")
		secondPool, secondName := newApplyReplicaPool(t, ctx, "stale-loser")
		first := newApplyFailureControl(t, firstPool, fixture.artifactReader)
		second := newApplyFailureControl(t, secondPool, fixture.artifactReader)
		entered, release := holdApplyAfterStart(first)
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		firstResult := runConcurrentApply(ctx, first, fixture.input)
		awaitApplyHook(t, entered)
		secondResult := runConcurrentApply(ctx, second, competingInput)
		waitEvent := awaitReplicaLockWait(t, ctx, admin, secondName)
		close(release)
		released = true
		winner, loser := awaitApplyResult(t, firstResult), awaitApplyResult(t, secondResult)
		if winner.err != nil || winner.row.Status != "succeeded" || !errors.Is(loser.err, ErrApplyConflict) {
			t.Fatalf("competing results winner=%+v loser=%+v", winner, loser)
		}
		assertCompetingApplyOutcome(t, ctx, admin, fixture, competingDigest)
		assertFailureRequestDigest(t, ctx, admin, fixture.sourceID, competingInput.RequestKey)
		t.Logf("concurrency_evidence case=competing_plan winner_replica=%s loser_replica=%s loser_wait_event=%s winner_applies=1 loser_failure_audits=1", firstName, secondName, waitEvent)
	})

	t.Run("different_sources_in_one_workspace_progress_in_parallel", func(t *testing.T) {
		firstFixture := createApplyFailureFixture(t, ctx, admin)
		defer cleanupApplyFailureFixture(t, admin, firstFixture)
		secondFixture := createAdditionalApplySourceFixture(t, ctx, admin, firstFixture)
		firstPool, firstName := newApplyReplicaPool(t, ctx, "source-one")
		secondPool, secondName := newApplyReplicaPool(t, ctx, "source-two")
		first := newApplyFailureControl(t, firstPool, firstFixture.artifactReader)
		second := newApplyFailureControl(t, secondPool, secondFixture.artifactReader)
		entered, release := holdApplyAfterStart(first)
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		firstResult := runConcurrentApply(ctx, first, firstFixture.input)
		awaitApplyHook(t, entered)
		secondStarted := time.Now()
		secondResult := runConcurrentApply(ctx, second, secondFixture.input)
		var independent concurrentApplyResult
		select {
		case independent = <-secondResult:
		case <-time.After(5 * time.Second):
			close(release)
			released = true
			_ = awaitApplyResult(t, firstResult)
			t.Fatal("different-source apply did not progress while the first source lock was held")
		}
		parallelDuration := time.Since(secondStarted)
		close(release)
		released = true
		blocked := awaitApplyResult(t, firstResult)
		if independent.err != nil || blocked.err != nil || independent.row.Status != "succeeded" || blocked.row.Status != "succeeded" {
			t.Fatalf("parallel source results first=%+v second=%+v", blocked, independent)
		}
		assertParallelSourceOutcome(t, ctx, admin, firstFixture, secondFixture)
		t.Logf("concurrency_evidence case=different_sources first_replica=%s second_replica=%s second_completed_while_first_held=true second_duration=%s", firstName, secondName, parallelDuration)
	})
}

func newApplyReplicaPool(t *testing.T, ctx context.Context, label string) (*pgxpool.Pool, string) {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL truncates application_name to NAMEDATALEN-1 bytes. Keep the
	// identifier short so pg_stat_activity can match the exact replica while
	// it is blocked on the source/workspace transaction lock.
	name := "rs_" + label + "_" + uuid.NewString()[:8]
	config.ConnConfig.RuntimeParams["application_name"] = name
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, name
}

func holdApplyAfterStart(control *ControlPlane) (<-chan struct{}, chan<- struct{}) {
	entered := make(chan struct{})
	release := make(chan struct{})
	control.applyFailurePoint = func(point string) error {
		if point == applyFaultApplyStarted {
			close(entered)
			<-release
		}
		return nil
	}
	return entered, release
}

func runConcurrentApply(ctx context.Context, control *ControlPlane, input ApplyPlanInput) <-chan concurrentApplyResult {
	result := make(chan concurrentApplyResult, 1)
	go func() {
		row, receipt, err := control.ApplyPlan(ctx, input)
		result <- concurrentApplyResult{row: row, receipt: receipt, err: err}
	}()
	return result
}

func awaitApplyHook(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("apply did not reach the transaction hold point")
	}
}

func awaitApplyResult(t *testing.T, result <-chan concurrentApplyResult) concurrentApplyResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent apply did not finish")
		return concurrentApplyResult{}
	}
}

func awaitReplicaLockWait(t *testing.T, ctx context.Context, pool *pgxpool.Pool, applicationName string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waitEvent string
		err := pool.QueryRow(ctx, `
SELECT wait_event
FROM pg_stat_activity
WHERE datname=current_database()
  AND application_name=$1
  AND state='active'
  AND wait_event_type='Lock'
LIMIT 1
`, applicationName).Scan(&waitEvent)
		if err == nil {
			return waitEvent
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("replica %s did not enter a PostgreSQL lock wait", applicationName)
	return ""
}

func createCompetingApplyInput(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture) (ApplyPlanInput, string) {
	t.Helper()
	target := emptyApplySnapshot(t, "competing-"+uuid.NewString())
	input := persistApplyPlanFixture(t, ctx, pool, fixture, target, "competing")
	return input, target.SnapshotDigest
}

func createAdditionalApplySourceFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, parent applyFailureFixture) applyFailureFixture {
	t.Helper()
	fixture := applyFailureFixture{
		workspaceID: parent.workspaceID, sourceID: uuid.New(), actorID: parent.actorID, runtimeID: parent.runtimeID,
		artifactReader: noArtifactReader{},
	}
	from := emptyApplySnapshot(t, "parallel-from-"+uuid.NewString())
	to := emptyApplySnapshot(t, "parallel-to-"+uuid.NewString())
	fixture.fromSnapshot, fixture.toSnapshot = from, to
	fixture.fromDigest, fixture.toDigest = from.SnapshotDigest, to.SnapshotDigest
	if _, err := pool.Exec(ctx, `
INSERT INTO role_source (
  id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,
  config_redacted,policy,state,current_snapshot_digest,created_by,updated_by
) VALUES ($1,$2,$3,$4,'fake_source','1.0.0',$5,'{}'::jsonb,'{}'::jsonb,'active',$6,$7,$7)
`, fixture.sourceID, fixture.workspaceID, fixture.runtimeID, "Parallel source "+uuid.NewString(), "parallel-"+uuid.NewString(), from.SnapshotDigest, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(from.Manifest)
	diagnostics, _ := json.Marshal(from.Diagnostics)
	evidence, _ := json.Marshal(from.SourceEvidence)
	if _, err := db.New(pool).InsertRoleSourceSnapshot(ctx, db.InsertRoleSourceSnapshotParams{
		SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), SnapshotDigest: from.SnapshotDigest,
		ManifestDigest: from.ManifestDigest, Kind: string(from.Kind), AdapterVersion: from.AdapterVersion,
		ContractVersion: from.ContractVersion, Manifest: manifest, Diagnostics: diagnostics,
		SourceEvidence: evidence, ReportedByRuntimeID: pgUUID(fixture.runtimeID),
	}); err != nil {
		t.Fatal(err)
	}
	fixture.input = persistApplyPlanFixture(t, ctx, pool, fixture, to, "parallel")
	return fixture
}

func persistApplyPlanFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, target Snapshot, label string) ApplyPlanInput {
	t.Helper()
	plan, err := BuildPlan(fixture.sourceID.String(), &fixture.fromSnapshot, target)
	if err != nil || !plan.Applyable {
		t.Fatalf("build %s plan=%+v err=%v", label, plan, err)
	}
	decisions := ApprovalDecisions{ContractVersion: PlanContractVersion, Archives: []ArchiveActionDecision{}, Adoptions: []AdoptionActionDecision{}}
	if err := ValidateApprovalDecisions(plan, "approved", &decisions); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(target.Manifest)
	diagnostics, _ := json.Marshal(target.Diagnostics)
	evidence, _ := json.Marshal(target.SourceEvidence)
	planBody, _ := json.Marshal(plan)
	decisionsBody, _ := json.Marshal(decisions)
	approvalID := uuid.New()
	q := db.New(pool)
	if _, err := q.InsertRoleSourceSnapshot(ctx, db.InsertRoleSourceSnapshotParams{
		SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), SnapshotDigest: target.SnapshotDigest,
		ManifestDigest: target.ManifestDigest, Kind: string(target.Kind), AdapterVersion: target.AdapterVersion,
		ContractVersion: target.ContractVersion, Manifest: manifest, Diagnostics: diagnostics,
		SourceEvidence: evidence, ReportedByRuntimeID: pgUUID(fixture.runtimeID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRoleSourcePlan(ctx, db.InsertRoleSourcePlanParams{
		SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), PlanDigest: plan.PlanDigest,
		FromSnapshotDigest: pgtype.Text{String: fixture.fromSnapshot.SnapshotDigest, Valid: true},
		ToSnapshotDigest:   target.SnapshotDigest, Plan: planBody, CreatedBy: pgUUID(fixture.actorID),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRoleSourcePlanApproval(ctx, db.InsertRoleSourcePlanApprovalParams{
		ID: pgUUID(approvalID), SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID),
		PlanDigest: plan.PlanDigest, RequestKey: "approve-" + label + "-" + uuid.NewString(), Decision: "approved",
		Decisions: decisionsBody, ActorUserID: pgUUID(fixture.actorID),
	}); err != nil {
		t.Fatal(err)
	}
	return ApplyPlanInput{
		WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), PlanDigest: plan.PlanDigest,
		ApprovalID: approvalID.String(), RequestKey: "apply-" + label + "-" + uuid.NewString(),
		ActorUserID: fixture.actorID.String(), SecretTransferIDs: map[string]string{},
	}
}

func assertCompetingApplyOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, competingDigest string) {
	t.Helper()
	var current string
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, fixture.sourceID).Scan(&current); err != nil || current != fixture.toDigest || current == competingDigest {
		t.Fatalf("competing source snapshot=%s winner=%s loser=%s err=%v", current, fixture.toDigest, competingDigest, err)
	}
	var applies, audits, outbox, failures int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM role_source_apply WHERE source_id=$1 AND status='succeeded'),
  (SELECT count(*) FROM role_source_audit_event WHERE source_id=$1 AND event_type='apply_succeeded'),
  (SELECT count(*) FROM role_source_outbox WHERE source_id=$1),
  (SELECT count(*) FROM role_source_apply_failure WHERE source_id=$1 AND failure_code='state_conflict')
`, fixture.sourceID).Scan(&applies, &audits, &outbox, &failures); err != nil || applies != 1 || audits != 1 || outbox != 1 || failures != 1 {
		t.Fatalf("competing evidence applies=%d audits=%d outbox=%d failures=%d err=%v", applies, audits, outbox, failures, err)
	}
}

func assertParallelSourceOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, first, second applyFailureFixture) {
	t.Helper()
	var firstCurrent, secondCurrent string
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, first.sourceID).Scan(&firstCurrent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, second.sourceID).Scan(&secondCurrent); err != nil {
		t.Fatal(err)
	}
	if firstCurrent != first.toDigest || secondCurrent != second.toDigest {
		t.Fatalf("parallel snapshots first=%s second=%s", firstCurrent, secondCurrent)
	}
	var applies, audits, outbox, failures int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM role_source_apply WHERE workspace_id=$1 AND status='succeeded'),
  (SELECT count(*) FROM role_source_audit_event WHERE workspace_id=$1 AND event_type='apply_succeeded'),
  (SELECT count(*) FROM role_source_outbox WHERE workspace_id=$1),
  (SELECT count(*) FROM role_source_apply_failure WHERE workspace_id=$1)
`, first.workspaceID).Scan(&applies, &audits, &outbox, &failures); err != nil || applies != 2 || audits != 2 || outbox != 2 || failures != 0 {
		t.Fatalf("parallel evidence applies=%d audits=%d outbox=%d failures=%d err=%v", applies, audits, outbox, failures, err)
	}
}

func requestKeyDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func assertFailureRequestDigest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sourceID uuid.UUID, requestKey string) {
	t.Helper()
	var digest string
	if err := pool.QueryRow(ctx, `SELECT request_key_digest FROM role_source_apply_failure WHERE source_id=$1 ORDER BY occurred_at DESC LIMIT 1`, sourceID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != requestKeyDigest(requestKey) {
		t.Fatalf("failure request digest=%s", digest)
	}
}
