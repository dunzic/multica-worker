package rolesource

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	retentionScaleSources            = 100
	retentionScaleSnapshotsPerSource = 100
	retentionScaleTotalSnapshots     = retentionScaleSources * retentionScaleSnapshotsPerSource
	retentionScaleBatchSize          = 100
	retentionScaleArtifactBytes      = 4096
	retentionScaleP95Budget          = 2 * time.Second
	retentionScaleP99Budget          = 5 * time.Second
	retentionScaleTotalBudget        = 30 * time.Second
)

// This opt-in gate executes the exact generated production candidate query
// against 100 sources and 10,000 eligible immutable snapshots. It records an
// EXPLAIN ANALYZE/BUFFERS plan for the first batch, then drains the whole
// inventory through the public bounded batch API and enforces a deliberately
// conservative local SLO. Candidate-topology failover remains a separate gate.
func TestRoleSourceRetentionCandidateScalePostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_RETENTION_SCALE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_RETENTION_SCALE_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := seedRetentionScaleFixture(t, ctx, pool)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			cleanupRetentionScaleFixture(t, pool, fixture)
		}
	})

	control := newApplyFailureControl(t, pool, noArtifactReader{})
	previewStarted := time.Now()
	preview, err := control.GetRetentionPreview(ctx, fixture.workspaceID.String(), fixture.sourceID.String())
	previewDuration := time.Since(previewStarted)
	if err != nil {
		t.Fatal(err)
	}
	const expectedSourceBytes = retentionScaleSnapshotsPerSource * retentionScaleArtifactBytes
	if preview.EligibleCount != retentionScaleSnapshotsPerSource || preview.EstimatedBytes != expectedSourceBytes ||
		preview.UniquelyReclaimableBytes != expectedSourceBytes || preview.Truncated {
		t.Fatalf("retention scale preview count=%d referenced=%d unique=%d truncated=%t",
			preview.EligibleCount, preview.EstimatedBytes, preview.UniquelyReclaimableBytes, preview.Truncated)
	}
	if previewDuration > retentionScaleP95Budget {
		t.Fatalf("retention scale preview exceeded safety budget: %s", previewDuration)
	}

	query := generatedRetentionQueueSQL(t)
	started := time.Now()
	explain := explainRetentionQueue(t, ctx, pool, query)
	if explain.ExecutionTimeMS > float64((5*time.Second)/time.Millisecond) || explain.PlanningTimeMS > 1000 {
		t.Fatalf("retention first-batch plan exceeded safety budget: planning_ms=%.3f execution_ms=%.3f", explain.PlanningTimeMS, explain.ExecutionTimeMS)
	}
	if explain.NodeType != "ModifyTable" || explain.Operation != "Insert" {
		t.Fatalf("retention EXPLAIN did not execute the production INSERT: %s", explain.PlanJSON)
	}

	latencies := []time.Duration{time.Duration(explain.ExecutionTimeMS * float64(time.Millisecond))}
	totalQueued := retentionScaleBatchSize
	for totalQueued < retentionScaleTotalSnapshots {
		batchStarted := time.Now()
		rows, err := control.QueueRetentionCandidates(ctx, retentionScaleBatchSize)
		latencies = append(latencies, time.Since(batchStarted))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 || len(rows) > retentionScaleBatchSize {
			t.Fatalf("retention batch size=%d after queued=%d", len(rows), totalQueued)
		}
		totalQueued += len(rows)
	}
	totalDuration := time.Since(started)
	if totalQueued != retentionScaleTotalSnapshots {
		t.Fatalf("queued snapshots=%d want=%d", totalQueued, retentionScaleTotalSnapshots)
	}
	assertRetentionScaleCandidates(t, ctx, pool, fixture.workspaceID)
	p50, p95, p99 := retentionLatencyPercentiles(latencies)
	if totalDuration > retentionScaleTotalBudget || p95 > retentionScaleP95Budget || p99 > retentionScaleP99Budget {
		t.Fatalf("retention candidate SLO exceeded: total=%s p50=%s p95=%s p99=%s batches=%d", totalDuration, p50, p95, p99, len(latencies))
	}
	t.Logf("retention_scale_evidence sources=%d snapshots=%d artifacts=%d edges=%d batches=%d batch_size=%d preview=%s planning_ms=%.3f first_execution_ms=%.3f shared_hit_blocks=%d shared_read_blocks=%d wal_records=%d total=%s p50=%s p95=%s p99=%s",
		retentionScaleSources, retentionScaleTotalSnapshots, retentionScaleTotalSnapshots, retentionScaleTotalSnapshots,
		len(latencies), retentionScaleBatchSize, previewDuration,
		explain.PlanningTimeMS, explain.ExecutionTimeMS, explain.SharedHitBlocks, explain.SharedReadBlocks, explain.WALRecords,
		totalDuration, p50, p95, p99)

	cleanupRetentionScaleFixture(t, pool, fixture)
	cleaned = true
	assertRetentionScaleResidue(t, ctx, pool, fixture)
}

type retentionScaleFixture struct {
	userID      uuid.UUID
	workspaceID uuid.UUID
	runtimeID   uuid.UUID
	sourceID    uuid.UUID
}

type retentionExplainEvidence struct {
	PlanningTimeMS   float64
	ExecutionTimeMS  float64
	SharedHitBlocks  int64
	SharedReadBlocks int64
	WALRecords       int64
	NodeType         string
	Operation        string
	PlanJSON         string
}

func seedRetentionScaleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) retentionScaleFixture {
	t.Helper()
	fixture := retentionScaleFixture{userID: uuid.New(), workspaceID: uuid.New(), runtimeID: uuid.New()}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Retention scale actor',$2)`, fixture.userID, "retention-scale-"+uuid.NewString()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Retention scale',$2)`, fixture.workspaceID, "retention-scale-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO agent_runtime (id,workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata)
VALUES ($1,$2,$3,'Retention scale runtime','local','codex','online','scale','{}'::jsonb)
`, fixture.runtimeID, fixture.workspaceID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source (
  id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,
  config_redacted,policy,state,created_by,updated_by
)
SELECT gen_random_uuid(),$1,$2,'Retention scale source '||ordinal,
       'agentwaker_directory','0.1.0','scale','{"configured":true}'::jsonb,
       '{}'::jsonb,'active',$3,$3
FROM generate_series(1,$4::int) ordinal
`, fixture.workspaceID, fixture.runtimeID, fixture.userID, retentionScaleSources); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source_retention_policy (
  id,workspace_id,source_id,version,request_key_digest,enabled,
  minimum_age_days,keep_successful_snapshots,created_by
)
SELECT gen_random_uuid(),source.workspace_id,source.id,1,
       'sha256:'||encode(digest(convert_to(source.id::text,'UTF8'),'sha256'),'hex'),
       true,90,10,$2
FROM role_source source WHERE source.workspace_id=$1
`, fixture.workspaceID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source_snapshot (
  source_id,workspace_id,snapshot_digest,manifest_digest,kind,adapter_version,
  contract_version,manifest,diagnostics,source_evidence,reported_by_runtime_id,created_at
)
SELECT source.id,source.workspace_id,
       'sha256:'||encode(digest(convert_to(source.id::text||':'||ordinal,'UTF8'),'sha256'),'hex'),
       'sha256:'||encode(digest(convert_to('manifest:'||source.id::text||':'||ordinal,'UTF8'),'sha256'),'hex'),
       'agentwaker_directory','0.1.0','1.0',
       '{"contract_version":"1.0","roles":[],"capabilities":[]}'::jsonb,
       '[]'::jsonb,'{}'::jsonb,$2,
       now()-interval '120 days'-(ordinal*interval '1 second')
FROM role_source source
CROSS JOIN generate_series(1,$3::int) ordinal
WHERE source.workspace_id=$1
`, fixture.workspaceID, fixture.runtimeID, retentionScaleSnapshotsPerSource); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
WITH generated AS (
  SELECT source.workspace_id,source.id AS source_id,ordinal,
         encode(digest(convert_to('artifact:'||source.id::text||':'||ordinal,'UTF8'),'sha256'),'hex') AS artifact_hex
  FROM role_source source
  CROSS JOIN generate_series(1,$4::int) ordinal
  WHERE source.workspace_id=$1
)
INSERT INTO role_source_artifact (
  workspace_id,digest,size_bytes,storage_key,uploaded_by_runtime_id,
  first_source_id,first_scan_request_id
)
SELECT workspace_id,'sha256:'||artifact_hex,$3,
       'role-source-artifacts/'||workspace_id::text||'/'||artifact_hex,
       $2,source_id,gen_random_uuid()
FROM generated
`, fixture.workspaceID, fixture.runtimeID, retentionScaleArtifactBytes, retentionScaleSnapshotsPerSource); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source_snapshot_artifact (
  workspace_id,source_id,snapshot_digest,artifact_digest,size_bytes
)
SELECT source.workspace_id,source.id,
       'sha256:'||encode(digest(convert_to(source.id::text||':'||ordinal,'UTF8'),'sha256'),'hex'),
       'sha256:'||encode(digest(convert_to('artifact:'||source.id::text||':'||ordinal,'UTF8'),'sha256'),'hex'),
       $2
FROM role_source source
CROSS JOIN generate_series(1,$3::int) ordinal
WHERE source.workspace_id=$1
`, fixture.workspaceID, retentionScaleArtifactBytes, retentionScaleSnapshotsPerSource); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM role_source WHERE workspace_id=$1 ORDER BY id LIMIT 1`, fixture.workspaceID).Scan(&fixture.sourceID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func generatedRetentionQueueSQL(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../../pkg/db/generated/role_source.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "const queueEligibleRoleSourceRetentionCandidates = `"
	start := strings.Index(string(body), marker)
	if start < 0 {
		t.Fatal("generated retention candidate query not found")
	}
	start += len(marker)
	end := strings.Index(string(body[start:]), "`")
	if end < 0 {
		t.Fatal("generated retention candidate query is unterminated")
	}
	return string(body[start : start+end])
}

func explainRetentionQueue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) retentionExplainEvidence {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, WAL, FORMAT JSON) "+query,
		retentionInterval(RetentionPlanGracePeriod), int32(retentionScaleBatchSize)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode retention EXPLAIN: plans=%d err=%v", len(plans), err)
	}
	evidence := retentionExplainEvidence{PlanJSON: string(raw)}
	evidence.PlanningTimeMS, _ = plans[0]["Planning Time"].(float64)
	evidence.ExecutionTimeMS, _ = plans[0]["Execution Time"].(float64)
	root, _ := plans[0]["Plan"].(map[string]any)
	evidence.NodeType, _ = root["Node Type"].(string)
	evidence.Operation, _ = root["Operation"].(string)
	evidence.SharedHitBlocks = jsonPlanInt64(root["Shared Hit Blocks"])
	evidence.SharedReadBlocks = jsonPlanInt64(root["Shared Read Blocks"])
	evidence.WALRecords = jsonPlanInt64(root["WAL Records"])
	return evidence
}

func jsonPlanInt64(value any) int64 {
	number, _ := value.(float64)
	return int64(number)
}

func retentionLatencyPercentiles(values []time.Duration) (time.Duration, time.Duration, time.Duration) {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	percentile := func(numerator int) time.Duration {
		index := (len(ordered)*numerator + 99) / 100
		if index < 1 {
			index = 1
		}
		return ordered[index-1]
	}
	return percentile(50), percentile(95), percentile(99)
}

func assertRetentionScaleCandidates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID) {
	t.Helper()
	var total, distinct int
	var estimatedBytes int64
	if err := pool.QueryRow(ctx, `
SELECT count(*),count(DISTINCT (source_id,snapshot_digest)),COALESCE(sum(estimated_bytes),0)::bigint
FROM role_source_retention_candidate WHERE workspace_id=$1
`, workspaceID).Scan(&total, &distinct, &estimatedBytes); err != nil {
		t.Fatal(err)
	}
	if total != retentionScaleTotalSnapshots || distinct != retentionScaleTotalSnapshots ||
		estimatedBytes != int64(retentionScaleTotalSnapshots*retentionScaleArtifactBytes) {
		t.Fatalf("retention candidates total=%d distinct=%d estimated_bytes=%d", total, distinct, estimatedBytes)
	}
}

func cleanupRetentionScaleFixture(t *testing.T, pool *pgxpool.Pool, fixture retentionScaleFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin retention scale cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := db.New(tx)
	if err := q.SetWorkspaceTeardownMode(ctx); err != nil {
		t.Errorf("set retention scale teardown: %v", err)
		return
	}
	if err := q.DeleteWorkspaceRoleSources(ctx, pgUUID(fixture.workspaceID)); err != nil {
		t.Errorf("delete retention scale role sources: %v", err)
		return
	}
	storagePrefix := "role-source-artifacts/" + fixture.workspaceID.String() + "/%"
	if _, err := tx.Exec(ctx, `DELETE FROM role_source_artifact_delete_intent WHERE storage_key LIKE $1`, storagePrefix); err != nil {
		t.Errorf("delete retention scale artifact intents: %v", err)
		return
	}
	for _, deletion := range []struct {
		label     string
		statement string
		argument  any
	}{
		{label: "runtime", statement: `DELETE FROM agent_runtime WHERE id=$1`, argument: fixture.runtimeID},
		{label: "workspace", statement: `DELETE FROM workspace WHERE id=$1`, argument: fixture.workspaceID},
		{label: "user", statement: `DELETE FROM "user" WHERE id=$1`, argument: fixture.userID},
	} {
		if _, err := tx.Exec(ctx, deletion.statement, deletion.argument); err != nil {
			t.Errorf("delete retention scale %s: %v", deletion.label, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit retention scale cleanup: %v", err)
	}
}

func assertRetentionScaleResidue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture retentionScaleFixture) {
	t.Helper()
	checks := map[string]string{
		"sources":    `SELECT count(*) FROM role_source WHERE workspace_id=$1`,
		"snapshots":  `SELECT count(*) FROM role_source_snapshot WHERE workspace_id=$1`,
		"edges":      `SELECT count(*) FROM role_source_snapshot_artifact WHERE workspace_id=$1`,
		"artifacts":  `SELECT count(*) FROM role_source_artifact WHERE workspace_id=$1`,
		"candidates": `SELECT count(*) FROM role_source_retention_candidate WHERE workspace_id=$1`,
		"policies":   `SELECT count(*) FROM role_source_retention_policy WHERE workspace_id=$1`,
		"workspace":  `SELECT count(*) FROM workspace WHERE id=$1`,
	}
	for label, query := range checks {
		var count int
		if err := pool.QueryRow(ctx, query, fixture.workspaceID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retention scale residue %s=%d err=%v", label, count, err)
		}
	}
	var intentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_artifact_delete_intent WHERE storage_key LIKE $1`, "role-source-artifacts/"+fixture.workspaceID.String()+"/%").Scan(&intentCount); err != nil || intentCount != 0 {
		t.Fatalf("retention scale artifact intent residue=%d err=%v", intentCount, err)
	}
	var runtimeCount, userCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id=$1`, fixture.runtimeID).Scan(&runtimeCount); err != nil || runtimeCount != 0 {
		t.Fatalf("retention scale runtime residue=%d err=%v", runtimeCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE id=$1`, fixture.userID).Scan(&userCount); err != nil || userCount != 0 {
		t.Fatalf("retention scale user residue=%d err=%v", userCount, err)
	}
}
