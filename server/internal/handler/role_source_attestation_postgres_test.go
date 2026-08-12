package handler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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
