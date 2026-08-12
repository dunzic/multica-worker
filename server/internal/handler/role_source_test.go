package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	roleSourceTestRuntimeID = "00000000-0000-4000-8000-000000000041"
	roleSourceTestSourceID  = "00000000-0000-4000-8000-000000000042"
	roleSourceTestScanID    = "00000000-0000-4000-8000-000000000043"
)

type fakeRoleSourceControlPlane struct {
	registerInput *rolesource.RegisterSourceInput
	registerRow   db.RoleSource
	registerErr   error
	requestRow    db.RoleSourceScanRequest
	requestErr    error
	getScanRow    db.RoleSourceScanRequest
	getScanErr    error
	listRows      []db.RoleSource
	listErr       error
	getSourceRow  db.RoleSource
	getSourceErr  error
	claimRow      rolesource.ClaimedScan
	claimErr      error
	claimCalls    int
	renewRow      db.RoleSourceScanRequest
	renewErr      error
	calls         int
}

type roleSourcePendingWorkRecorder struct {
	runtimeID string
	kind      string
	calls     int
}

func (r *roleSourcePendingWorkRecorder) NotifyPendingWork(runtimeID, kind string) {
	r.runtimeID, r.kind = runtimeID, kind
	r.calls++
}

func (f *fakeRoleSourceControlPlane) RegisterSource(_ context.Context, input rolesource.RegisterSourceInput) (db.RoleSource, error) {
	f.calls++
	f.registerInput = &input
	return f.registerRow, f.registerErr
}

func (f *fakeRoleSourceControlPlane) RequestScan(context.Context, string, string, string) (db.RoleSourceScanRequest, error) {
	f.calls++
	return f.requestRow, f.requestErr
}

func (f *fakeRoleSourceControlPlane) ListSources(context.Context, string) ([]db.RoleSource, error) {
	f.calls++
	return f.listRows, f.listErr
}

func (f *fakeRoleSourceControlPlane) GetSource(context.Context, string, string) (db.RoleSource, error) {
	f.calls++
	return f.getSourceRow, f.getSourceErr
}

func (f *fakeRoleSourceControlPlane) GetScan(context.Context, string, string, string) (db.RoleSourceScanRequest, error) {
	f.calls++
	return f.getScanRow, f.getScanErr
}

func (f *fakeRoleSourceControlPlane) ClaimNextScan(context.Context, string, time.Duration) (rolesource.ClaimedScan, error) {
	f.claimCalls++
	return f.claimRow, f.claimErr
}

func (f *fakeRoleSourceControlPlane) RenewScanLease(context.Context, string, string, string, string, string, time.Duration) (db.RoleSourceScanRequest, error) {
	return f.renewRow, f.renewErr
}

func (f *fakeRoleSourceControlPlane) ReportScanSuccess(context.Context, rolesource.ReportScanSuccessInput) (db.RoleSourceSnapshot, error) {
	return db.RoleSourceSnapshot{}, errors.New("unexpected report success")
}

func (f *fakeRoleSourceControlPlane) ReportScanFailure(context.Context, rolesource.ReportScanFailureInput) (db.RoleSourceScanRequest, error) {
	return db.RoleSourceScanRequest{}, errors.New("unexpected report failure")
}

func roleSourceTestHandler(t *testing.T, enabled bool, controlPlane *fakeRoleSourceControlPlane) *Handler {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	provider.LoadRules(map[string]featureflag.Rule{
		rolesource.FeatureFlagRoleSourceSync: {Default: enabled},
		rolesource.FeatureFlagRoleSourceScan: {Default: enabled},
	})
	catalog, err := rolesource.NewCatalog(rolesource.Descriptor{
		Kind: "test_directory", DisplayName: "Test directory", AdapterVersion: "1.0.0",
		ContractVersion: rolesource.ContractVersion,
	})
	if err != nil {
		t.Fatalf("new role source catalog: %v", err)
	}
	return &Handler{
		FeatureFlags: featureflag.NewService(provider),
		RoleSources:  controlPlane, RoleSourceCatalog: catalog,
	}
}

func roleSourceTestRow() db.RoleSource {
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC), Valid: true}
	return db.RoleSource{
		ID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
		RuntimeID: util.MustParseUUID(roleSourceTestRuntimeID), Name: "AgentWaker roles", Kind: "test_directory",
		AdapterVersion: "1.0.0", DaemonConfigID: "local-secret-config-handle",
		ConfigRedacted: []byte(`{"configured":true,"attributes":[{"name":"root_name","value":"roles"}]}`),
		Policy:         []byte(`{"deletion":"approval_required"}`), State: "active", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
}

func roleSourceTestScanRow() db.RoleSourceScanRequest {
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 10, 1, 0, 0, time.UTC), Valid: true}
	return db.RoleSourceScanRequest{
		ID: util.MustParseUUID(roleSourceTestScanID), SourceID: util.MustParseUUID(roleSourceTestSourceID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID), Status: "running", ExpectedAdapterVersion: "1.0.0",
		LeaseToken: util.MustParseUUID("00000000-0000-4000-8000-000000000044"), RequestedAt: now, ClaimedAt: now,
	}
}

func TestCreateRoleSource_DefaultOffIsNotFoundAndDoesNotReachControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, false, fake)
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(testUserID, http.MethodPost, "/api/workspaces/ignored/role-sources", map[string]any{}), "id", testWorkspaceID)

	h.CreateRoleSource(w, req)

	if w.Code != http.StatusNotFound || fake.calls != 0 {
		t.Fatalf("default-off create: status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestCreateRoleSource_RejectsUnknownFieldsBeforeControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(testUserID, http.MethodPost, "/api/workspaces/ignored/role-sources", map[string]any{
		"runtime_id": roleSourceTestRuntimeID, "name": "roles", "kind": "test_directory",
		"adapter_version": "1.0.0", "daemon_config_id": "config-1", "config_summary": map[string]any{"configured": true},
		"unexpected": "must fail closed",
	}), "id", testWorkspaceID)

	h.CreateRoleSource(w, req)

	if w.Code != http.StatusBadRequest || fake.calls != 0 {
		t.Fatalf("unknown field: status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestCreateRoleSource_ResponseDoesNotExposeDaemonConfiguration(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{registerRow: roleSourceTestRow()}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(testUserID, http.MethodPost, "/api/workspaces/ignored/role-sources", map[string]any{
		"runtime_id": roleSourceTestRuntimeID, "name": "AgentWaker roles", "kind": "test_directory",
		"adapter_version": "1.0.0", "daemon_config_id": "local-secret-config-handle",
		"config_summary": map[string]any{"configured": true, "attributes": []map[string]string{{"name": "root_name", "value": "roles"}}},
		"policy":         map[string]string{"deletion": "approval_required"},
	}), "id", testWorkspaceID)

	h.CreateRoleSource(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if fake.registerInput == nil || fake.registerInput.DaemonConfigID != "local-secret-config-handle" {
		t.Fatalf("control plane did not receive daemon-local config handle: %+v", fake.registerInput)
	}
	body := w.Body.String()
	for _, forbidden := range []string{"daemon_config_id", "local-secret-config-handle", "root_path", "lease_token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"root_name":"roles"`) {
		t.Fatalf("response omitted safe config summary: %s", body)
	}
}

func TestRequestRoleSourceScan_MapsActiveScanToConflict(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestErr: rolesource.ErrScanAlreadyActive}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.RequestRoleSourceScan(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("active scan: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetRoleSourceScan_ResponseDoesNotExposeLeaseToken(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{getScanRow: roleSourceTestScanRow()}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "scanId", roleSourceTestScanID)

	h.GetRoleSourceScan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get scan: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, exposed := body["lease_token"]; exposed || strings.Contains(w.Body.String(), "000000000044") {
		t.Fatalf("scan response exposed lease token: %s", w.Body.String())
	}
}

func TestRequestRoleSourceScan_WakesOwningRuntime(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestRow: roleSourceTestScanRow(), getSourceRow: roleSourceTestRow()}
	h := roleSourceTestHandler(t, true, fake)
	recorder := &roleSourcePendingWorkRecorder{}
	h.DaemonPendingWork = recorder
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.RequestRoleSourceScan(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("request scan: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if recorder.calls != 1 || recorder.runtimeID != roleSourceTestRuntimeID || recorder.kind != protocol.PendingWorkKindRoleSourceScan {
		t.Fatalf("runtime wakeup = %+v", recorder)
	}
}

func TestPopulateRoleSourceHeartbeat_SeparatesNegotiationFromPolling(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	ack := &protocol.DaemonHeartbeatAckPayload{}

	h.populateRoleSourceHeartbeat(context.Background(), ack, roleSourceTestRuntimeID, testWorkspaceID, true, false)

	if fake.claimCalls != 0 {
		t.Fatalf("capability-only heartbeat hit database claim %d times", fake.claimCalls)
	}
	if len(ack.ServerCapabilities) != 1 || ack.ServerCapabilities[0] != protocol.DaemonCapabilityRoleSourceScanV1 {
		t.Fatalf("server capabilities = %v", ack.ServerCapabilities)
	}

	claimed := roleSourceTestScanRow()
	claimed.LeaseExpiresAt = pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 10, 3, 0, 0, time.UTC), Valid: true}
	fake.claimRow = rolesource.ClaimedScan{Request: claimed, Source: roleSourceTestRow()}
	h.populateRoleSourceHeartbeat(context.Background(), ack, roleSourceTestRuntimeID, testWorkspaceID, true, true)

	if fake.claimCalls != 1 || ack.PendingRoleSourceScan == nil {
		t.Fatalf("poll did not claim scan: calls=%d pending=%+v", fake.claimCalls, ack.PendingRoleSourceScan)
	}
	pending := ack.PendingRoleSourceScan
	if pending.RequestID != roleSourceTestScanID || pending.DaemonConfigID != "local-secret-config-handle" || pending.LeaseToken == "" {
		t.Fatalf("pending scan = %+v", pending)
	}
}

func TestDeleteWorkspace_RemovesEntireRoleSourceGraph(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var workspaceID, runtimeID, sourceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description)
		VALUES ('Role Source Delete Test', 'role-source-delete-' || gen_random_uuid()::text, '')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"role_source_audit_event", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_snapshot", "role_source_scan_request", "role_source"} {
			_, _ = testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE workspace_id = $1", workspaceID)
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, workspaceID, testUserID); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id)
		VALUES ($1, 'role-source-runtime', 'cloud', 'handler_test_runtime', 'online', 'fixture', '{}'::jsonb, $2)
		RETURNING id
	`, workspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO role_source (
			id, workspace_id, runtime_id, name, kind, adapter_version, daemon_config_id,
			config_redacted, policy, audit_sequence, created_by, updated_by
		) VALUES (
			gen_random_uuid(), $1, $2, 'delete fixture', 'agentwaker_directory', '0.1.0', 'fixture',
			'{"configured":true}'::jsonb, '{}'::jsonb, 1, $3, $3
		) RETURNING id
	`, workspaceID, runtimeID, testUserID).Scan(&sourceID); err != nil {
		t.Fatalf("create source: %v", err)
	}
	digestA, digestB := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	execFixture := func(statement string, arguments ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, statement, arguments...); err != nil {
			t.Fatalf("create role source graph: %v", err)
		}
	}
	execFixture(`
		INSERT INTO role_source_scan_request (id, source_id, workspace_id, requested_by, expected_adapter_version)
		VALUES (gen_random_uuid(), $1, $2, $3, '0.1.0')
	`, sourceID, workspaceID, testUserID)
	execFixture(`
		INSERT INTO role_source_snapshot (
			source_id, workspace_id, snapshot_digest, manifest_digest, kind, adapter_version,
			contract_version, manifest, diagnostics, source_evidence, reported_by_runtime_id
		) VALUES ($1, $2, $3, $4, 'agentwaker_directory', '0.1.0', '1.0', '{}'::jsonb, '[]'::jsonb, '{}'::jsonb, $5)
	`, sourceID, workspaceID, digestA, digestB, runtimeID)
	execFixture(`
		INSERT INTO role_source_plan (source_id, workspace_id, plan_digest, to_snapshot_digest, plan, created_by)
		VALUES ($1, $2, $3, $4, '{}'::jsonb, $5)
	`, sourceID, workspaceID, digestB, digestA, testUserID)
	execFixture(`
		INSERT INTO role_source_plan_approval (id, source_id, workspace_id, plan_digest, decision, decisions, actor_user_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'approved', '{}'::jsonb, $4)
	`, sourceID, workspaceID, digestB, testUserID)
	execFixture(`
		INSERT INTO role_source_apply (id, source_id, workspace_id, request_key, mode, snapshot_digest, plan_digest, status, actor_user_id)
		VALUES (gen_random_uuid(), $1, $2, 'delete-fixture', 'apply', $3, $4, 'pending', $5)
	`, sourceID, workspaceID, digestA, digestB, testUserID)
	execFixture(`
		INSERT INTO role_source_audit_event (id, source_id, workspace_id, sequence, event_type, actor_type, actor_id, event_digest, payload)
		VALUES (gen_random_uuid(), $1, $2, 1, 'source_registered', 'user', $3, $4, '{}'::jsonb)
	`, sourceID, workspaceID, testUserID, digestA)

	w := httptest.NewRecorder()
	req := withURLParam(newRequestAs(testUserID, http.MethodDelete, "/api/workspaces/"+workspaceID, nil), "id", workspaceID)
	testHandler.DeleteWorkspace(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete workspace: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"role_source_audit_event", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_snapshot", "role_source_scan_request", "role_source"} {
		var count int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", workspaceID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows survived workspace delete: %d", table, count)
		}
	}
}
