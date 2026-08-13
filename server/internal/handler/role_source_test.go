package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	roleSourceTestHoldID    = "00000000-0000-4000-8000-000000000045"
)

type fakeRoleSourceControlPlane struct {
	registerInput             *rolesource.RegisterSourceInput
	registerRow               db.RoleSource
	registerErr               error
	lifecycleInput            *rolesource.UpdateSourceLifecycleInput
	lifecycleRow              db.RoleSource
	lifecycleErr              error
	requestRow                db.RoleSourceScanRequest
	requestCreated            bool
	requestErr                error
	requestKey                string
	getScanRow                db.RoleSourceScanRequest
	getScanErr                error
	getLatestScanRow          db.RoleSourceScanRequest
	getLatestScanErr          error
	listRows                  []db.RoleSource
	listErr                   error
	getSourceRow              db.RoleSource
	getSourceErr              error
	claimRow                  rolesource.ClaimedScan
	claimErr                  error
	claimCalls                int
	renewRow                  db.RoleSourceScanRequest
	renewErr                  error
	createPlanInput           *rolesource.CreatePlanInput
	createPlanRow             db.RoleSourcePlan
	createPlanErr             error
	rollbackPlanInput         *rolesource.CreateRollbackPlanInput
	rollbackPlanRow           db.RoleSourcePlan
	rollbackPlanErr           error
	approvalInput             *rolesource.RecordPlanApprovalInput
	approvalRow               db.RoleSourcePlanApproval
	approvalErr               error
	applyInput                *rolesource.ApplyPlanInput
	applyRow                  db.RoleSourceApply
	applyReceipt              rolesource.ApplyReceipt
	applyErr                  error
	secretTransferInput       *rolesource.RequestSecretTransferInput
	secretTransferRow         db.RoleSourceSecretTransfer
	secretTransferErr         error
	claimedSecretTransfer     rolesource.ClaimedSecretTransfer
	claimSecretTransferErr    error
	claimSecretTransferCalls  int
	reportSecretTransferInput *rolesource.ReportSecretTransferInput
	reportSecretTransferRow   db.RoleSourceSecretTransfer
	reportSecretTransferErr   error
	getPlanRow                db.RoleSourcePlan
	getPlanErr                error
	planImpact                rolesource.PlanImpact
	planImpactErr             error
	planRows                  []db.RoleSourcePlan
	planListErr               error
	applyHistory              []rolesource.ApplyHistoryItem
	applyHistoryErr           error
	applyFailures             []db.RoleSourceApplyFailure
	applyFailuresErr          error
	snapshotRows              []db.RoleSourceSnapshot
	snapshotListErr           error
	approvalRows              []db.RoleSourcePlanApproval
	approvalListErr           error
	createLegalHoldInput      *rolesource.CreateLegalHoldInput
	createLegalHoldRow        rolesource.LegalHold
	createLegalHoldErr        error
	releaseLegalHoldInput     *rolesource.ReleaseLegalHoldInput
	releaseLegalHoldRow       rolesource.LegalHold
	releaseLegalHoldErr       error
	legalHoldRows             []rolesource.LegalHold
	legalHoldListErr          error
	retentionPreview          rolesource.RetentionPreview
	retentionPreviewErr       error
	retentionPolicyInput      *rolesource.UpdateRetentionPolicyInput
	retentionPolicy           rolesource.RetentionPolicy
	retentionPolicyErr        error
	missingArtifacts          []rolesource.ArtifactRef
	missingErr                error
	storedArtifact            db.RoleSourceArtifact
	storedCreated             bool
	storeArtifactErr          error
	calls                     int
}

func (f *fakeRoleSourceControlPlane) ApplyPlan(_ context.Context, input rolesource.ApplyPlanInput) (db.RoleSourceApply, rolesource.ApplyReceipt, error) {
	f.calls++
	f.applyInput = &input
	return f.applyRow, f.applyReceipt, f.applyErr
}

func (f *fakeRoleSourceControlPlane) CreateRollbackPlan(_ context.Context, input rolesource.CreateRollbackPlanInput) (db.RoleSourcePlan, error) {
	f.calls++
	f.rollbackPlanInput = &input
	return f.rollbackPlanRow, f.rollbackPlanErr
}

func (f *fakeRoleSourceControlPlane) ListApplyHistory(context.Context, string, string, int32) ([]rolesource.ApplyHistoryItem, error) {
	f.calls++
	return f.applyHistory, f.applyHistoryErr
}

func (f *fakeRoleSourceControlPlane) ListApplyFailures(context.Context, string, string, int32) ([]db.RoleSourceApplyFailure, error) {
	f.calls++
	return f.applyFailures, f.applyFailuresErr
}

func (f *fakeRoleSourceControlPlane) RequestSecretTransfer(_ context.Context, input rolesource.RequestSecretTransferInput) (db.RoleSourceSecretTransfer, error) {
	f.calls++
	f.secretTransferInput = &input
	return f.secretTransferRow, f.secretTransferErr
}

func (f *fakeRoleSourceControlPlane) ClaimNextSecretTransfer(context.Context, string, time.Duration) (rolesource.ClaimedSecretTransfer, error) {
	f.calls++
	f.claimSecretTransferCalls++
	return f.claimedSecretTransfer, f.claimSecretTransferErr
}

func (f *fakeRoleSourceControlPlane) ReportSecretTransfer(_ context.Context, input rolesource.ReportSecretTransferInput) (db.RoleSourceSecretTransfer, error) {
	f.calls++
	f.reportSecretTransferInput = &input
	return f.reportSecretTransferRow, f.reportSecretTransferErr
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

func (f *fakeRoleSourceControlPlane) UpdateSourceLifecycle(_ context.Context, input rolesource.UpdateSourceLifecycleInput) (db.RoleSource, error) {
	f.calls++
	f.lifecycleInput = &input
	return f.lifecycleRow, f.lifecycleErr
}

func (f *fakeRoleSourceControlPlane) RequestScan(_ context.Context, _, _, _, requestKey string) (db.RoleSourceScanRequest, bool, error) {
	f.calls++
	f.requestKey = requestKey
	return f.requestRow, f.requestCreated, f.requestErr
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

func (f *fakeRoleSourceControlPlane) GetLatestScan(context.Context, string, string) (db.RoleSourceScanRequest, error) {
	f.calls++
	return f.getLatestScanRow, f.getLatestScanErr
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

func (f *fakeRoleSourceControlPlane) GetPlanImpact(context.Context, string, string, string) (rolesource.PlanImpact, error) {
	f.calls++
	return f.planImpact, f.planImpactErr
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

func (f *fakeRoleSourceControlPlane) CreateLegalHold(_ context.Context, input rolesource.CreateLegalHoldInput) (rolesource.LegalHold, error) {
	f.calls++
	f.createLegalHoldInput = &input
	return f.createLegalHoldRow, f.createLegalHoldErr
}

func (f *fakeRoleSourceControlPlane) ReleaseLegalHold(_ context.Context, input rolesource.ReleaseLegalHoldInput) (rolesource.LegalHold, error) {
	f.calls++
	f.releaseLegalHoldInput = &input
	return f.releaseLegalHoldRow, f.releaseLegalHoldErr
}

func (f *fakeRoleSourceControlPlane) ListLegalHolds(context.Context, string, string, int32) ([]rolesource.LegalHold, error) {
	f.calls++
	return f.legalHoldRows, f.legalHoldListErr
}

func (f *fakeRoleSourceControlPlane) GetRetentionPreview(context.Context, string, string) (rolesource.RetentionPreview, error) {
	f.calls++
	return f.retentionPreview, f.retentionPreviewErr
}

func (f *fakeRoleSourceControlPlane) UpdateRetentionPolicy(_ context.Context, input rolesource.UpdateRetentionPolicyInput) (rolesource.RetentionPolicy, error) {
	f.calls++
	f.retentionPolicyInput = &input
	return f.retentionPolicy, f.retentionPolicyErr
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
		WorkspaceID: util.MustParseUUID(testWorkspaceID), Status: "claimed", ExpectedAdapterVersion: "1.0.0",
		LeaseToken: util.MustParseUUID("00000000-0000-4000-8000-000000000044"), RequestedAt: now, ClaimedAt: now,
	}
}

func roleSourceTestLegalHold(active bool) rolesource.LegalHold {
	hold := rolesource.LegalHold{
		ID: roleSourceTestHoldID, WorkspaceID: testWorkspaceID, SourceID: roleSourceTestSourceID,
		Scope: rolesource.LegalHoldScopeSnapshot, SnapshotDigest: "sha256:" + strings.Repeat("a", 64),
		ReasonCode: rolesource.LegalHoldReasonLitigation, ReferenceDigest: "sha256:" + strings.Repeat("b", 64),
		CreatedBy: testUserID, CreatedAt: "2026-08-13T10:02:00Z",
	}
	if !active {
		hold.ReleaseReasonCode = rolesource.LegalHoldReleaseResolved
		hold.ReleaseReferenceDigest = "sha256:" + strings.Repeat("c", 64)
		hold.ReleasedBy = testUserID
		hold.ReleasedAt = "2026-08-13T11:02:00Z"
	}
	return hold
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
	if !strings.Contains(body, `"runtime_config":{"status":"unattested"`) {
		t.Fatalf("new source response omitted the explicit unattested runtime state: %s", body)
	}
}

func TestUpdateRoleSourceLifecycle_UsesVersionedStrictRequest(t *testing.T) {
	row := roleSourceTestRow()
	row.State = "paused"
	row.Version = 8
	fake := &fakeRoleSourceControlPlane{lifecycleRow: row}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
		"action": "pause", "expected_version": 7,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.UpdateRoleSourceLifecycle(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("lifecycle update status=%d body=%s", w.Code, w.Body.String())
	}
	if fake.lifecycleInput == nil || fake.lifecycleInput.Action != rolesource.SourceLifecyclePause ||
		fake.lifecycleInput.ExpectedVersion != 7 || fake.lifecycleInput.SourceID != roleSourceTestSourceID {
		t.Fatalf("lifecycle input=%+v", fake.lifecycleInput)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("lifecycle response must not return a partial runtime projection: %s", w.Body.String())
	}
}

func TestUpdateRoleSourceLifecycle_RejectsUnknownFields(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
		"action": "pause", "expected_version": 7, "force": true,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.UpdateRoleSourceLifecycle(w, req)

	if w.Code != http.StatusBadRequest || fake.calls != 0 {
		t.Fatalf("unknown lifecycle field status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestUpdateRoleSourceLifecycle_MapsSafeConflicts(t *testing.T) {
	for name, controlErr := range map[string]error{
		"version":     rolesource.ErrSourceVersionConflict,
		"transition":  rolesource.ErrInvalidLifecycleTransition,
		"runtime":     rolesource.ErrLifecycleRuntimeUnavailable,
		"attestation": rolesource.ErrLifecycleConfigNotLoaded,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeRoleSourceControlPlane{lifecycleErr: controlErr}
			h := roleSourceTestHandler(t, true, fake)
			w := httptest.NewRecorder()
			req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
				"action": "resume", "expected_version": 7,
			}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

			h.UpdateRoleSourceLifecycle(w, req)

			if w.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			for _, forbidden := range []string{"daemon_config_id", roleSourceTestRuntimeID, roleSourceTestSourceID} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("conflict response exposed %q: %s", forbidden, w.Body.String())
				}
			}
		})
	}
}

func TestCreateRoleSourceLegalHold_UsesStrictContentFreeRequest(t *testing.T) {
	hold := roleSourceTestLegalHold(true)
	fake := &fakeRoleSourceControlPlane{createLegalHoldRow: hold}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "hold-request-1", "scope": "snapshot", "snapshot_digest": hold.SnapshotDigest,
		"reason_code": "litigation", "reference_digest": hold.ReferenceDigest,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.CreateRoleSourceLegalHold(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create hold status=%d body=%s", w.Code, w.Body.String())
	}
	if fake.createLegalHoldInput == nil || fake.createLegalHoldInput.RequestKey != "hold-request-1" ||
		fake.createLegalHoldInput.Scope != rolesource.LegalHoldScopeSnapshot || fake.createLegalHoldInput.SnapshotDigest != hold.SnapshotDigest {
		t.Fatalf("create legal hold input=%+v", fake.createLegalHoldInput)
	}
	body := w.Body.String()
	for _, forbidden := range []string{"request_key", "hold-request-1", "case_number", "case_reference"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("legal hold response exposed %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"status":"active"`) || !strings.Contains(body, hold.ReferenceDigest) {
		t.Fatalf("legal hold response=%s", body)
	}
}

func TestCreateRoleSourceLegalHold_RejectsUnknownFieldsBeforeControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "hold-request-1", "scope": "source", "reason_code": "regulatory", "case_number": "secret-case",
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.CreateRoleSourceLegalHold(w, req)

	if w.Code != http.StatusBadRequest || fake.calls != 0 {
		t.Fatalf("unknown legal hold field status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestReleaseAndListRoleSourceLegalHolds_ReturnSafeState(t *testing.T) {
	released := roleSourceTestLegalHold(false)
	fake := &fakeRoleSourceControlPlane{releaseLegalHoldRow: released, legalHoldRows: []rolesource.LegalHold{released}}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "release-request-1", "reason_code": "resolved", "reference_digest": released.ReleaseReferenceDigest,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "holdId", roleSourceTestHoldID)

	h.ReleaseRoleSourceLegalHold(w, req)
	if w.Code != http.StatusOK || fake.releaseLegalHoldInput == nil || fake.releaseLegalHoldInput.HoldID != roleSourceTestHoldID {
		t.Fatalf("release hold status=%d input=%+v body=%s", w.Code, fake.releaseLegalHoldInput, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "release-request-1") || !strings.Contains(w.Body.String(), `"status":"released"`) {
		t.Fatalf("release response=%s", w.Body.String())
	}

	w = httptest.NewRecorder()
	req = withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored?limit=100", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)
	h.ListRoleSourceLegalHolds(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"legal_holds"`) || !strings.Contains(w.Body.String(), `"status":"released"`) {
		t.Fatalf("list holds status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRoleSourceLegalHoldMutation_MapsStateConflicts(t *testing.T) {
	for name, controlErr := range map[string]error{
		"idempotency": rolesource.ErrIdempotencyConflict,
		"released":    rolesource.ErrLegalHoldReleased,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeRoleSourceControlPlane{releaseLegalHoldErr: controlErr}
			h := roleSourceTestHandler(t, true, fake)
			w := httptest.NewRecorder()
			req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
				"request_key": "release-request-1", "reason_code": "resolved",
			}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "holdId", roleSourceTestHoldID)
			h.ReleaseRoleSourceLegalHold(w, req)
			if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), roleSourceTestHoldID) {
				t.Fatalf("hold conflict status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRoleSourceRetentionPreviewIsContentFreeAndBounded(t *testing.T) {
	preview := rolesource.RetentionPreview{
		Policy: rolesource.RetentionPolicy{
			WorkspaceID: testWorkspaceID, SourceID: roleSourceTestSourceID,
			Version: 2, Enabled: true, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
		},
		EligibleCount: 1, EstimatedBytes: 4096,
		Candidates: []rolesource.RetentionCandidatePreview{{
			SnapshotDigest: "sha256:" + strings.Repeat("a", 64), CreatedAt: "2026-01-01T00:00:00Z", EstimatedBytes: 4096,
		}},
	}
	fake := &fakeRoleSourceControlPlane{retentionPreview: preview}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)
	h.GetRoleSourceRetentionPreview(w, req)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"minimum_age_days":90`) || !strings.Contains(body, `"estimated_bytes":4096`) {
		t.Fatalf("retention preview status=%d body=%s", w.Code, body)
	}
	for _, forbidden := range []string{"manifest", "diagnostics", "source_evidence", "request_key", "storage_key", "artifact_body"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("retention preview exposed %q: %s", forbidden, body)
		}
	}
}

func TestUpdateRoleSourceRetentionPolicyUsesStrictCASRequest(t *testing.T) {
	policy := rolesource.RetentionPolicy{
		WorkspaceID: testWorkspaceID, SourceID: roleSourceTestSourceID,
		Version: 3, Enabled: true, MinimumAgeDays: 180, KeepSuccessfulSnapshots: 12,
	}
	fake := &fakeRoleSourceControlPlane{retentionPolicy: policy}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
		"request_key": "policy-3", "expected_version": 2, "enabled": true,
		"minimum_age_days": 180, "keep_successful_snapshots": 12,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)
	h.UpdateRoleSourceRetentionPolicy(w, req)
	if w.Code != http.StatusOK || fake.retentionPolicyInput == nil || fake.retentionPolicyInput.ExpectedVersion != 2 ||
		fake.retentionPolicyInput.MinimumAgeDays != 180 || fake.retentionPolicyInput.KeepSuccessfulSnapshots != 12 {
		t.Fatalf("retention update status=%d input=%+v body=%s", w.Code, fake.retentionPolicyInput, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "policy-3") || strings.Contains(w.Body.String(), "request_key") {
		t.Fatalf("retention response exposed idempotency key: %s", w.Body.String())
	}
}

func TestUpdateRoleSourceRetentionPolicyRejectsUnknownAndMapsConflict(t *testing.T) {
	t.Run("unknown field", func(t *testing.T) {
		fake := &fakeRoleSourceControlPlane{}
		h := roleSourceTestHandler(t, true, fake)
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
			"request_key": "policy", "expected_version": 0, "enabled": true,
			"minimum_age_days": 90, "keep_successful_snapshots": 10, "delete_immediately": true,
		}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)
		h.UpdateRoleSourceRetentionPolicy(w, req)
		if w.Code != http.StatusBadRequest || fake.calls != 0 {
			t.Fatalf("unknown retention field status=%d calls=%d", w.Code, fake.calls)
		}
	})
	t.Run("version conflict", func(t *testing.T) {
		fake := &fakeRoleSourceControlPlane{retentionPolicyErr: rolesource.ErrRetentionVersion}
		h := roleSourceTestHandler(t, true, fake)
		w := httptest.NewRecorder()
		req := withURLParams(newRequestAs(testUserID, http.MethodPatch, "/ignored", map[string]any{
			"request_key": "policy", "expected_version": 0, "enabled": false,
			"minimum_age_days": 90, "keep_successful_snapshots": 10,
		}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)
		h.UpdateRoleSourceRetentionPolicy(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("retention conflict status=%d body=%s", w.Code, w.Body.String())
		}
	})
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
	if !strings.Contains(w.Body.String(), "role_source_scan_already_active") {
		t.Fatalf("active scan response missing stable code: %s", w.Body.String())
	}
}

func TestRequestRoleSourceScan_MapsPausedOrDetachedSourceToConflict(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestErr: fmt.Errorf("%w: paused", rolesource.ErrScanSourceState)}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.RequestRoleSourceScan(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("paused source: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "role_source_scan_source_state") {
		t.Fatalf("paused source response missing stable code: %s", w.Body.String())
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

func TestGetLatestRoleSourceScan_ResponseDoesNotExposeLeaseOrRuntimeIdentity(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{getLatestScanRow: roleSourceTestScanRow()}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.GetLatestRoleSourceScan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get latest scan: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{"lease_token", "lease_expires_at", "claimed_by_runtime_id", "request_key_digest", "000000000044"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("latest scan response exposed %q: %s", forbidden, w.Body.String())
		}
	}
}

func TestGetLatestRoleSourceScan_MapsEmptyHistoryToNotFound(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{getLatestScanErr: pgx.ErrNoRows}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.GetLatestRoleSourceScan(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("empty latest scan: expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequestRoleSourceScan_WakesOwningRuntime(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestRow: roleSourceTestScanRow(), requestCreated: true, getSourceRow: roleSourceTestRow()}
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

func TestRequestRoleSourceScan_IdempotentReplayDoesNotWakeRuntimeAgain(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestRow: roleSourceTestScanRow(), requestCreated: false}
	h := roleSourceTestHandler(t, true, fake)
	recorder := &roleSourcePendingWorkRecorder{}
	h.DaemonPendingWork = recorder
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]string{
		"request_key": "role-source-scan-stable-key",
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.RequestRoleSourceScan(w, req)

	if w.Code != http.StatusOK || recorder.calls != 0 || fake.requestKey != "role-source-scan-stable-key" {
		t.Fatalf("idempotent replay status=%d wakeups=%d key=%q body=%s", w.Code, recorder.calls, fake.requestKey, w.Body.String())
	}
}

func TestRequestRoleSourceScan_LegacyEmptyBodyGetsBoundedServerKey(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{requestRow: roleSourceTestScanRow(), requestCreated: false}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.RequestRoleSourceScan(w, req)

	if !strings.HasPrefix(fake.requestKey, "legacy-role-source-scan-") || len(fake.requestKey) > 200 {
		t.Fatalf("legacy request key = %q", fake.requestKey)
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

func TestGetRoleSourcePlanImpact_ReturnsContentFreeOperationalEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	fake := &fakeRoleSourceControlPlane{planImpact: rolesource.PlanImpact{
		ContractVersion: rolesource.PlanImpactContractVersion,
		SourceID:        roleSourceTestSourceID, PlanDigest: digest,
		TargetSnapshotDigest: "sha256:" + strings.Repeat("b", 64),
		GeneratedAt:          "2026-08-13T00:00:00Z",
		Summary:              rolesource.PlanImpactSummary{CancelOnApply: 2},
		Tasks: []rolesource.PlanImpactTask{{
			TaskID:       "00000000-0000-4000-8000-000000000099",
			SourceRoleID: "researcher", AgentID: "00000000-0000-4000-8000-000000000098",
			Status: "queued", Effect: "cancel_on_apply", CreatedAt: "2026-08-13T00:00:00Z",
		}},
	}}
	h := roleSourceTestHandler(t, true, fake)
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", digest)
	w := httptest.NewRecorder()

	h.GetRoleSourcePlanImpact(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cancel_on_apply":2`) {
		t.Fatalf("impact response status=%d body=%s", w.Code, w.Body.String())
	}
	for _, forbidden := range []string{"prompt", "result", "context", "error"} {
		if strings.Contains(w.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("impact response exposed forbidden field %q: %s", forbidden, w.Body.String())
		}
	}
}

func TestGetRoleSourcePlanImpact_DefaultOffDoesNotReachControlPlane(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, false, fake)
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil),
		"id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", "sha256:"+strings.Repeat("a", 64))
	w := httptest.NewRecorder()

	h.GetRoleSourcePlanImpact(w, req)

	if w.Code != http.StatusNotFound || fake.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, fake.calls, w.Body.String())
	}
}

func TestCreateRoleSourceRollbackPlan_UsesApplyGateAndExactHistoricalTarget(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	fake := &fakeRoleSourceControlPlane{rollbackPlanRow: plan}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]string{
		"target_snapshot_digest": plan.ToSnapshotDigest,
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.CreateRoleSourceRollbackPlan(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create rollback plan: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if fake.rollbackPlanInput == nil || fake.rollbackPlanInput.TargetSnapshotDigest != plan.ToSnapshotDigest || fake.rollbackPlanInput.ActorUserID != testUserID {
		t.Fatalf("rollback plan input = %+v", fake.rollbackPlanInput)
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
	secretTransferID := "00000000-0000-4000-8000-000000000046"
	receipt := rolesource.ApplyReceipt{
		ContractVersion: rolesource.ApplyReceiptContractVersion, Mode: "apply", ApplyID: util.UUIDToString(applyID),
		SourceID: roleSourceTestSourceID, WorkspaceID: testWorkspaceID, SnapshotDigest: plan.ToSnapshotDigest,
		PlanDigest: plan.PlanDigest, ApprovalID: approvalID, Mappings: []rolesource.ApplyMapping{},
		SecretTransfers: []rolesource.SecretTransferReceipt{{RoleID: "writer", TransferID: secretTransferID, EnvelopeDigest: "sha256:" + strings.Repeat("d", 64)}},
	}
	fake := &fakeRoleSourceControlPlane{
		applyRow:     db.RoleSourceApply{ID: applyID, SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID), Mode: "apply", Status: "succeeded", ActorUserID: util.MustParseUUID(testUserID)},
		applyReceipt: receipt,
	}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]any{
		"request_key": "apply-once", "approval_id": approvalID, "secret_transfer_ids": map[string]string{"writer": secretTransferID},
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.ApplyRoleSourcePlan(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("apply plan: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if fake.applyInput == nil || fake.applyInput.RequestKey != "apply-once" || fake.applyInput.ApprovalID != approvalID || fake.applyInput.ActorUserID != testUserID || fake.applyInput.SecretTransferIDs["writer"] != secretTransferID {
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

func TestListRoleSourceApplyHistoryExposesProvenanceWithoutIdempotencyKeys(t *testing.T) {
	applyID := util.MustParseUUID("00000000-0000-4000-8000-000000000045")
	receipt := rolesource.ApplyReceipt{
		ContractVersion: rolesource.ApplyReceiptContractVersion, Mode: "rollback", ApplyID: util.UUIDToString(applyID),
		SourceID: roleSourceTestSourceID, WorkspaceID: testWorkspaceID,
		SnapshotDigest: "sha256:" + strings.Repeat("a", 64), FromSnapshotDigest: "sha256:" + strings.Repeat("b", 64),
		PlanDigest: "sha256:" + strings.Repeat("c", 64), ApprovalID: "00000000-0000-4000-8000-000000000044",
	}
	fake := &fakeRoleSourceControlPlane{applyHistory: []rolesource.ApplyHistoryItem{{
		Row: db.RoleSourceApply{
			ID: applyID, SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
			RequestKey: "never-expose-history-key", Mode: "rollback", Status: "succeeded", ActorUserID: util.MustParseUUID(testUserID),
		},
		Receipt: receipt,
	}}}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.ListRoleSourceApplyHistory(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"rollback"`) || !strings.Contains(w.Body.String(), `"actor_user_id":"`+testUserID+`"`) {
		t.Fatalf("apply history response: status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "never-expose-history-key") || strings.Contains(w.Body.String(), "request_key") {
		t.Fatalf("apply history exposed idempotency key: %s", w.Body.String())
	}
}

func TestListRoleSourceApplyFailuresExposesStableCodesWithoutCorrelationDigest(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{applyFailures: []db.RoleSourceApplyFailure{{
		ID:       util.MustParseUUID("00000000-0000-4000-8000-000000000045"),
		SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
		PlanDigest: "sha256:" + strings.Repeat("a", 64), ApprovalID: util.MustParseUUID("00000000-0000-4000-8000-000000000044"),
		ActorUserID: util.MustParseUUID(testUserID), RequestKeyDigest: "sha256:" + strings.Repeat("f", 64),
		Mode: "apply", FailureStage: "materialization", FailureCode: "state_conflict",
		OccurredAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC), Valid: true},
	}}}
	h := roleSourceTestHandler(t, true, fake)
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodGet, "/ignored", nil), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID)

	h.ListRoleSourceApplyFailures(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"failure_stage":"materialization"`) || !strings.Contains(w.Body.String(), `"failure_code":"state_conflict"`) {
		t.Fatalf("apply failure response: status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), strings.Repeat("f", 64)) || strings.Contains(w.Body.String(), "request_key") || strings.Contains(w.Body.String(), "raw_error") {
		t.Fatalf("apply failure response exposed internal correlation or raw detail: %s", w.Body.String())
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

func TestRequestRoleSourceSecretTransfer_ReturnsOnlyPublicChallengeAndWakesRuntime(t *testing.T) {
	plan := roleSourceTestPlanRow(t)
	transferID := util.MustParseUUID("00000000-0000-4000-8000-000000000046")
	approvalID := util.MustParseUUID("00000000-0000-4000-8000-000000000044")
	expiresAt := time.Date(2026, 8, 13, 10, 15, 0, 0, time.UTC)
	claims := rolesource.SecretEnvelopeClaims{
		ContractVersion: rolesource.SecretEnvelopeContractVersion, TransferID: util.UUIDToString(transferID),
		WorkspaceID: testWorkspaceID, SourceID: roleSourceTestSourceID, RoleID: "writer",
		SnapshotDigest: plan.ToSnapshotDigest, ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}
	claimsBody, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRoleSourceControlPlane{secretTransferRow: db.RoleSourceSecretTransfer{
		ID: transferID, WorkspaceID: util.MustParseUUID(testWorkspaceID), SourceID: util.MustParseUUID(roleSourceTestSourceID),
		RuntimeID: util.MustParseUUID(roleSourceTestRuntimeID), PlanDigest: plan.PlanDigest, ApprovalID: approvalID,
		SnapshotDigest: plan.ToSnapshotDigest, RoleID: "writer", RequestKey: "do-not-expose-request-key", Status: "pending",
		PublicKey: strings.Repeat("A", 43), PrivateKeyCiphertext: []byte("do-not-expose-private-key"), KeyID: "do-not-expose-key-id",
		Claims: claimsBody, ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}}
	h := roleSourceTestHandler(t, true, fake)
	recorder := &roleSourcePendingWorkRecorder{}
	h.DaemonPendingWork = recorder
	w := httptest.NewRecorder()
	req := withURLParams(newRequestAs(testUserID, http.MethodPost, "/ignored", map[string]string{
		"request_key": "do-not-expose-request-key", "approval_id": util.UUIDToString(approvalID), "role_id": "writer",
	}), "id", testWorkspaceID, "sourceId", roleSourceTestSourceID, "planDigest", plan.PlanDigest)

	h.RequestRoleSourceSecretTransfer(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("request secret transfer: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if fake.secretTransferInput == nil || fake.secretTransferInput.RoleID != "writer" || fake.secretTransferInput.ActorUserID != testUserID {
		t.Fatalf("secret transfer input = %+v", fake.secretTransferInput)
	}
	for _, forbidden := range []string{"request_key", "do-not-expose-request-key", "private_key", "do-not-expose-private-key", "key_id", "do-not-expose-key-id", "envelope"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("secret transfer response exposed %q: %s", forbidden, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), `"public_key"`) || !strings.Contains(w.Body.String(), plan.ToSnapshotDigest) {
		t.Fatalf("secret transfer response omitted public challenge: %s", w.Body.String())
	}
	if recorder.calls != 1 || recorder.runtimeID != roleSourceTestRuntimeID || recorder.kind != protocol.PendingWorkKindRoleSourceSecretTransfer {
		t.Fatalf("secret transfer wakeup = %+v", recorder)
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

func TestPopulateRoleSourceSecretTransferHeartbeat_RequiresIndependentNegotiation(t *testing.T) {
	fake := &fakeRoleSourceControlPlane{}
	h := roleSourceTestHandler(t, true, fake)
	ack := &protocol.DaemonHeartbeatAckPayload{}

	h.populateRoleSourceSecretTransferHeartbeat(context.Background(), ack, roleSourceTestRuntimeID, testWorkspaceID, false, true)
	if fake.claimSecretTransferCalls != 0 || len(ack.ServerCapabilities) != 0 {
		t.Fatalf("unsupported daemon claimed secret work: calls=%d capabilities=%v", fake.claimSecretTransferCalls, ack.ServerCapabilities)
	}
	h.populateRoleSourceSecretTransferHeartbeat(context.Background(), ack, roleSourceTestRuntimeID, testWorkspaceID, true, false)
	if fake.claimSecretTransferCalls != 0 || len(ack.ServerCapabilities) != 1 || ack.ServerCapabilities[0] != protocol.DaemonCapabilityRoleSourceSecretTransferV1 {
		t.Fatalf("capability-only secret heartbeat = calls:%d capabilities:%v", fake.claimSecretTransferCalls, ack.ServerCapabilities)
	}

	transferID := util.MustParseUUID("00000000-0000-4000-8000-000000000046")
	leaseToken := util.MustParseUUID("00000000-0000-4000-8000-000000000047")
	expiresAt := time.Date(2026, 8, 13, 10, 15, 0, 0, time.UTC)
	fake.claimedSecretTransfer = rolesource.ClaimedSecretTransfer{
		Transfer: db.RoleSourceSecretTransfer{
			ID: transferID, SourceID: util.MustParseUUID(roleSourceTestSourceID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
			RoleID: "writer", SnapshotDigest: "sha256:" + strings.Repeat("c", 64), PublicKey: strings.Repeat("A", 43),
			LeaseToken: leaseToken, LeaseExpiresAt: pgtype.Timestamptz{Time: expiresAt.Add(-time.Minute), Valid: true},
		},
		Source: roleSourceTestRow(),
		Claims: rolesource.SecretEnvelopeClaims{
			ContractVersion: rolesource.SecretEnvelopeContractVersion, TransferID: util.UUIDToString(transferID),
			WorkspaceID: testWorkspaceID, SourceID: roleSourceTestSourceID, RoleID: "writer",
			SnapshotDigest: "sha256:" + strings.Repeat("c", 64), ExpiresAt: expiresAt.Format(time.RFC3339Nano),
		},
	}
	h.populateRoleSourceSecretTransferHeartbeat(context.Background(), ack, roleSourceTestRuntimeID, testWorkspaceID, true, true)
	if fake.claimSecretTransferCalls != 1 || ack.PendingRoleSourceSecretTransfer == nil {
		t.Fatalf("secret poll did not claim work: calls=%d pending=%+v", fake.claimSecretTransferCalls, ack.PendingRoleSourceSecretTransfer)
	}
	if pending := ack.PendingRoleSourceSecretTransfer; pending.TransferID != util.UUIDToString(transferID) || pending.DaemonConfigID != "local-secret-config-handle" || pending.PublicKey == "" || pending.LeaseToken == "" {
		t.Fatalf("pending secret transfer = %+v", pending)
	}
}

func TestDeleteWorkspace_RemovesEntireRoleSourceGraph(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var workspaceID, runtimeID, sourceID, artifactStorageKey string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description)
		VALUES ('Role Source Delete Test', 'role-source-delete-' || gen_random_uuid()::text, '')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		if artifactStorageKey != "" {
			_, _ = testPool.Exec(context.Background(), "DELETE FROM role_source_artifact_delete_intent WHERE storage_key = $1", artifactStorageKey)
		}
		for _, table := range []string{"role_source_audit_event", "role_source_secret_transfer", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_capability_version", "role_source_object_mapping", "role_source_snapshot_artifact", "role_source_artifact", "role_source_snapshot", "role_source_scan_request", "role_source"} {
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
	artifactStorageKey = "role-source-artifacts/" + workspaceID + "/" + strings.TrimPrefix(digestA, "sha256:")
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
	`, workspaceID, digestA, artifactStorageKey, runtimeID, sourceID)
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
	var approvalID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO role_source_plan_approval (id, source_id, workspace_id, plan_digest, request_key, decision, decisions, actor_user_id)
		VALUES (gen_random_uuid(), $1, $2, $3, 'delete-secret-fixture', 'approved', '{}'::jsonb, $4)
		RETURNING id
	`, sourceID, workspaceID, digestB, testUserID).Scan(&approvalID); err != nil {
		t.Fatalf("create secret transfer approval: %v", err)
	}
	execFixture(`
		INSERT INTO role_source_secret_transfer (
			id, workspace_id, source_id, runtime_id, plan_digest, approval_id, snapshot_digest,
			role_id, request_key, public_key, private_key_ciphertext, key_id, claims, expires_at, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, $3, $4, $5, $6,
			'delete-role', 'delete-secret-fixture', repeat('A', 43), convert_to(repeat('x', 60), 'UTF8'), 'v1', '{}'::jsonb, now() + interval '15 minutes', $7
		)
	`, workspaceID, sourceID, runtimeID, digestB, approvalID, digestA, testUserID)
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
	for _, table := range []string{"role_source_audit_event", "role_source_secret_transfer", "role_source_plan_approval", "role_source_apply", "role_source_plan", "role_source_capability_version", "role_source_object_mapping", "role_source_snapshot_artifact", "role_source_artifact", "role_source_snapshot", "role_source_scan_request", "role_source"} {
		var count int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", workspaceID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows survived workspace delete: %d", table, count)
		}
	}
	var deleteIntentCount int
	if err := testPool.QueryRow(ctx, "SELECT count(*) FROM role_source_artifact_delete_intent WHERE storage_key = $1", artifactStorageKey).Scan(&deleteIntentCount); err != nil {
		t.Fatal(err)
	}
	if deleteIntentCount != 1 {
		t.Fatalf("workspace delete artifact intent count = %d, want 1", deleteIntentCount)
	}
}

func TestDeleteWorkspace_ActiveRoleSourceLegalHoldIsHardFence(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var workspaceID, runtimeID, sourceID, holdID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description)
		VALUES ('Legal Hold Delete Fence', 'legal-hold-delete-' || gen_random_uuid()::text, '')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			INSERT INTO role_source_legal_hold_release (
				hold_id, workspace_id, source_id, request_key_digest, reason_code, released_by
			)
			SELECT id, workspace_id, source_id, 'sha256:' || repeat('0', 64), 'entered_in_error', $2
			FROM role_source_legal_hold WHERE workspace_id = $1
			ON CONFLICT (hold_id) DO NOTHING
		`, workspaceID, testUserID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM role_source_legal_hold WHERE workspace_id = $1", workspaceID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM role_source_legal_hold_release WHERE workspace_id = $1", workspaceID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM role_source WHERE workspace_id = $1", workspaceID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM agent_runtime WHERE workspace_id = $1", workspaceID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM member WHERE workspace_id = $1", workspaceID)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM workspace WHERE id = $1", workspaceID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, workspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id)
		VALUES ($1, 'legal-hold-runtime', 'cloud', 'handler_test_runtime', 'online', 'fixture', '{}'::jsonb, $2)
		RETURNING id
	`, workspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO role_source (
			id, workspace_id, runtime_id, name, kind, adapter_version, daemon_config_id,
			config_redacted, policy, created_by, updated_by
		) VALUES (
			gen_random_uuid(), $1, $2, 'legal hold fixture', 'agentwaker_directory', '0.1.0', 'fixture',
			'{"configured":true}'::jsonb, '{}'::jsonb, $3, $3
		) RETURNING id
	`, workspaceID, runtimeID, testUserID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO role_source_legal_hold (
			id, workspace_id, source_id, request_key_digest, scope, reason_code, reference_digest, created_by
		) VALUES (
			gen_random_uuid(), $1, $2, $3, 'source', 'regulatory', $4, $5
		) RETURNING id
	`, workspaceID, sourceID, "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64), testUserID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}

	deleteWorkspace := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := withURLParam(newRequestAs(testUserID, http.MethodDelete, "/api/workspaces/"+workspaceID, nil), "id", workspaceID)
		testHandler.DeleteWorkspace(recorder, request)
		return recorder
	}
	blocked := deleteWorkspace()
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "active legal holds") {
		t.Fatalf("held workspace delete status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	for table, idColumn := range map[string]string{"workspace": "id", "role_source": "id", "role_source_legal_hold": "id"} {
		id := workspaceID
		if table == "role_source" {
			id = sourceID
		} else if table == "role_source_legal_hold" {
			id = holdID
		}
		var count int
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+idColumn+" = $1", id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s after blocked delete count=%d err=%v", table, count, err)
		}
	}
	if _, err := testPool.Exec(ctx, "DELETE FROM role_source_legal_hold WHERE id = $1", holdID); err == nil {
		t.Fatal("database allowed direct deletion of an active legal hold")
	}
	var holdCount int
	if err := testPool.QueryRow(ctx, "SELECT count(*) FROM role_source_legal_hold WHERE id = $1", holdID).Scan(&holdCount); err != nil || holdCount != 1 {
		t.Fatalf("active hold after direct delete attempt count=%d err=%v", holdCount, err)
	}

	if _, err := testPool.Exec(ctx, `
		INSERT INTO role_source_legal_hold_release (
			hold_id, workspace_id, source_id, request_key_digest, reason_code, released_by
		) VALUES ($1, $2, $3, $4, 'court_order', $5)
	`, holdID, workspaceID, sourceID, "sha256:"+strings.Repeat("e", 64), testUserID); err != nil {
		t.Fatal(err)
	}
	released := deleteWorkspace()
	if released.Code != http.StatusNoContent {
		t.Fatalf("released workspace delete status=%d body=%s", released.Code, released.Body.String())
	}
	for _, table := range []string{"workspace", "role_source", "role_source_legal_hold", "role_source_legal_hold_release"} {
		var count int
		column := "workspace_id"
		if table == "workspace" {
			column = "id"
		}
		if err := testPool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+column+" = $1", workspaceID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s after released delete count=%d err=%v", table, count, err)
		}
	}
}
