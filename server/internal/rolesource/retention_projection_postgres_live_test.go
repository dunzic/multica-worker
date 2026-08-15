package rolesource

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This opt-in gate proves that the retention preview counts an artifact once
// only when every workspace edge to that artifact belongs to the current
// eligible set. Shared eligible edges are reclaimable as a group; an edge from
// any retained snapshot or another source keeps the artifact out of the
// projection. It intentionally does not claim that object storage has already
// reclaimed these bytes.
func TestRoleSourceRetentionUniqueReclaimProjectionPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_RETENTION_RECLAIM_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_RETENTION_RECLAIM_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := seedRetentionProjectionFixture(t, ctx, pool)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			cleanupRetentionProjectionFixture(t, pool, fixture)
		}
	})

	control := newApplyFailureControl(t, pool, noArtifactReader{})
	preview, err := control.GetRetentionPreview(ctx, fixture.workspaceID.String(), fixture.sourceID.String())
	if err != nil {
		t.Fatal(err)
	}
	if preview.EligibleCount != 2 || preview.EstimatedBytes != 1200 || preview.UniquelyReclaimableBytes != 300 || preview.Truncated {
		t.Fatalf("retention projection count=%d referenced=%d unique=%d truncated=%t, want 2/1200/300/false",
			preview.EligibleCount, preview.EstimatedBytes, preview.UniquelyReclaimableBytes, preview.Truncated)
	}
	if len(preview.Candidates) != 2 {
		t.Fatalf("retention projection candidates=%d want=2", len(preview.Candidates))
	}
	var candidateBytes int64
	for _, candidate := range preview.Candidates {
		candidateBytes += candidate.EstimatedBytes
	}
	if candidateBytes != preview.EstimatedBytes {
		t.Fatalf("candidate referenced bytes=%d total=%d", candidateBytes, preview.EstimatedBytes)
	}

	candidate, err := control.QueueNextRetentionCandidate(ctx)
	if err != nil || candidate.SnapshotDigest != fixture.firstSnapshotDigest {
		t.Fatalf("first retention candidate digest=%q err=%v", candidate.SnapshotDigest, err)
	}
	leaseToken := pgUUID(uuid.New())
	claimed, err := db.New(pool).ClaimNextRoleSourceRetentionCandidate(ctx, db.ClaimNextRoleSourceRetentionCandidateParams{
		LeaseToken: leaseToken, LeaseDuration: retentionInterval(5 * time.Minute),
	})
	if err != nil || claimed.ID != candidate.ID {
		t.Fatalf("claim first retention candidate id=%v err=%v", claimed.ID, err)
	}
	if outcome, err := control.PruneRetentionCandidate(ctx, candidate.ID, leaseToken); err != nil || outcome != "pruned" {
		t.Fatalf("prune first retention candidate outcome=%q err=%v", outcome, err)
	}
	auditRow, err := db.New(pool).GetLatestRoleSourceAuditEvent(ctx, db.GetLatestRoleSourceAuditEventParams{
		SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID),
	})
	if err != nil {
		t.Fatal(err)
	}
	auditEvent, err := DecodePersistedAuditEvent(auditRow)
	if err != nil {
		t.Fatal(err)
	}
	if auditEvent.EventType != "snapshot_retention_pruned" || auditEvent.Payload.UniquelyReclaimableBytes != 100 {
		t.Fatalf("retention prune audit type=%q unique=%d", auditEvent.EventType, auditEvent.Payload.UniquelyReclaimableBytes)
	}
	remaining, err := control.GetRetentionPreview(ctx, fixture.workspaceID.String(), fixture.sourceID.String())
	if err != nil {
		t.Fatal(err)
	}
	if remaining.EligibleCount != 1 || remaining.EstimatedBytes != 600 || remaining.UniquelyReclaimableBytes != 200 {
		t.Fatalf("remaining retention projection count=%d referenced=%d unique=%d",
			remaining.EligibleCount, remaining.EstimatedBytes, remaining.UniquelyReclaimableBytes)
	}

	cleanupRetentionProjectionFixture(t, pool, fixture)
	cleaned = true
	assertRetentionProjectionResidue(t, ctx, pool, fixture)
}

func TestRoleSourceRetentionPruneSerializesArtifactPublicationPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_RETENTION_RECLAIM_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_RETENTION_RECLAIM_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	fixture := seedRetentionProjectionFixture(t, ctx, pool)
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			cleanupRetentionProjectionFixture(t, pool, fixture)
		}
	})

	control := newApplyFailureControl(t, pool, noArtifactReader{})
	candidate, err := control.QueueNextRetentionCandidate(ctx)
	if err != nil || candidate.SnapshotDigest != fixture.firstSnapshotDigest {
		t.Fatalf("first retention candidate digest=%q err=%v", candidate.SnapshotDigest, err)
	}
	leaseToken := pgUUID(uuid.New())
	claimed, err := db.New(pool).ClaimNextRoleSourceRetentionCandidate(ctx, db.ClaimNextRoleSourceRetentionCandidateParams{
		LeaseToken: leaseToken, LeaseDuration: retentionInterval(5 * time.Minute),
	})
	if err != nil || claimed.ID != candidate.ID {
		t.Fatalf("claim first retention candidate id=%v err=%v", claimed.ID, err)
	}

	publisher, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Rollback(context.Background()) //nolint:errcheck
	locked, err := db.New(publisher).ListRoleSourceArtifactsForSnapshotByDigests(ctx, db.ListRoleSourceArtifactsForSnapshotByDigestsParams{
		WorkspaceID: pgUUID(fixture.workspaceID), Digests: []string{fixture.uniqueArtifactDigest},
	})
	if err != nil || len(locked) != 1 {
		t.Fatalf("lock artifact for snapshot publication rows=%d err=%v", len(locked), err)
	}

	type pruneResult struct {
		outcome string
		err     error
	}
	result := make(chan pruneResult, 1)
	go func() {
		outcome, pruneErr := control.PruneRetentionCandidate(ctx, candidate.ID, leaseToken)
		result <- pruneResult{outcome: outcome, err: pruneErr}
	}()
	select {
	case early := <-result:
		t.Fatalf("prune bypassed snapshot-publication artifact lock: outcome=%q err=%v", early.outcome, early.err)
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := publisher.Exec(ctx, `
INSERT INTO role_source_snapshot_artifact (
  workspace_id,source_id,snapshot_digest,artifact_digest,size_bytes
) VALUES ($1,$2,$3,$4,100)
`, fixture.workspaceID, fixture.sourceID, fixture.currentSnapshotDigest, fixture.uniqueArtifactDigest); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-result:
		if completed.err != nil || completed.outcome != "pruned" {
			t.Fatalf("serialized prune outcome=%q err=%v", completed.outcome, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serialized prune did not complete after snapshot publication committed")
	}

	auditRow, err := db.New(pool).GetLatestRoleSourceAuditEvent(ctx, db.GetLatestRoleSourceAuditEventParams{
		SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID),
	})
	if err != nil {
		t.Fatal(err)
	}
	auditEvent, err := DecodePersistedAuditEvent(auditRow)
	if err != nil {
		t.Fatal(err)
	}
	if auditEvent.EventType != "snapshot_retention_pruned" || auditEvent.Payload.UniquelyReclaimableBytes != 0 {
		t.Fatalf("serialized prune audit type=%q unique=%d", auditEvent.EventType, auditEvent.Payload.UniquelyReclaimableBytes)
	}

	cleanupRetentionProjectionFixture(t, pool, fixture)
	cleaned = true
	assertRetentionProjectionResidue(t, ctx, pool, fixture)
}

type retentionProjectionFixture struct {
	userID                uuid.UUID
	workspaceID           uuid.UUID
	runtimeID             uuid.UUID
	sourceID              uuid.UUID
	firstSnapshotDigest   string
	currentSnapshotDigest string
	uniqueArtifactDigest  string
	storageKeys           []string
}

func seedRetentionProjectionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) retentionProjectionFixture {
	t.Helper()
	fixture := retentionProjectionFixture{
		userID: uuid.New(), workspaceID: uuid.New(), runtimeID: uuid.New(), sourceID: uuid.New(),
	}
	otherSourceID := uuid.New()
	snapshots := []struct {
		sourceID uuid.UUID
		digest   string
		age      string
	}{
		{fixture.sourceID, "sha256:" + repeatHex("1"), "120 days"},
		{fixture.sourceID, "sha256:" + repeatHex("2"), "119 days"},
		{fixture.sourceID, "sha256:" + repeatHex("3"), "1 day"},
		{otherSourceID, "sha256:" + repeatHex("4"), "1 day"},
	}
	fixture.firstSnapshotDigest = snapshots[0].digest
	fixture.currentSnapshotDigest = snapshots[2].digest
	artifacts := []struct {
		digest string
		size   int64
	}{
		{"sha256:" + repeatHex("a"), 100},
		{"sha256:" + repeatHex("b"), 200},
		{"sha256:" + repeatHex("c"), 300},
		{"sha256:" + repeatHex("d"), 400},
	}
	fixture.uniqueArtifactDigest = artifacts[0].digest
	for _, artifact := range artifacts {
		fixture.storageKeys = append(fixture.storageKeys, "role-source-artifacts/"+fixture.workspaceID.String()+"/"+artifact.digest[7:])
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Retention projection actor',$2)`, fixture.userID, "retention-projection-"+uuid.NewString()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Retention projection',$2)`, fixture.workspaceID, "retention-projection-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO agent_runtime (id,workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata)
VALUES ($1,$2,$3,'Retention projection runtime','local','codex','online','projection','{}'::jsonb)
`, fixture.runtimeID, fixture.workspaceID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source (
  id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,
  config_redacted,policy,state,created_by,updated_by
) VALUES
  ($1,$3,$4,'Retention projection source','agentwaker_directory','0.1.0','projection','{"configured":true}'::jsonb,'{}'::jsonb,'active',$5,$5),
  ($2,$3,$4,'Retained sharing source','agentwaker_directory','0.1.0','projection-other','{"configured":true}'::jsonb,'{}'::jsonb,'active',$5,$5)
`, fixture.sourceID, otherSourceID, fixture.workspaceID, fixture.runtimeID, fixture.userID); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if _, err := tx.Exec(ctx, `
INSERT INTO role_source_snapshot (
  source_id,workspace_id,snapshot_digest,manifest_digest,kind,adapter_version,
  contract_version,manifest,diagnostics,source_evidence,reported_by_runtime_id,created_at
) VALUES ($1,$2,$3,$3,'agentwaker_directory','0.1.0','1.0',
  '{"contract_version":"1.0","roles":[],"capabilities":[]}'::jsonb,
  '[]'::jsonb,'{}'::jsonb,$4,now()-$5::interval)
`, snapshot.sourceID, fixture.workspaceID, snapshot.digest, fixture.runtimeID, snapshot.age); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE role_source SET current_snapshot_digest=$2 WHERE id=$1`, fixture.sourceID, snapshots[2].digest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE role_source SET current_snapshot_digest=$2 WHERE id=$1`, otherSourceID, snapshots[3].digest); err != nil {
		t.Fatal(err)
	}
	for index, artifact := range artifacts {
		if _, err := tx.Exec(ctx, `
INSERT INTO role_source_artifact (
  workspace_id,digest,size_bytes,storage_key,uploaded_by_runtime_id,first_source_id,first_scan_request_id
) VALUES ($1,$2,$3,$4,$5,$6,$7)
`, fixture.workspaceID, artifact.digest, artifact.size, fixture.storageKeys[index], fixture.runtimeID, fixture.sourceID, uuid.New()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO role_source_artifact_integrity (
  workspace_id,artifact_digest,storage_key,size_bytes,state,next_check_at
) VALUES ($1,$2,$3,$4,'healthy',now())
`, fixture.workspaceID, artifact.digest, fixture.storageKeys[index], artifact.size); err != nil {
			t.Fatal(err)
		}
	}
	edges := []struct {
		sourceID      uuid.UUID
		snapshotIndex int
		artifactIndex int
	}{
		{fixture.sourceID, 0, 0},
		{fixture.sourceID, 0, 1},
		{fixture.sourceID, 1, 1},
		{fixture.sourceID, 0, 2},
		{fixture.sourceID, 2, 2},
		{fixture.sourceID, 1, 3},
		{otherSourceID, 3, 3},
	}
	for _, edge := range edges {
		artifact := artifacts[edge.artifactIndex]
		if _, err := tx.Exec(ctx, `
INSERT INTO role_source_snapshot_artifact (
  workspace_id,source_id,snapshot_digest,artifact_digest,size_bytes
) VALUES ($1,$2,$3,$4,$5)
`, fixture.workspaceID, edge.sourceID, snapshots[edge.snapshotIndex].digest, artifact.digest, artifact.size); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO role_source_retention_policy (
  id,workspace_id,source_id,version,request_key_digest,enabled,
  minimum_age_days,keep_successful_snapshots,created_by
) VALUES ($1,$2,$3,1,$4,true,90,10,$5)
`, uuid.New(), fixture.workspaceID, fixture.sourceID, "sha256:"+repeatHex("e"), fixture.userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupRetentionProjectionFixture(t *testing.T, pool *pgxpool.Pool, fixture retentionProjectionFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin retention projection cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := db.New(tx)
	if err := q.SetWorkspaceTeardownMode(ctx); err != nil {
		t.Errorf("set retention projection teardown: %v", err)
		return
	}
	if err := q.DeleteWorkspaceRoleSources(ctx, pgUUID(fixture.workspaceID)); err != nil {
		t.Errorf("delete retention projection role sources: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_source_artifact_delete_intent WHERE storage_key=ANY($1::text[])`, fixture.storageKeys); err != nil {
		t.Errorf("delete retention projection intents: %v", err)
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
			t.Errorf("delete retention projection %s: %v", deletion.label, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit retention projection cleanup: %v", err)
	}
}

func assertRetentionProjectionResidue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture retentionProjectionFixture) {
	t.Helper()
	checks := []struct {
		label string
		query string
		arg   any
	}{
		{label: "sources", query: `SELECT count(*) FROM role_source WHERE workspace_id=$1`, arg: fixture.workspaceID},
		{label: "snapshots", query: `SELECT count(*) FROM role_source_snapshot WHERE workspace_id=$1`, arg: fixture.workspaceID},
		{label: "edges", query: `SELECT count(*) FROM role_source_snapshot_artifact WHERE workspace_id=$1`, arg: fixture.workspaceID},
		{label: "artifacts", query: `SELECT count(*) FROM role_source_artifact WHERE workspace_id=$1`, arg: fixture.workspaceID},
		{label: "integrity", query: `SELECT count(*) FROM role_source_artifact_integrity WHERE workspace_id=$1`, arg: fixture.workspaceID},
		{label: "intents", query: `SELECT count(*) FROM role_source_artifact_delete_intent WHERE storage_key=ANY($1::text[])`, arg: fixture.storageKeys},
		{label: "workspace", query: `SELECT count(*) FROM workspace WHERE id=$1`, arg: fixture.workspaceID},
		{label: "runtime", query: `SELECT count(*) FROM agent_runtime WHERE id=$1`, arg: fixture.runtimeID},
		{label: "user", query: `SELECT count(*) FROM "user" WHERE id=$1`, arg: fixture.userID},
	}
	for _, check := range checks {
		var count int
		if err := pool.QueryRow(ctx, check.query, check.arg).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retention projection residue %s=%d err=%v", check.label, count, err)
		}
	}
}

func repeatHex(value string) string {
	result := ""
	for range 64 {
		result += value
	}
	return result
}
