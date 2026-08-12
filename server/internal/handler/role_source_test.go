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
