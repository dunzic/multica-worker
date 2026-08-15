package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errInjectedApplyFailure = errors.New("injected role-source apply failure")

func TestRoleSourceApplyFailureMatrixPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_APPLY_FAILURE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_APPLY_FAILURE_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	preCommit := []struct {
		point string
		stage string
	}{
		{applyFaultTransactionBegan, "transaction"},
		{applyFaultApplyStarted, "transaction"},
		{applyFaultBeforeMaterialize, "materialization"},
		{applyFaultAfterMaterialize, "materialization"},
		{applyFaultSecretsConsumed, "materialization"},
		{applyFaultSnapshotAdvanced, "finalize"},
		{applyFaultReceiptCompleted, "finalize"},
		{applyFaultAuditAppended, "finalize"},
		{applyFaultOutboxInserted, "finalize"},
	}
	for _, test := range preCommit {
		t.Run(test.point, func(t *testing.T) {
			fixture := createApplyFailureFixture(t, ctx, pool)
			defer cleanupApplyFailureFixture(t, pool, fixture)
			control := newApplyFailureControl(t, pool, fixture.artifactReader)
			control.applyFailurePoint = func(point string) error {
				if point == test.point {
					return errInjectedApplyFailure
				}
				return nil
			}
			if _, _, err := control.ApplyPlan(ctx, fixture.input); !errors.Is(err, errInjectedApplyFailure) {
				t.Fatalf("ApplyPlan error=%v", err)
			}
			assertApplyRolledBack(t, ctx, pool, fixture, test.stage)
		})
	}

	t.Run("materialized_agent_and_mapping_rollback", func(t *testing.T) {
		fixture := createMaterializingApplyFailureFixture(t, ctx, pool)
		defer cleanupApplyFailureFixture(t, pool, fixture)
		control := newApplyFailureControl(t, pool, fixture.artifactReader)
		control.applyFailurePoint = func(point string) error {
			if point == applyFaultAfterMaterialize {
				return errInjectedApplyFailure
			}
			return nil
		}
		if _, _, err := control.ApplyPlan(ctx, fixture.input); !errors.Is(err, errInjectedApplyFailure) {
			t.Fatalf("ApplyPlan error=%v", err)
		}
		assertApplyRolledBack(t, ctx, pool, fixture, "materialization")
		for table, args := range map[string]struct {
			condition string
			value     any
		}{
			"agent":                      {condition: "workspace_id=$1", value: fixture.workspaceID},
			"role_source_object_mapping": {condition: "source_id=$1", value: fixture.sourceID},
		} {
			var count int
			if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, args.condition), args.value).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s committed business rows=%d err=%v", table, count, err)
			}
		}
	})

	t.Run("ambiguous_driver_commit", func(t *testing.T) {
		fixture := createApplyFailureFixture(t, ctx, pool)
		defer cleanupApplyFailureFixture(t, pool, fixture)
		control := newApplyFailureControl(t, ambiguousCommitDB{Pool: pool}, fixture.artifactReader)
		row, receipt, err := control.ApplyPlan(ctx, fixture.input)
		if err != nil || row.Status != "succeeded" || receipt.ReceiptDigest == "" {
			t.Fatalf("commit reconciliation row=%+v receipt=%+v err=%v", row, receipt, err)
		}
		secondRow, secondReceipt, err := newApplyFailureControl(t, pool, fixture.artifactReader).ApplyPlan(ctx, fixture.input)
		if err != nil || secondRow.ID != row.ID || secondReceipt.ReceiptDigest != receipt.ReceiptDigest {
			t.Fatalf("idempotent restart row=%+v receipt=%+v err=%v", secondRow, secondReceipt, err)
		}
		assertApplyCommittedOnce(t, ctx, pool, fixture, receipt.ReceiptDigest)
	})

	t.Run("cancelled_response_after_commit", func(t *testing.T) {
		fixture := createApplyFailureFixture(t, ctx, pool)
		defer cleanupApplyFailureFixture(t, pool, fixture)
		requestCtx, cancelRequest := context.WithCancel(ctx)
		control := newApplyFailureControl(t, pool, fixture.artifactReader)
		control.applyFailurePoint = func(point string) error {
			if point == applyFaultCommitResponseLost {
				cancelRequest()
				return context.Canceled
			}
			return nil
		}
		row, receipt, err := control.ApplyPlan(requestCtx, fixture.input)
		if err != nil || row.Status != "succeeded" || receipt.ReceiptDigest == "" {
			t.Fatalf("cancelled commit reconciliation row=%+v receipt=%+v err=%v", row, receipt, err)
		}
		assertApplyCommittedOnce(t, ctx, pool, fixture, receipt.ReceiptDigest)
	})

	t.Run("process_restart_after_commit", func(t *testing.T) {
		fixture := createApplyFailureFixture(t, ctx, pool)
		defer cleanupApplyFailureFixture(t, pool, fixture)
		firstProcess := newApplyFailureControl(t, pool, fixture.artifactReader)
		firstProcess.applyFailurePoint = func(point string) error {
			if point == applyFaultCommitResponseLost {
				return errInjectedApplyFailure
			}
			return nil
		}
		tracker := applyAttemptTracker{}
		if _, _, err := firstProcess.applyPlan(ctx, fixture.input, &tracker); !errors.Is(err, errInjectedApplyFailure) {
			t.Fatalf("first process error=%v", err)
		}
		restartedProcess := newApplyFailureControl(t, pool, fixture.artifactReader)
		row, receipt, err := restartedProcess.ApplyPlan(ctx, fixture.input)
		if err != nil || row.Status != "succeeded" || receipt.ReceiptDigest == "" {
			t.Fatalf("restart recovery row=%+v receipt=%+v err=%v", row, receipt, err)
		}
		assertApplyCommittedOnce(t, ctx, pool, fixture, receipt.ReceiptDigest)
	})
}

type applyFailureFixture struct {
	workspaceID    uuid.UUID
	sourceID       uuid.UUID
	actorID        uuid.UUID
	runtimeID      uuid.UUID
	fromDigest     string
	toDigest       string
	fromSnapshot   Snapshot
	toSnapshot     Snapshot
	input          ApplyPlanInput
	artifactReader ArtifactReader
	artifactKeys   []string
}

func createApplyFailureFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) applyFailureFixture {
	t.Helper()
	return createApplyFailureFixtureForManifest(t, ctx, pool, Manifest{
		ContractVersion: ContractVersion, Roles: []Role{}, Capabilities: []Capability{},
	}, nil)
}

func createMaterializingApplyFailureFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) applyFailureFixture {
	t.Helper()
	body := []byte("# Apply failure injection\n\nCreate one transaction-scoped agent.\n")
	sum := sha256.Sum256(body)
	ref := ArtifactRef{
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Path: "roles/failure-writer/instructions.md",
		MediaType: "text/markdown", SizeBytes: int64(len(body)),
	}
	manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{{
		ID: "failure-writer", DisplayName: "Failure Writer " + uuid.NewString(), Instructions: ref,
		Skills: []Skill{}, CapabilityBindings: []CapabilityBinding{}, Environment: []EnvironmentKey{},
		MCP: []MCPServer{}, Automations: []Automation{},
	}}, Capabilities: []Capability{}}
	return createApplyFailureFixtureForManifest(t, ctx, pool, manifest, map[string][]byte{ref.Digest: body})
}

func createApplyFailureFixtureForManifest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, target Manifest, artifacts map[string][]byte) applyFailureFixture {
	t.Helper()
	fixture := applyFailureFixture{workspaceID: uuid.New(), sourceID: uuid.New(), actorID: uuid.New(), artifactReader: noArtifactReader{}}
	runtimeID, approvalID := uuid.New(), uuid.New()
	fixture.runtimeID = runtimeID
	from := emptyApplySnapshot(t, "from-"+uuid.NewString())
	to := applyFailureSnapshot(t, target, "to-"+uuid.NewString())
	fixture.fromSnapshot, fixture.toSnapshot = from, to
	plan, err := BuildPlan(fixture.sourceID.String(), &from, to)
	if err != nil || !plan.Applyable {
		t.Fatalf("build apply plan=%+v err=%v", plan, err)
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
	if _, err := pool.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Apply failure actor',$2)`, fixture.actorID, "apply-failure-"+uuid.NewString()+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Apply failure test',$2)`, fixture.workspaceID, "apply-failure-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_runtime (id,workspace_id,daemon_id,name,runtime_mode,provider,status,device_info,metadata) VALUES ($1,$2,$3,'Apply runtime','local','codex','online','', '{}'::jsonb)`, runtimeID, fixture.workspaceID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO role_source (id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,config_redacted,policy,state,current_snapshot_digest,created_by,updated_by) VALUES ($1,$2,$3,'Apply source','fake_source','1.0.0','local','{}'::jsonb,'{}'::jsonb,'active',$4,$5,$5)`, fixture.sourceID, fixture.workspaceID, runtimeID, from.SnapshotDigest, fixture.actorID); err != nil {
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
	if len(artifacts) > 0 {
		bodies := make(map[string][]byte, len(artifacts))
		for digest, body := range artifacts {
			storageKey := "apply-failure/" + fixture.workspaceID.String() + "/" + strings.TrimPrefix(digest, "sha256:")
			fixture.artifactKeys = append(fixture.artifactKeys, storageKey)
			bodies[storageKey] = append([]byte(nil), body...)
			if _, err := pool.Exec(ctx, `INSERT INTO role_source_artifact (workspace_id,digest,size_bytes,storage_key,uploaded_by_runtime_id,first_source_id,first_scan_request_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`, fixture.workspaceID, digest, len(body), storageKey, runtimeID, fixture.sourceID, uuid.New()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO role_source_artifact_integrity (workspace_id,artifact_digest,storage_key,size_bytes,state,last_outcome,last_verified_at) VALUES ($1,$2,$3,$4,'healthy','healthy',now())`, fixture.workspaceID, digest, storageKey, len(body)); err != nil {
				t.Fatal(err)
			}
		}
		fixture.artifactReader = mapArtifactReader(bodies)
	}
	if _, err := q.InsertRoleSourcePlan(ctx, db.InsertRoleSourcePlanParams{SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), PlanDigest: plan.PlanDigest, FromSnapshotDigest: pgtype.Text{String: from.SnapshotDigest, Valid: true}, ToSnapshotDigest: to.SnapshotDigest, Plan: planBody, CreatedBy: pgUUID(fixture.actorID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRoleSourcePlanApproval(ctx, db.InsertRoleSourcePlanApprovalParams{ID: pgUUID(approvalID), SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), PlanDigest: plan.PlanDigest, RequestKey: "approve-" + uuid.NewString(), Decision: "approved", Decisions: decisionsBody, ActorUserID: pgUUID(fixture.actorID)}); err != nil {
		t.Fatal(err)
	}
	fixture.fromDigest, fixture.toDigest = from.SnapshotDigest, to.SnapshotDigest
	fixture.input = ApplyPlanInput{WorkspaceID: fixture.workspaceID.String(), SourceID: fixture.sourceID.String(), PlanDigest: plan.PlanDigest, ApprovalID: approvalID.String(), RequestKey: "apply-" + uuid.NewString(), ActorUserID: fixture.actorID.String(), SecretTransferIDs: map[string]string{}}
	return fixture
}

func emptyApplySnapshot(t *testing.T, tree string) Snapshot {
	t.Helper()
	manifest := Manifest{ContractVersion: ContractVersion, Roles: []Role{}, Capabilities: []Capability{}}
	return applyFailureSnapshot(t, manifest, tree)
}

func applyFailureSnapshot(t *testing.T, manifest Manifest, tree string) Snapshot {
	t.Helper()
	if err := validateManifest(&manifest); err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := digestManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	treeSum := sha256.Sum256([]byte(tree))
	snapshot := Snapshot{Kind: "fake_source", AdapterVersion: "1.0.0", ContractVersion: ContractVersion, ManifestDigest: manifestDigest, Manifest: manifest, Diagnostics: []Diagnostic{}, SourceEvidence: SourceEvidence{TreeDigest: "sha256:" + hex.EncodeToString(treeSum[:])}}
	snapshot.SnapshotDigest, err = digestSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type noArtifactReader struct{}

func (noArtifactReader) GetReader(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("no-op apply requested an artifact")
}

type mapArtifactReader map[string][]byte

func (r mapArtifactReader) GetReader(_ context.Context, storageKey string) (io.ReadCloser, error) {
	body, ok := r[storageKey]
	if !ok {
		return nil, fmt.Errorf("missing apply failure artifact %q", storageKey)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

type ambiguousCommitDB struct {
	*pgxpool.Pool
}

func (d ambiguousCommitDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &ambiguousCommitTx{Tx: tx}, nil
}

type ambiguousCommitTx struct {
	pgx.Tx
}

func (tx *ambiguousCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

func newApplyFailureControl(t *testing.T, database controlPlaneDB, reader ArtifactReader) *ControlPlane {
	t.Helper()
	control, err := NewControlPlane(database, &Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	control.SetArtifactReader(reader)
	return control
}

func assertApplyRolledBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, expectedStage string) {
	t.Helper()
	var current string
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, fixture.sourceID).Scan(&current); err != nil || current != fixture.fromDigest {
		t.Fatalf("source current snapshot=%s err=%v", current, err)
	}
	for table, condition := range map[string]string{
		"role_source_apply":       "source_id=$1",
		"role_source_outbox":      "source_id=$1",
		"role_source_audit_event": "source_id=$1",
	} {
		var count int
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, condition), fixture.sourceID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s committed rows=%d err=%v", table, count, err)
		}
	}
	var failureCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_apply_failure WHERE source_id=$1`, fixture.sourceID).Scan(&failureCount); err != nil || failureCount != 1 {
		t.Fatalf("failure evidence rows=%d want=1 err=%v", failureCount, err)
	}
	var stage, code, requestDigest string
	if err := pool.QueryRow(ctx, `SELECT failure_stage,failure_code,request_key_digest FROM role_source_apply_failure WHERE source_id=$1`, fixture.sourceID).Scan(&stage, &code, &requestDigest); err != nil {
		t.Fatal(err)
	}
	if stage != expectedStage || code != "internal_failure" || !strings.HasPrefix(requestDigest, "sha256:") || strings.Contains(requestDigest, fixture.input.RequestKey) {
		t.Fatalf("failure evidence stage=%s code=%s request=%s", stage, code, requestDigest)
	}
}

func assertApplyCommittedOnce(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture applyFailureFixture, receiptDigest string) {
	t.Helper()
	var current string
	if err := pool.QueryRow(ctx, `SELECT current_snapshot_digest FROM role_source WHERE id=$1`, fixture.sourceID).Scan(&current); err != nil || current != fixture.toDigest {
		t.Fatalf("source current snapshot=%s err=%v", current, err)
	}
	checks := []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM role_source_apply WHERE source_id=$1 AND status='succeeded' AND receipt_digest=$2`, 1},
		{`SELECT count(*) FROM role_source_outbox WHERE source_id=$1 AND receipt_digest=$2`, 1},
		{`SELECT count(*) FROM role_source_audit_event WHERE source_id=$1 AND event_type='apply_succeeded' AND payload->>'receipt_digest'=$2`, 1},
		{`SELECT count(*) FROM role_source_apply_failure WHERE source_id=$1`, 0},
	}
	for _, check := range checks {
		var count int
		args := []any{fixture.sourceID}
		if strings.Contains(check.query, "$2") {
			args = append(args, receiptDigest)
		}
		if err := pool.QueryRow(ctx, check.query, args...).Scan(&count); err != nil || count != check.want {
			t.Fatalf("query %q count=%d want=%d err=%v", check.query, count, check.want, err)
		}
	}
}

func cleanupApplyFailureFixture(t *testing.T, pool *pgxpool.Pool, fixture applyFailureFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin apply failure cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := db.New(tx)
	if err := q.SetWorkspaceTeardownMode(ctx); err != nil {
		t.Errorf("set apply failure teardown mode: %v", err)
		return
	}
	if err := q.DeleteWorkspaceRoleSources(ctx, pgUUID(fixture.workspaceID)); err != nil {
		t.Errorf("delete apply failure role sources: %v", err)
		return
	}
	if len(fixture.artifactKeys) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM role_source_artifact_delete_intent WHERE storage_key = ANY($1::text[])`, fixture.artifactKeys); err != nil {
			t.Errorf("delete apply failure artifact intents: %v", err)
			return
		}
	}
	for name, statement := range map[string]string{
		"workspace": `DELETE FROM workspace WHERE id=$1`,
		"actor":     `DELETE FROM "user" WHERE id=$1`,
	} {
		arg := any(fixture.workspaceID)
		if name == "actor" {
			arg = fixture.actorID
		}
		if _, err := tx.Exec(ctx, statement, arg); err != nil {
			t.Errorf("delete apply failure %s: %v", name, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit apply failure cleanup: %v", err)
	}
}
