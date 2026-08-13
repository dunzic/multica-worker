package rolesource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const liveScaleArtifactPrefix = "role-source-scale/"

// TestRoleSourceProductionScaleApplyPostgres is opt-in because it performs the
// full 1,000-role/10,000-skill apply with 11,000 distinct 8 KiB bodies. It is a
// candidate-build evidence gate, not an ordinary unit test.
func TestRoleSourceProductionScaleApplyPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_SCALE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_SCALE_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	started := time.Now()
	fixture := createProductionScaleFixture(t, ctx, pool)
	defer cleanupProductionScaleFixture(t, pool, fixture)
	fixtureDuration := time.Since(started)

	var databaseBytesBefore int64
	var walBefore string
	if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()), pg_current_wal_lsn()::text`).Scan(&databaseBytesBefore, &walBefore); err != nil {
		t.Fatal(err)
	}
	var memoryBefore runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	stopMemorySampler := samplePeakHeapAlloc(memoryBefore.HeapAlloc)
	var peakHeapAlloc uint64
	defer func() {
		if peakHeapAlloc == 0 {
			stopMemorySampler()
		}
	}()

	control := newApplyFailureControl(t, pool, scaleArtifactReader{prefix: fixture.artifactPrefix})
	applyStarted := time.Now()
	row, receipt, err := control.ApplyPlan(ctx, fixture.input)
	applyDuration := time.Since(applyStarted)
	peakHeapAlloc = stopMemorySampler()
	if err != nil {
		t.Fatalf("production-scale ApplyPlan: %v", err)
	}
	if row.Status != "succeeded" || receipt.Counts.Created != productionApplyRoleCount*(productionApplySkillsPerRole+1) ||
		len(receipt.Mappings) != productionApplyRoleCount*(productionApplySkillsPerRole+1) {
		t.Fatalf("production-scale receipt status=%s counts=%+v mappings=%d", row.Status, receipt.Counts, len(receipt.Mappings))
	}

	retryStarted := time.Now()
	retryRow, retryReceipt, err := newApplyFailureControl(t, pool, scaleArtifactReader{prefix: fixture.artifactPrefix}).ApplyPlan(ctx, fixture.input)
	retryDuration := time.Since(retryStarted)
	if err != nil || retryRow.ID != row.ID || retryReceipt.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("production-scale retry row=%+v receipt=%+v err=%v", retryRow, retryReceipt, err)
	}
	counts := productionScaleCounts{}
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM agent WHERE workspace_id=$1),
  (SELECT count(*) FROM skill WHERE workspace_id=$1),
  (SELECT count(*) FROM agent_skill association JOIN agent ON agent.id=association.agent_id WHERE agent.workspace_id=$1),
  (SELECT count(*) FROM role_source_object_mapping WHERE source_id=$2),
  (SELECT count(*) FROM role_source_apply WHERE source_id=$2 AND status='succeeded'),
  (SELECT count(*) FROM role_source_audit_event WHERE source_id=$2 AND event_type='apply_succeeded'),
  (SELECT count(*) FROM role_source_outbox WHERE source_id=$2)
`, fixture.workspaceID, fixture.sourceID).Scan(
		&counts.agents, &counts.skills, &counts.bindings, &counts.mappings,
		&counts.applies, &counts.audits, &counts.outbox,
	); err != nil {
		t.Fatal(err)
	}
	if counts != (productionScaleCounts{
		agents: productionApplyRoleCount, skills: productionApplyRoleCount * productionApplySkillsPerRole,
		bindings: productionApplyRoleCount * productionApplySkillsPerRole,
		mappings: productionApplyRoleCount * (productionApplySkillsPerRole + 1), applies: 1, audits: 1, outbox: 1,
	}) {
		t.Fatalf("production-scale persisted counts=%+v", counts)
	}

	var databaseBytesAfter, walBytes int64
	if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()), pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::bigint`, walBefore).Scan(&databaseBytesAfter, &walBytes); err != nil {
		t.Fatal(err)
	}
	var memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryAfter)
	t.Logf("scale_evidence roles=%d skills=%d artifacts=%d artifact_bytes=%d fixture=%s apply=%s idempotent_retry=%s db_growth_bytes=%d wal_bytes=%d heap_alloc_bytes=%d peak_heap_alloc_bytes=%d peak_heap_delta_bytes=%d total_alloc_delta_bytes=%d receipt_bytes=%d",
		productionApplyRoleCount, productionApplyRoleCount*productionApplySkillsPerRole,
		productionApplyRoleCount*(productionApplySkillsPerRole+1),
		productionApplyRoleCount*(productionApplySkillsPerRole+1)*int(productionApplyArtifactBytes),
		fixtureDuration, applyDuration, retryDuration, databaseBytesAfter-databaseBytesBefore, walBytes,
		memoryAfter.Alloc, peakHeapAlloc, peakHeapAlloc-memoryBefore.HeapAlloc,
		memoryAfter.TotalAlloc-memoryBefore.TotalAlloc, len(row.Receipt),
	)
}

func samplePeakHeapAlloc(initial uint64) func() uint64 {
	stop := make(chan struct{})
	done := make(chan uint64, 1)
	go func() {
		peak := initial
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > peak {
					peak = stats.HeapAlloc
				}
			case <-stop:
				done <- peak
				return
			}
		}
	}()
	return func() uint64 {
		close(stop)
		return <-done
	}
}

type productionScaleCounts struct {
	agents   int64
	skills   int64
	bindings int64
	mappings int64
	applies  int64
	audits   int64
	outbox   int64
}

type productionScaleFixture struct {
	workspaceID    uuid.UUID
	sourceID       uuid.UUID
	actorID        uuid.UUID
	artifactPrefix string
	input          ApplyPlanInput
}

func createProductionScaleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) productionScaleFixture {
	t.Helper()
	fixture := productionScaleFixture{
		workspaceID: uuid.New(), sourceID: uuid.New(), actorID: uuid.New(),
		artifactPrefix: liveScaleArtifactPrefix + uuid.NewString() + "/",
	}
	runtimeID, approvalID := uuid.New(), uuid.New()
	from := emptyApplySnapshot(t, "scale-from-"+uuid.NewString())
	to := applyFailureSnapshot(t, productionApplyManifest(), "scale-to-"+uuid.NewString())
	plan, err := BuildPlan(fixture.sourceID.String(), &from, to)
	if err != nil || !plan.Applyable || plan.Summary.Create != productionApplyRoleCount*(productionApplySkillsPerRole+1) {
		t.Fatalf("build production-scale plan summary=%+v err=%v", plan.Summary, err)
	}
	decisions := ApprovalDecisions{ContractVersion: PlanContractVersion, Archives: []ArchiveActionDecision{}, Adoptions: []AdoptionActionDecision{}}
	if err := ValidateApprovalDecisions(plan, "approved", &decisions); err != nil {
		t.Fatal(err)
	}
	manifestFrom, _ := json.Marshal(from.Manifest)
	diagnosticsFrom, _ := json.Marshal(from.Diagnostics)
	evidenceFrom, _ := json.Marshal(from.SourceEvidence)
	manifestTo, _ := json.Marshal(to.Manifest)
	diagnosticsTo, _ := json.Marshal(to.Diagnostics)
	evidenceTo, _ := json.Marshal(to.SourceEvidence)
	planBody, _ := json.Marshal(plan)
	decisionsBody, _ := json.Marshal(decisions)
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Role source scale actor',$2)`, fixture.actorID, "role-source-scale-"+uuid.NewString()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Role source scale',$2)`, fixture.workspaceID, "role-source-scale-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runtime (id,workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata) VALUES ($1,$2,$3,'Scale runtime','local','codex','online','', '{}'::jsonb)`, runtimeID, fixture.workspaceID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO role_source (id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,config_redacted,policy,state,current_snapshot_digest,created_by,updated_by) VALUES ($1,$2,$3,'Scale source','fake_source','1.0.0','local','{}'::jsonb,'{}'::jsonb,'active',$4,$5,$5)`, fixture.sourceID, fixture.workspaceID, runtimeID, from.SnapshotDigest, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	q := db.New(pool)
	for _, params := range []db.InsertRoleSourceSnapshotParams{
		{SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), SnapshotDigest: from.SnapshotDigest, ManifestDigest: from.ManifestDigest, Kind: string(from.Kind), AdapterVersion: from.AdapterVersion, ContractVersion: from.ContractVersion, Manifest: manifestFrom, Diagnostics: diagnosticsFrom, SourceEvidence: evidenceFrom, ReportedByRuntimeID: pgUUID(runtimeID)},
		{SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), SnapshotDigest: to.SnapshotDigest, ManifestDigest: to.ManifestDigest, Kind: string(to.Kind), AdapterVersion: to.AdapterVersion, ContractVersion: to.ContractVersion, Manifest: manifestTo, Diagnostics: diagnosticsTo, SourceEvidence: evidenceTo, ReportedByRuntimeID: pgUUID(runtimeID)},
	} {
		if _, err := q.InsertRoleSourceSnapshot(ctx, params); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := collectMaterializationArtifactRefs(to)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	artifactRows := make([][]any, len(refs))
	integrityRows := make([][]any, len(refs))
	for index, ref := range refs {
		storageKey := fixture.artifactPrefix + ref.Path
		artifactRows[index] = []any{fixture.workspaceID, ref.Digest, ref.SizeBytes, storageKey, runtimeID, fixture.sourceID, uuid.New(), createdAt}
		integrityRows[index] = []any{fixture.workspaceID, ref.Digest, storageKey, ref.SizeBytes, "healthy", "healthy", createdAt, createdAt, createdAt}
	}
	if inserted, err := pool.CopyFrom(ctx, pgx.Identifier{"role_source_artifact"}, []string{
		"workspace_id", "digest", "size_bytes", "storage_key", "uploaded_by_runtime_id", "first_source_id", "first_scan_request_id", "created_at",
	}, pgx.CopyFromRows(artifactRows)); err != nil || inserted != int64(len(refs)) {
		t.Fatalf("copy artifact ledger inserted=%d want=%d err=%v", inserted, len(refs), err)
	}
	if inserted, err := pool.CopyFrom(ctx, pgx.Identifier{"role_source_artifact_integrity"}, []string{
		"workspace_id", "artifact_digest", "storage_key", "size_bytes", "state", "last_outcome", "last_verified_at", "created_at", "updated_at",
	}, pgx.CopyFromRows(integrityRows)); err != nil || inserted != int64(len(refs)) {
		t.Fatalf("copy integrity ledger inserted=%d want=%d err=%v", inserted, len(refs), err)
	}
	if _, err := q.InsertRoleSourcePlan(ctx, db.InsertRoleSourcePlanParams{SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), PlanDigest: plan.PlanDigest, FromSnapshotDigest: pgtype.Text{String: from.SnapshotDigest, Valid: true}, ToSnapshotDigest: to.SnapshotDigest, Plan: planBody, CreatedBy: pgUUID(fixture.actorID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRoleSourcePlanApproval(ctx, db.InsertRoleSourcePlanApprovalParams{ID: pgUUID(approvalID), SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), PlanDigest: plan.PlanDigest, RequestKey: "approve-scale-" + uuid.NewString(), Decision: "approved", Decisions: decisionsBody, ActorUserID: pgUUID(fixture.actorID)}); err != nil {
		t.Fatal(err)
	}
	fixture.input = ApplyPlanInput{
		WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), PlanDigest: plan.PlanDigest,
		ApprovalID: approvalID.String(), RequestKey: "apply-scale-" + uuid.NewString(),
		ActorUserID: fixture.actorID.String(), SecretTransferIDs: map[string]string{},
	}
	return fixture
}

type scaleArtifactReader struct {
	prefix string
}

func (r scaleArtifactReader) GetReader(_ context.Context, storageKey string) (io.ReadCloser, error) {
	if !strings.HasPrefix(storageKey, r.prefix) {
		return nil, fmt.Errorf("unexpected scale artifact key")
	}
	path := strings.TrimPrefix(storageKey, r.prefix)
	return io.NopCloser(bytes.NewReader(productionApplyArtifactBody(path))), nil
}

func cleanupProductionScaleFixture(t *testing.T, pool *pgxpool.Pool, fixture productionScaleFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin scale cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := db.New(tx)
	if err := q.SetWorkspaceTeardownMode(ctx); err != nil {
		t.Errorf("set scale teardown mode: %v", err)
		return
	}
	if err := q.DeleteWorkspaceRoleSources(ctx, pgUUID(fixture.workspaceID)); err != nil {
		t.Errorf("delete scale role sources: %v", err)
		return
	}
	steps := []struct {
		name  string
		query string
		arg   any
	}{
		{name: "artifact delete intents", query: `DELETE FROM role_source_artifact_delete_intent WHERE storage_key LIKE $1`, arg: fixture.artifactPrefix + "%"},
		{name: "workspace", query: `DELETE FROM workspace WHERE id=$1`, arg: fixture.workspaceID},
		{name: "actor", query: `DELETE FROM "user" WHERE id=$1`, arg: fixture.actorID},
	}
	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.query, step.arg); err != nil {
			t.Errorf("delete scale %s: %v", step.name, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit scale cleanup: %v", err)
		return
	}
	var residue int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM workspace WHERE id=$1) +
  (SELECT count(*) FROM "user" WHERE id=$2) +
	  (SELECT count(*) FROM role_source WHERE id=$4) +
	  (SELECT count(*) FROM role_source_artifact WHERE storage_key LIKE $3) +
	  (SELECT count(*) FROM role_source_artifact_delete_intent WHERE storage_key LIKE $3)
`, fixture.workspaceID, fixture.actorID, fixture.artifactPrefix+"%", fixture.sourceID).Scan(&residue); err != nil || residue != 0 {
		t.Errorf("scale cleanup residue=%d err=%v", residue, err)
	}
}
