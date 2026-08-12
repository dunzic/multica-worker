package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
	registerInput    *rolesource.RegisterSourceInput
	registerRow      db.RoleSource
	registerErr      error
	requestRow       db.RoleSourceScanRequest
	requestErr       error
	getScanRow       db.RoleSourceScanRequest
	getScanErr       error
	listRows         []db.RoleSource
	listErr          error
	getSourceRow     db.RoleSource
	getSourceErr     error
	claimRow         rolesource.ClaimedScan
	claimErr         error
	claimCalls       int
	renewRow         db.RoleSourceScanRequest
	renewErr         error
	createPlanInput  *rolesource.CreatePlanInput
	createPlanRow    db.RoleSourcePlan
	createPlanErr    error
	approvalInput    *rolesource.RecordPlanApprovalInput
	approvalRow      db.RoleSourcePlanApproval
	approvalErr      error
	applyInput       *rolesource.ApplyPlanInput
	applyRow         db.RoleSourceApply
	applyReceipt     rolesource.ApplyReceipt
	applyErr         error
	getPlanRow       db.RoleSourcePlan
	getPlanErr       error
	planRows         []db.RoleSourcePlan
	planListErr      error
	snapshotRows     []db.RoleSourceSnapshot
	snapshotListErr  error
	approvalRows     []db.RoleSourcePlanApproval
	approvalListErr  error
	missingArtifacts []rolesource.ArtifactRef
	missingErr       error
	storedArtifact   db.RoleSourceArtifact
	storedCreated    bool
	storeArtifactErr error
	calls            int
}

func (f *fakeRoleSourceControlPlane) ApplyPlan(_ context.Context, input rolesource.ApplyPlanInput) (db.RoleSourceApply, rolesource.ApplyReceipt, error) {
	f.calls++
	f.applyInput = &input
	return f.applyRow, f.applyReceipt, f.applyErr
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

func (f *fakeRoleSourceControlPlane) CreatePlan(_ context.Context, input rolesource.CreatePlanInput) (db.RoleSourcePlan, error) {
	f.calls++
	f.createPlanInput = &input
	return f.createPlanRow, f.createPlanErr
}

func (f *fakeRoleSourceControlPlane) RecordPlanApproval(_ context.Context, input rolesource.RecordPlanApprovalInput) (db.RoleSourcePlanApproval, error) {
	f.calls++
	f.approvalInput = &input
	return f.approvalRow, f.approvalErr
}

func (f *fakeRoleSourceControlPlane) GetPlan(context.Context, string, string, string) (db.RoleSourcePlan, error) {
	f.calls++
	return f.getPlanRow, f.getPlanErr
}

func (f *fakeRoleSourceControlPlane) ListPlans(context.Context, string, string, int32) ([]db.RoleSourcePlan, error) {
	f.calls++
	return f.planRows, f.planListErr
}

func (f *fakeRoleSourceControlPlane) ListSnapshots(context.Context, string, string, int32) ([]db.RoleSourceSnapshot, error) {
	f.calls++
	return f.snapshotRows, f.snapshotListErr
}

func (f *fakeRoleSourceControlPlane) ListPlanApprovals(context.Context, string, string, string, int32) ([]db.RoleSourcePlanApproval, error) {
	f.calls++
	return f.approvalRows, f.approvalListErr
}

func (f *fakeRoleSourceControlPlane) ListMissingArtifacts(context.Context, rolesource.ArtifactLeaseInput, []rolesource.ArtifactRef) ([]rolesource.ArtifactRef, error) {
	f.calls++
	return f.missingArtifacts, f.missingErr
}

func (f *fakeRoleSourceControlPlane) StoreArtifactRecord(context.Context, rolesource.StoreArtifactInput) (db.RoleSourceArtifact, bool, error) {
	f.calls++
	return f.storedArtifact, f.storedCreated, f.storeArtifactErr
}

func roleSourceTestHandler(t *testing.T, enabled bool, controlPlane *fakeRoleSourceControlPlane) *Handler {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	provider.LoadRules(map[string]featureflag.Rule{
		rolesource.FeatureFlagRoleSourceSync:  {Default: enabled},
		rolesource.FeatureFlagRoleSourceScan:  {Default: enabled},
		rolesource.FeatureFlagRoleSourceApply: {Default: enabled},
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

func roleSourceTestPlanRow(t *testing.T) db.RoleSourcePlan {
	t.Helper()
	plan := rolesource.Plan{
		ContractVersion: rolesource.PlanContractVersion, SourceID: roleSourceTestSourceID,
		ToSnapshotDigest: "sha256:" + strings.Repeat("a", 64), Applyable: true,
		Actions: []rolesource.PlanAction{}, Blockers: []rolesource.PlanBlocker{},
	}
	unsigned, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(unsigned)
	plan.PlanDigest = "sha256:" + hex.EncodeToString(sum[:])
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	now := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 10, 2, 0, 0, time.UTC), Valid: true}
	return db.RoleSourcePlan{
		SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
		PlanDigest: plan.PlanDigest, ToSnapshotDigest: plan.ToSnapshotDigest, Plan: body,
		CreatedBy: util.MustParseUUID(testUserID), CreatedAt: now,
	}
}

func roleSourceTestApprovalRow(t *testing.T, plan db.RoleSourcePlan) db.RoleSourcePlanApproval {
	t.Helper()
	decisions, err := json.Marshal(rolesource.ApprovalDecisions{
		ContractVersion: rolesource.PlanContractVersion, Archives: []rolesource.ArchiveActionDecision{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return db.RoleSourcePlanApproval{
		ID: util.MustParseUUID("00000000-0000-4000-8000-000000000045"), SourceID: plan.SourceID,
		WorkspaceID: plan.WorkspaceID, PlanDigest: plan.PlanDigest, RequestKey: "private-client-retry-key",
		Decision: "approved", Decisions: decisions, ActorUserID: util.MustParseUUID(testUserID), CreatedAt: plan.CreatedAt,
	}
}

func TestSpoolRoleSourceArtifactVerifiesDigestSizeAndLimit(t *testing.T) {
	payload := []byte("verified artifact")
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	file, size, err := spoolRoleSourceArtifact(strings.NewReader(string(payload)), int64(len(payload)), digest)
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	defer os.Remove(name) //nolint:errcheck
	defer file.Close()    //nolint:errcheck
	body, err := os.ReadFile(name)
	if err != nil || string(body) != string(payload) || size != int64(len(payload)) {
		t.Fatalf("spooled size=%d body=%q err=%v", size, body, err)
	}

	if _, _, err := spoolRoleSourceArtifact(strings.NewReader(string(payload)), int64(len(payload)-1), digest); err == nil {
		t.Fatal("spool accepted a body longer than declared")
	}
	if _, _, err := spoolRoleSourceArtifact(strings.NewReader(string(payload)), int64(len(payload)), "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("spool accepted a mismatched digest")
	}
	if _, _, err := spoolRoleSourceArtifact(strings.NewReader(""), maxRoleSourceArtifactUploadBytes+1, digest); err == nil {
		t.Fatal("spool accepted an oversized declaration")
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

func TestCreateRoleSourcePlan_DefaultOffDoesNotReachControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, false, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]string{
		"target_snapshot_digest": "sha256:" + strings.Repeat("a", 64),
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.CreateRoleSourcePlan(w, req)

	if w.Code != http.StatusNotFound || fake.calls != 0 {
		t.Fatalf("default-off plan: status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestCreateRoleSourcePlan_ReturnsVerifiedDeterministicPlan(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	fake := &fakeRoleSourceControlPlane{createPlanRow: plan}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]string{
		"target_snapshot_digest": plan.ToSnapshotDigest,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.CreateRoleSourcePlan(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create plan: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if fake.createPlanInput == nil || fake.createPlanInput.TargetSnapshotDigest != plan.ToSnapshotDigest || fake.createPlanInput.ActorUserID != testUserID {
		t.Fatalf("create plan input = %+v", fake.createPlanInput)
	}
	if !strings.Contains(w.Body.String(), plan.PlanDigest) {
		t.Fatalf("response omitted deterministic plan digest: %s", w.Body.String())
	}
}

func TestRecordRoleSourcePlanApproval_MapsRequestKeyReuseToConflict(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	fake := &fakeRoleSourceControlPlane{approvalErr: rolesource.ErrIdempotencyConflict}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "approve-once", "decision": "approved",
		"decisions": map[string]any{"contract_version": rolesource.PlanContractVersion, "archives": []any{}},
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.RecordRoleSourcePlanApproval(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("approval key reuse: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRecordRoleSourcePlanApproval_DoesNotExposeRequestKey(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	approval := roleSourceTestApprovalRow(t, plan)
	fake := &fakeRoleSourceControlPlane{approvalRow: approval}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": approval.RequestKey, "decision": "approved",
		"decisions": map[string]any{"contract_version": rolesource.PlanContractVersion, "archives": []any{}},
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.RecordRoleSourcePlanApproval(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("record approval: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if fake.approvalInput == nil || fake.approvalInput.RequestKey != approval.RequestKey {
		t.Fatalf("approval input = %+v", fake.approvalInput)
	}
	if strings.Contains(w.Body.String(), approval.RequestKey) || strings.Contains(w.Body.String(), "request_key") {
		t.Fatalf("approval response exposed idempotency key: %s", w.Body.String())
	}
}

func TestApplyRoleSourcePlan_UsesSeparateApplyGateAndReturnsReceipt(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	applyID := util.MustParseUUID("00000000-0000-4000-8000-000000000045")
	approvalID := "00000000-0000-4000-8000-000000000044"
	receipt := rolesource.ApplyReceipt{
		ContractVersion: rolesource.ApplyReceiptContractVersion, ApplyID: util.UUIDToString(applyID),
		SourceID: roleSourceTestSourceID, WorkspaceID: testWorkspaceID, SnapshotDigest: plan.ToSnapshotDigest,
		PlanDigest: plan.PlanDigest, ApprovalID: approvalID, Mappings: []rolesource.ApplyMapping{},
	}
	fake := &fakeRoleSourceControlPlane{
		applyRow:     db.RoleSourceApply{ID: applyID, SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID), Status: "succeeded"},
		applyReceipt: receipt,
	}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "apply-once", "approval_id": approvalID,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.ApplyRoleSourcePlan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("apply plan: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if fake.applyInput == nil || fake.applyInput.RequestKey != "apply-once" || fake.applyInput.ApprovalID != approvalID || fake.applyInput.ActorUserID != testUserID {
		t.Fatalf("apply input = %+v", fake.applyInput)
	}
	if strings.Contains(w.Body.String(), "apply-once") || !strings.Contains(w.Body.String(), approvalID) {
		t.Fatalf("apply response leaked request key or omitted approval evidence: %s", w.Body.String())
	}
}

func TestApplyRoleSourcePlan_DefaultOffDoesNotReachControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	provider := featureflag.NewStaticProvider()
	provider.LoadRules(map[string]featureflag.Rule{
		rolesource.FeatureFlagRoleSourceSync:  {Default: true},
		rolesource.FeatureFlagRoleSourceScan:  {Default: true},
		rolesource.FeatureFlagRoleSourceApply: {Default: false},
	})
	h.FeatureFlags = featureflag.NewService(provider)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "apply-off", "approval_id": "00000000-0000-4000-8000-000000000044",
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", "sha256:"+strings.Repeat("a", 64))

	h.ApplyRoleSourcePlan(w, req)

	if w.Code != http.StatusNotFound || fake.calls != 0 {
		t.Fatalf("default-off apply: status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestApplyRoleSourcePlan_MapsUnsafeMaterializationToUnprocessable(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	fake := &fakeRoleSourceControlPlane{applyErr: rolesource.ErrMaterializationBlocked}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "apply-blocked", "approval_id": "00000000-0000-4000-8000-000000000044",
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.ApplyRoleSourcePlan(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe materialization: expected 422, got %d: %s", w.Code, w.Body.String())
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
		for _, table := range []string{"role_source_audit_event", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_capability_version", "role_source_object_mapping", "role_source_artifact", "role_source_snapshot", "role_source_scan_request", "role_source"} {
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
		INSERT INTO role_source_artifact (
			workspace_id, digest, size_bytes, storage_key, uploaded_by_runtime_id,
			first_source_id, first_scan_request_id
		) VALUES ($1, $2, 7, $3, $4, $5, gen_random_uuid())
	`, workspaceID, digestA, "role-source-artifacts/"+workspaceID+"/"+strings.TrimPrefix(digestA, "sha256:"), runtimeID, sourceID)
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
		INSERT INTO role_source_plan_approval (id, source_id, workspace_id, plan_digest, request_key, decision, decisions, actor_user_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'delete-fixture', 'approved', '{}'::jsonb, $4)
	`, sourceID, workspaceID, digestB, testUserID)
	execFixture(`
		INSERT INTO role_source_apply (id, source_id, workspace_id, request_key, mode, snapshot_digest, plan_digest, status, actor_user_id)
		VALUES (gen_random_uuid(), $1, $2, 'delete-fixture', 'apply', $3, $4, 'pending', $5)
	`, sourceID, workspaceID, digestA, digestB, testUserID)
	execFixture(`
		INSERT INTO role_source_object_mapping (
			source_id, workspace_id, source_kind, source_object_id, target_kind, target_id,
			ownership_mask, last_applied_digest, last_snapshot_digest
		) VALUES ($1, $2, 'role', 'delete-role', 'agent', gen_random_uuid(), '["name"]'::jsonb, $3, $3)
	`, sourceID, workspaceID, digestA)
	execFixture(`
		INSERT INTO role_source_capability_version (
			workspace_id, source_id, capability_id, version, object_digest, definition, snapshot_digest
		) VALUES ($1, $2, 'delete-capability', '1.0.0', $3, '{}'::jsonb, $3)
	`, workspaceID, sourceID, digestA)
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
	for _, table := range []string{"role_source_audit_event", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_capability_version", "role_source_object_mapping", "role_source_artifact", "role_source_snapshot", "role_source_scan_request", "role_source"} {
		var count int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", workspaceID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows survived workspace delete: %d", table, count)
		}
	}
}
