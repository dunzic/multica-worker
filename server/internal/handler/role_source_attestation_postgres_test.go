package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRoleSourceRuntimeAttestationPersistsDistinctRestartHistory(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Attestation History")
	cleanupRoleSourceRuntimeAttestations(t, runtimeID)
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}

	first := liveRoleSourceAttestationRequest(t, runtimeID, "a", "1.0.0")
	for attempt := 0; attempt < 2; attempt++ {
		accepted, err := testHandler.recordRoleSourceRuntimeAttestation(ctx, runtime, first)
		if err != nil {
			t.Fatalf("persist first attestation attempt %d: %v", attempt+1, err)
		}
		if accepted != first.RoleSourceConfigAttestation.AttestationID {
			t.Fatalf("accepted attestation = %q, want %q", accepted, first.RoleSourceConfigAttestation.AttestationID)
		}
	}

	var currentID string
	if err := testPool.QueryRow(ctx, `
		SELECT attestation_id
		FROM role_source_runtime_attestation
		WHERE runtime_id = $1
	`, runtimeID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	if currentID != first.RoleSourceConfigAttestation.AttestationID {
		t.Fatalf("current attestation = %q, want first state %q", currentID, first.RoleSourceConfigAttestation.AttestationID)
	}
	var observations int
	var restartCount int64
	if err := testPool.QueryRow(ctx, `
		SELECT count(*), max(observation_count)
		FROM role_source_runtime_attestation_observation
		WHERE runtime_id = $1
	`, runtimeID).Scan(&observations, &restartCount); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || restartCount != 2 {
		t.Fatalf("duplicate state history = %d rows / %d observations, want 1 / 2", observations, restartCount)
	}

	second := liveRoleSourceAttestationRequest(t, runtimeID, "b", "1.1.0")
	accepted, err := testHandler.recordRoleSourceRuntimeAttestation(ctx, runtime, second)
	if err != nil {
		t.Fatalf("persist changed attestation: %v", err)
	}
	if accepted != second.RoleSourceConfigAttestation.AttestationID {
		t.Fatalf("accepted changed attestation = %q, want %q", accepted, second.RoleSourceConfigAttestation.AttestationID)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT attestation_id
		FROM role_source_runtime_attestation
		WHERE runtime_id = $1
	`, runtimeID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	if currentID != second.RoleSourceConfigAttestation.AttestationID {
		t.Fatalf("current attestation = %q, want changed state %q", currentID, second.RoleSourceConfigAttestation.AttestationID)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*)
		FROM role_source_runtime_attestation_observation
		WHERE runtime_id = $1
	`, runtimeID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 2 {
		t.Fatalf("distinct attestation history rows = %d, want 2", observations)
	}
}

func TestRoleSourceRuntimeAttestationPersistsUnloadedSourcesAsEmptyArray(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Unloaded Attestation")
	cleanupRoleSourceRuntimeAttestations(t, runtimeID)
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	attestation, err := protocol.NewRoleSourceConfigAttestation(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.DaemonHeartbeatRequestPayload{
		RuntimeID: runtimeID, SupportsRoleSourceConfigAttestation: true,
		RoleSourceConfigAttestation: &attestation,
	}
	accepted, err := testHandler.recordRoleSourceRuntimeAttestation(ctx, runtime, request)
	if err != nil {
		t.Fatalf("persist unloaded attestation: %v", err)
	}
	if accepted != attestation.AttestationID {
		t.Fatalf("accepted attestation = %q, want %q", accepted, attestation.AttestationID)
	}

	for _, table := range []string{
		"role_source_runtime_attestation",
		"role_source_runtime_attestation_observation",
	} {
		var sourceType, sourceBody string
		query := "SELECT jsonb_typeof(sources), sources::text FROM " + table + " WHERE runtime_id = $1"
		if err := testPool.QueryRow(ctx, query, runtimeID).Scan(&sourceType, &sourceBody); err != nil {
			t.Fatalf("read %s unloaded evidence: %v", table, err)
		}
		if sourceType != "array" || sourceBody != "[]" {
			t.Fatalf("%s sources = type %q body %q, want array []", table, sourceType, sourceBody)
		}
	}
}

func TestRoleSourceRuntimeAttestationShapeConstraintsRejectScalarsCleanly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Invalid Attestation Shape")
	cleanupRoleSourceRuntimeAttestations(t, runtimeID)

	for _, table := range []string{
		"role_source_runtime_attestation",
		"role_source_runtime_attestation_observation",
	} {
		_, err := testPool.Exec(ctx, `
			INSERT INTO `+table+` (
				runtime_id, workspace_id, contract_version, loaded,
				attestation_id, config_revision, sources
			) VALUES ($1, $2, $3, false, $4, NULL, 'null'::jsonb)
		`, runtimeID, testWorkspaceID, protocol.RoleSourceConfigAttestationContractV1, "sha256:"+strings.Repeat("d", 64))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("%s scalar sources error = %v, want SQLSTATE 23514 check violation", table, err)
		}
	}
}

func TestRoleSourceRuntimeAttestationCannotReappearAfterRuntimeDelete(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Attestation Delete Race")
	cleanupRoleSourceRuntimeAttestations(t, runtimeID)
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}

	deleteTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(context.Background())
	deleteQueries := testHandler.Queries.WithTx(deleteTx)
	if _, err := deleteQueries.LockAgentRuntime(ctx, runtime.ID); err != nil {
		t.Fatalf("lock runtime for delete: %v", err)
	}
	if err := deleteQueries.DeleteAgentRuntime(ctx, runtime.ID); err != nil {
		t.Fatalf("delete runtime inside held transaction: %v", err)
	}

	type persistenceResult struct {
		accepted string
		err      error
	}
	result := make(chan persistenceResult, 1)
	request := liveRoleSourceAttestationRequest(t, runtimeID, "c", "1.0.0")
	go func() {
		accepted, err := testHandler.recordRoleSourceRuntimeAttestation(context.Background(), runtime, request)
		result <- persistenceResult{accepted: accepted, err: err}
	}()

	select {
	case early := <-result:
		t.Fatalf("attestation write bypassed the runtime delete lock: accepted=%q err=%v", early.accepted, early.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case completed := <-result:
		if completed.err == nil || completed.accepted != "" {
			t.Fatalf("attestation write survived committed runtime deletion: accepted=%q err=%v", completed.accepted, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attestation writer did not unblock after runtime deletion committed")
	}
	for _, table := range []string{
		"role_source_runtime_attestation",
		"role_source_runtime_attestation_observation",
	} {
		var rows int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE runtime_id = $1", runtimeID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s retained %d orphan rows after runtime deletion", table, rows)
		}
	}
}

func TestRoleSourceRegistrationLockSerializesRuntimeDelete(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Registration Delete Race")

	registrationTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer registrationTx.Rollback(context.Background())
	registrationQueries := testHandler.Queries.WithTx(registrationTx)
	if _, err := registrationQueries.LockWorkspaceForRoleSourceMutation(ctx, parseUUID(testWorkspaceID)); err != nil {
		t.Fatalf("lock workspace for registration: %v", err)
	}
	if _, err := registrationQueries.LockRoleSourceRuntimeForRegistration(ctx, db.LockRoleSourceRuntimeForRegistrationParams{
		RuntimeID: parseUUID(runtimeID), WorkspaceID: parseUUID(testWorkspaceID),
	}); err != nil {
		t.Fatalf("lock runtime for registration: %v", err)
	}

	deleteTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteTx.Rollback(context.Background())
	if _, err := deleteTx.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = testHandler.Queries.WithTx(deleteTx).LockAgentRuntime(ctx, parseUUID(runtimeID))
	if err == nil {
		t.Fatal("runtime delete lock bypassed the role-source registration lock")
	}
	if pgError := new(pgconn.PgError); !errors.As(err, &pgError) || pgError.Code != "55P03" {
		t.Fatalf("runtime delete lock error = %v, want PostgreSQL lock timeout 55P03", err)
	}
}

func TestRoleSourceLifecyclePauseResumeDetachAndRebind(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Lifecycle A")
	destinationRuntimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Lifecycle B")
	cleanupRoleSourceRuntimeAttestations(t, runtimeID)

	source, err := testHandler.RoleSources.RegisterSource(ctx, rolesource.RegisterSourceInput{
		WorkspaceID: testWorkspaceID, RuntimeID: runtimeID, ActorUserID: testUserID,
		Name: "Lifecycle " + runtimeID, Kind: "agentwaker_directory", AdapterVersion: "0.1.0",
		DaemonConfigID: "production", ConfigSummary: rolesource.ConfigSummary{Configured: true},
	})
	if err != nil {
		t.Fatalf("register lifecycle source: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_audit_event WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source WHERE id = $1`, source.ID)
	})

	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	request := liveRoleSourceAttestationRequest(t, runtimeID, "d", "0.1.0")
	if _, err := testHandler.recordRoleSourceRuntimeAttestation(ctx, runtime, request); err != nil {
		t.Fatalf("record lifecycle attestation: %v", err)
	}

	source, err = testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecyclePause,
	})
	if err != nil || source.State != "paused" {
		t.Fatalf("pause source: state=%q err=%v", source.State, err)
	}
	source, err = testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecycleResume,
	})
	if err != nil || source.State != "registered" {
		t.Fatalf("resume source: state=%q err=%v", source.State, err)
	}
	source, err = testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecyclePause,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err = testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecycleDetach,
	})
	if err != nil || source.State != "detached" {
		t.Fatalf("detach source: state=%q err=%v", source.State, err)
	}
	count, err := testHandler.Queries.CountRoleSourcesByRuntime(ctx, parseUUID(runtimeID))
	if err != nil || count != 0 {
		t.Fatalf("detached source still pins runtime: count=%d err=%v", count, err)
	}
	source, err = testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecycleRebind,
		RuntimeID: destinationRuntimeID, DaemonConfigID: "destination",
	})
	if err != nil || source.State != "paused" || util.UUIDToString(source.RuntimeID) != destinationRuntimeID {
		t.Fatalf("rebind source: state=%q runtime=%s err=%v", source.State, util.UUIDToString(source.RuntimeID), err)
	}
	if _, err := testHandler.RoleSources.UpdateSourceLifecycle(ctx, rolesource.UpdateSourceLifecycleInput{
		WorkspaceID: testWorkspaceID, SourceID: util.UUIDToString(source.ID), ActorUserID: testUserID,
		ExpectedVersion: source.Version, Action: rolesource.SourceLifecycleResume,
	}); !errors.Is(err, rolesource.ErrLifecycleConfigNotLoaded) {
		t.Fatalf("rebound source resumed without destination attestation: %v", err)
	}
}

func TestRoleSourceLegalHoldCreateReleaseAndAudit(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Legal Hold")
	source, err := testHandler.RoleSources.RegisterSource(ctx, rolesource.RegisterSourceInput{
		WorkspaceID: testWorkspaceID, RuntimeID: runtimeID, ActorUserID: testUserID,
		Name: "Legal Hold " + runtimeID, Kind: "agentwaker_directory", AdapterVersion: "0.1.0",
		DaemonConfigID: "production", ConfigSummary: rolesource.ConfigSummary{Configured: true},
	})
	if err != nil {
		t.Fatalf("register legal-hold source: %v", err)
	}
	sourceID := util.UUIDToString(source.ID)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = testPool.Exec(cleanupCtx, `
			INSERT INTO role_source_legal_hold_release (
				hold_id, workspace_id, source_id, request_key_digest, reason_code, released_by
			)
			SELECT id, workspace_id, source_id, 'sha256:' || repeat('0', 64), 'entered_in_error', $2
			FROM role_source_legal_hold WHERE source_id = $1
			ON CONFLICT (hold_id) DO NOTHING
		`, source.ID, util.MustParseUUID(testUserID))
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_legal_hold WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_legal_hold_release WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_audit_event WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source WHERE id = $1`, source.ID)
	})

	createInput := rolesource.CreateLegalHoldInput{
		WorkspaceID: testWorkspaceID, SourceID: sourceID, ActorUserID: testUserID,
		RequestKey: "legal-hold-live-create", Scope: rolesource.LegalHoldScopeSource,
		ReasonCode: rolesource.LegalHoldReasonRegulatory, ReferenceDigest: "sha256:" + strings.Repeat("e", 64),
	}
	created, err := testHandler.RoleSources.CreateLegalHold(ctx, createInput)
	if err != nil || !created.Active() {
		t.Fatalf("create legal hold: hold=%+v err=%v", created, err)
	}
	retried, err := testHandler.RoleSources.CreateLegalHold(ctx, createInput)
	if err != nil || retried.ID != created.ID {
		t.Fatalf("retry legal hold: hold=%+v err=%v", retried, err)
	}
	conflicting := createInput
	conflicting.ReasonCode = rolesource.LegalHoldReasonLitigation
	if _, err := testHandler.RoleSources.CreateLegalHold(ctx, conflicting); !errors.Is(err, rolesource.ErrIdempotencyConflict) {
		t.Fatalf("conflicting legal hold retry error=%v", err)
	}

	listed, err := testHandler.RoleSources.ListLegalHolds(ctx, testWorkspaceID, sourceID, 100)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID || !listed[0].Active() {
		t.Fatalf("list active legal hold: rows=%+v err=%v", listed, err)
	}
	releaseInput := rolesource.ReleaseLegalHoldInput{
		WorkspaceID: testWorkspaceID, SourceID: sourceID, HoldID: created.ID, ActorUserID: testUserID,
		RequestKey: "legal-hold-live-release", ReasonCode: rolesource.LegalHoldReleaseCourtOrder,
		ReferenceDigest: "sha256:" + strings.Repeat("f", 64),
	}
	released, err := testHandler.RoleSources.ReleaseLegalHold(ctx, releaseInput)
	if err != nil || released.Active() || released.ReleaseReasonCode != rolesource.LegalHoldReleaseCourtOrder {
		t.Fatalf("release legal hold: hold=%+v err=%v", released, err)
	}
	retriedRelease, err := testHandler.RoleSources.ReleaseLegalHold(ctx, releaseInput)
	if err != nil || retriedRelease.ReleasedAt != released.ReleasedAt {
		t.Fatalf("retry legal hold release: hold=%+v err=%v", retriedRelease, err)
	}
	retriedCreateAfterRelease, err := testHandler.RoleSources.CreateLegalHold(ctx, createInput)
	if err != nil || retriedCreateAfterRelease.Active() || retriedCreateAfterRelease.ReleasedAt != released.ReleasedAt {
		t.Fatalf("retry legal hold creation after release: hold=%+v err=%v", retriedCreateAfterRelease, err)
	}
	conflictingRelease := releaseInput
	conflictingRelease.RequestKey = "another-release"
	if _, err := testHandler.RoleSources.ReleaseLegalHold(ctx, conflictingRelease); !errors.Is(err, rolesource.ErrLegalHoldReleased) {
		t.Fatalf("conflicting legal hold release error=%v", err)
	}

	var createEvents, releaseEvents int
	if err := testPool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE event_type = 'legal_hold_created'),
			count(*) FILTER (WHERE event_type = 'legal_hold_released')
		FROM role_source_audit_event WHERE source_id = $1
	`, source.ID).Scan(&createEvents, &releaseEvents); err != nil {
		t.Fatal(err)
	}
	if createEvents != 1 || releaseEvents != 1 {
		t.Fatalf("legal-hold audit counts=create:%d release:%d, want 1/1", createEvents, releaseEvents)
	}
	if _, err := testPool.Exec(ctx, `UPDATE role_source_legal_hold SET reason_code = 'litigation' WHERE id = $1`, util.MustParseUUID(created.ID)); err == nil {
		t.Fatal("database allowed legal hold authority to be rewritten")
	}
	if _, err := testPool.Exec(ctx, `UPDATE role_source_legal_hold_release SET reason_code = 'resolved' WHERE hold_id = $1`, util.MustParseUUID(created.ID)); err == nil {
		t.Fatal("database allowed legal hold release authority to be rewritten")
	}
}

func TestRoleSourceRetentionPolicyHoldFenceAndPrune(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	control, ok := testHandler.RoleSources.(*rolesource.ControlPlane)
	if !ok {
		t.Fatal("live retention test requires concrete role-source control plane")
	}
	runtimeID := createCascadeFixtureRuntime(t, ctx, "Role Source Retention")
	source, err := testHandler.RoleSources.RegisterSource(ctx, rolesource.RegisterSourceInput{
		WorkspaceID: testWorkspaceID, RuntimeID: runtimeID, ActorUserID: testUserID,
		Name: "Retention " + runtimeID, Kind: "agentwaker_directory", AdapterVersion: "0.1.0",
		DaemonConfigID: "production", ConfigSummary: rolesource.ConfigSummary{Configured: true},
	})
	if err != nil {
		t.Fatalf("register retention source: %v", err)
	}
	sourceID := util.UUIDToString(source.ID)
	snapshotDigest := "sha256:" + strings.Repeat("8", 64)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = testPool.Exec(cleanupCtx, `
			INSERT INTO role_source_legal_hold_release (
				hold_id, workspace_id, source_id, request_key_digest, reason_code, released_by
			)
			SELECT id, workspace_id, source_id, 'sha256:' || repeat('0', 64), 'entered_in_error', $2
			FROM role_source_legal_hold WHERE source_id = $1
			ON CONFLICT (hold_id) DO NOTHING
		`, source.ID, util.MustParseUUID(testUserID))
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_legal_hold WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_legal_hold_release WHERE source_id = $1`, source.ID)
		tx, txErr := testPool.Begin(cleanupCtx)
		if txErr == nil {
			_, _ = tx.Exec(cleanupCtx, `SELECT set_config('multica.workspace_teardown', 'on', true)`)
			_, _ = tx.Exec(cleanupCtx, `DELETE FROM role_source_retention_candidate WHERE source_id = $1`, source.ID)
			_, _ = tx.Exec(cleanupCtx, `DELETE FROM role_source_retention_policy WHERE source_id = $1`, source.ID)
			_, _ = tx.Exec(cleanupCtx, `DELETE FROM role_source_snapshot_artifact WHERE source_id = $1`, source.ID)
			_, _ = tx.Exec(cleanupCtx, `DELETE FROM role_source_snapshot WHERE source_id = $1`, source.ID)
			_ = tx.Commit(cleanupCtx)
		}
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source_audit_event WHERE source_id = $1`, source.ID)
		_, _ = testPool.Exec(cleanupCtx, `DELETE FROM role_source WHERE id = $1`, source.ID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO role_source_snapshot (
			source_id, workspace_id, snapshot_digest, manifest_digest, kind,
			adapter_version, contract_version, manifest, diagnostics, source_evidence,
			reported_by_runtime_id, created_at
		) VALUES ($1, $2, $3, $3, 'agentwaker_directory', '0.1.0', '1.0',
			'{"roles":[],"capabilities":[]}'::jsonb, '[]'::jsonb, '{}'::jsonb, $4,
			now() - interval '120 days')
	`, source.ID, util.MustParseUUID(testWorkspaceID), snapshotDigest, util.MustParseUUID(runtimeID)); err != nil {
		t.Fatalf("insert old retention snapshot: %v", err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM role_source_snapshot WHERE source_id = $1 AND snapshot_digest = $2`, source.ID, snapshotDigest); err == nil {
		t.Fatal("database allowed historical snapshot deletion without retention authority")
	}
	policy, err := testHandler.RoleSources.UpdateRetentionPolicy(ctx, rolesource.UpdateRetentionPolicyInput{
		WorkspaceID: testWorkspaceID, SourceID: sourceID, ActorUserID: testUserID,
		RequestKey: "retention-live-policy", ExpectedVersion: 0, Enabled: true,
		MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
	})
	if err != nil || !policy.Enabled || policy.Version != 1 {
		t.Fatalf("create retention policy: policy=%+v err=%v", policy, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE role_source_retention_policy SET enabled = false WHERE source_id = $1`, source.ID); err == nil {
		t.Fatal("database allowed an in-place retention policy revision")
	}
	hold, err := testHandler.RoleSources.CreateLegalHold(ctx, rolesource.CreateLegalHoldInput{
		WorkspaceID: testWorkspaceID, SourceID: sourceID, ActorUserID: testUserID,
		RequestKey: "retention-live-hold", Scope: rolesource.LegalHoldScopeSnapshot,
		SnapshotDigest: snapshotDigest, ReasonCode: rolesource.LegalHoldReasonLitigation,
	})
	if err != nil {
		t.Fatalf("create retention hold: %v", err)
	}
	if _, err := control.QueueNextRetentionCandidate(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("legal-held snapshot became retention candidate: %v", err)
	}
	if _, err := testHandler.RoleSources.ReleaseLegalHold(ctx, rolesource.ReleaseLegalHoldInput{
		WorkspaceID: testWorkspaceID, SourceID: sourceID, HoldID: hold.ID, ActorUserID: testUserID,
		RequestKey: "retention-live-release", ReasonCode: rolesource.LegalHoldReleaseCourtOrder,
	}); err != nil {
		t.Fatalf("release retention hold: %v", err)
	}
	candidate, err := control.QueueNextRetentionCandidate(ctx)
	if err != nil || candidate.SnapshotDigest != snapshotDigest {
		t.Fatalf("queue retention candidate: candidate=%+v err=%v", candidate, err)
	}
	token := util.MustParseUUID("00000000-0000-4000-8000-000000000089")
	claimed, err := testHandler.Queries.ClaimNextRoleSourceRetentionCandidate(ctx, db.ClaimNextRoleSourceRetentionCandidateParams{
		LeaseToken: token, LeaseDuration: pgtype.Interval{Microseconds: (5 * time.Minute).Microseconds(), Valid: true},
	})
	if err != nil || claimed.ID != candidate.ID {
		t.Fatalf("claim retention candidate: candidate=%+v err=%v", claimed, err)
	}
	outcome, err := control.PruneRetentionCandidate(ctx, candidate.ID, token)
	if err != nil || outcome != "pruned" {
		t.Fatalf("prune retention candidate: outcome=%q err=%v", outcome, err)
	}
	var snapshots, auditEvents int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM role_source_snapshot WHERE source_id = $1 AND snapshot_digest = $2`, source.ID, snapshotDigest).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM role_source_audit_event WHERE source_id = $1 AND event_type = 'snapshot_retention_pruned'`, source.ID).Scan(&auditEvents); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 || auditEvents != 1 {
		t.Fatalf("retention result snapshots=%d audit_events=%d", snapshots, auditEvents)
	}
}

func liveRoleSourceAttestationRequest(t *testing.T, runtimeID, revisionByte, adapterVersion string) protocol.DaemonHeartbeatRequestPayload {
	t.Helper()
	configDigest, err := protocol.RoleSourceConfigIDDigest(runtimeID, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisionByte) != 1 {
		t.Fatalf("revision byte %q must contain one character", revisionByte)
	}
	attestation, err := protocol.NewRoleSourceConfigAttestation(true, "sha256:"+strings.Repeat(revisionByte, 64), []protocol.RoleSourceLoadedConfig{{
		ConfigIDDigest: configDigest, Kind: "agentwaker_directory", AdapterVersion: adapterVersion,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.DaemonHeartbeatRequestPayload{
		RuntimeID: runtimeID, SupportsRoleSourceConfigAttestation: true,
		RoleSourceConfigAttestation: &attestation,
	}
}

func cleanupRoleSourceRuntimeAttestations(t *testing.T, runtimeID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = testPool.Exec(ctx, `DELETE FROM role_source_runtime_attestation_observation WHERE runtime_id = $1`, runtimeID)
		_, _ = testPool.Exec(ctx, `DELETE FROM role_source_runtime_attestation WHERE runtime_id = $1`, runtimeID)
	})
}
