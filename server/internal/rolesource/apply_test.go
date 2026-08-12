package rolesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestClassifyApplyFailureUsesStableContentFreeCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "cancelled", err: context.Canceled, want: "request_cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline_exceeded"},
		{name: "capacity", err: ErrMaterializationOverload, want: "capacity_exhausted"},
		{name: "blocked", err: ErrMaterializationBlocked, want: "materialization_blocked"},
		{name: "apply conflict", err: ErrApplyConflict, want: "state_conflict"},
		{name: "idempotency conflict", err: ErrIdempotencyConflict, want: "state_conflict"},
		{name: "invalid request", err: ErrInvalidApplyRequest, want: "invalid_request"},
		{name: "invalid secret transfer", err: ErrInvalidSecretEnvelope, want: "invalid_secret_transfer"},
		{name: "expired secret transfer", err: ErrExpiredSecretEnvelope, want: "invalid_secret_transfer"},
		{name: "secret store unavailable", err: ErrSecretStoreUnavailable, want: "dependency_unavailable"},
		{name: "resource not found", err: pgx.ErrNoRows, want: "resource_not_found"},
		{name: "wrapped", err: errors.Join(errors.New("private detail"), ErrApplyConflict), want: "state_conflict"},
		{name: "unknown", err: errors.New("private database detail"), want: "internal_failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyApplyFailure(test.err); got != test.want {
				t.Fatalf("classifyApplyFailure()=%q, want %q", got, test.want)
			}
		})
	}
}

func TestNewApplyFailureParamsContainsOnlyBoundedAuditMetadata(t *testing.T) {
	id := util.MustParseUUID("00000000-0000-4000-8000-000000000040")
	workspaceID := util.MustParseUUID("00000000-0000-4000-8000-000000000041")
	sourceID := util.MustParseUUID("00000000-0000-4000-8000-000000000042")
	approvalID := util.MustParseUUID("00000000-0000-4000-8000-000000000043")
	actorID := util.MustParseUUID("00000000-0000-4000-8000-000000000044")
	occurredAt := time.Date(2026, 8, 13, 9, 30, 0, 123, time.FixedZone("CST", 8*60*60))
	tracker := applyAttemptTracker{
		recordable: true, workspaceID: workspaceID, sourceID: sourceID, approvalID: approvalID, actorID: actorID,
		planDigest: testSHA256("p"), requestKeyDigest: testSHA256("r"), mode: "rollback", stage: "materialization",
	}
	params := newApplyFailureParams(id, tracker, "state_conflict", occurredAt)
	if params.ID != id || params.WorkspaceID != workspaceID || params.SourceID != sourceID || params.ApprovalID != approvalID || params.ActorUserID != actorID {
		t.Fatalf("identity metadata=%+v", params)
	}
	if params.PlanDigest != tracker.planDigest || params.RequestKeyDigest != tracker.requestKeyDigest || params.Mode != "rollback" || params.FailureStage != "materialization" || params.FailureCode != "state_conflict" {
		t.Fatalf("audit metadata=%+v", params)
	}
	if !params.OccurredAt.Valid || !params.OccurredAt.Time.Equal(occurredAt) || params.OccurredAt.Time.Location() != time.UTC {
		t.Fatalf("occurred_at=%+v", params.OccurredAt)
	}
}

func TestValidateMaterializationScopeAcceptsSafeSourceNeutralObjects(t *testing.T) {
	manifest := planTestManifest()
	manifest.Capabilities = []Capability{{
		ID: "browser", Name: "Browser", Version: "1.0.0", Entrypoint: testArtifact("capabilities/browser/SKILL.md"),
	}}
	manifest.Roles[0].Automations = []Automation{{
		ID: "daily", Name: "Daily", Schedule: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: testArtifact("automations/daily.md"),
	}}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); err != nil {
		t.Fatalf("safe materialization rejected: %v", err)
	}
}

func TestValidateMaterializationScopeBlocksRoleProfileWithoutTargetContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"profile", func(manifest *Manifest) {
			value := testArtifact("roles/writer/profile.md")
			manifest.Roles[0].Profile = &value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := planTestManifest()
			test.mutate(&manifest)
			snapshot := planTestSnapshot(t, manifest)
			plan, err := BuildPlan("source-1", nil, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); !errors.Is(err, ErrMaterializationBlocked) {
				t.Fatalf("scope error = %v, want materialization blocked", err)
			}
		})
	}
}

func TestValidateMaterializationScopeAcceptsCapabilityBindingsAndOwnedSkillFiles(t *testing.T) {
	manifest := planTestManifest()
	manifest.Capabilities = []Capability{{ID: "browser", Name: "Browser", Version: "1.0.0", Profiles: []string{"default"}, PermissionModes: []string{"read-only"}, Entrypoint: testArtifact("capability.md")}}
	manifest.Roles[0].CapabilityBindings = []CapabilityBinding{{CapabilityID: "browser", SkillID: "draft", Profile: "default", VersionConstraint: "^1.0.0", PermissionMode: "read-only"}}
	manifest.Roles[0].Skills[0].Artifacts = []ArtifactRef{testArtifact("helper.txt")}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); err != nil {
		t.Fatalf("capability materialization rejected: %v", err)
	}
}

func TestValidateMaterializationScopeRejectsUnenforcedCapabilityAuthority(t *testing.T) {
	for _, test := range []struct {
		name       string
		permission string
		adapter    bool
	}{
		{name: "external write", permission: "external-write"},
		{name: "required adapter", permission: "read-only", adapter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := planTestManifest()
			capability := Capability{ID: "browser", Name: "Browser", Version: "1.0.0", Profiles: []string{"default"}, PermissionModes: []string{test.permission}, Entrypoint: testArtifact("capability.md")}
			if test.adapter {
				capability.Requirements.Adapters = []AdapterRequirement{{ID: "browser-driver", Required: true}}
			}
			manifest.Capabilities = []Capability{capability}
			manifest.Roles[0].CapabilityBindings = []CapabilityBinding{{CapabilityID: "browser", SkillID: "draft", Profile: "default", VersionConstraint: "^1.0.0", PermissionMode: test.permission}}
			snapshot := planTestSnapshot(t, manifest)
			plan, err := BuildPlan("source-1", nil, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); !errors.Is(err, ErrMaterializationBlocked) {
				t.Fatalf("scope error=%v, want blocked", err)
			}
		})
	}
}

func TestCapabilityBundleFilesCarriesImmutableRuntimeProof(t *testing.T) {
	entrypoint := testArtifact("capabilities/browser/SKILL.md")
	supporting := testArtifact("capabilities/browser/schema.json")
	supporting.Digest = testSHA256("b")
	state := materializationState{artifacts: map[string]verifiedArtifact{
		entrypoint.Digest: {ref: entrypoint, body: "0123456789"},
		supporting.Digest: {ref: supporting, body: "abcdefghij"},
	}}
	binding := CapabilityBinding{CapabilityID: "browser", SkillID: "draft", Profile: "default", VersionConstraint: "^1.0.0", PermissionMode: "read-only", Required: true, Fallback: "blocked"}
	files, err := state.capabilityBundleFiles(Capability{ID: "browser", Version: "1.2.3", Entrypoint: entrypoint, Artifacts: []ArtifactRef{supporting}}, binding, testSHA256("c"))
	if err != nil {
		t.Fatal(err)
	}
	markerPath := protocol.RoleSourceCapabilityMarkerPath("browser", "draft", "default")
	var marker protocol.RoleSourceCapabilityBundle
	if err := json.Unmarshal([]byte(files[markerPath]), &marker); err != nil {
		t.Fatal(err)
	}
	if marker.ContractVersion != protocol.RoleSourceCapabilityBundleContractV1 || marker.ObjectDigest != testSHA256("c") || marker.Fallback != "blocked" || len(marker.Files) != 2 || marker.EntrypointPath == "" {
		t.Fatalf("capability marker=%+v", marker)
	}
	if _, ok := files[marker.EntrypointPath]; !ok {
		t.Fatalf("entrypoint %q is not materialized", marker.EntrypointPath)
	}
}

func TestValidateMaterializationScopeAcceptsSecureEnvironmentAndMCP(t *testing.T) {
	manifest := planTestManifest()
	manifest.Roles[0].Environment = []EnvironmentKey{{Name: "TOKEN", Secret: true, Required: true, Configured: true, ValueDigest: "hmac-sha256:" + strings.Repeat("a", 64)}}
	manifest.Roles[0].MCP = []MCPServer{{ID: "browser", DefinitionHash: testSHA256("a")}}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); err != nil {
		t.Fatalf("secure role fields rejected: %v", err)
	}
}

func TestValidateMaterializationScopeRequiresRetainForSkillRemoval(t *testing.T) {
	base := planTestSnapshot(t, planTestManifest())
	targetManifest := planTestManifest()
	targetManifest.Roles[0].Skills = []Skill{}
	target := planTestSnapshot(t, targetManifest)
	plan, err := BuildPlan("source-1", &base, target)
	if err != nil {
		t.Fatal(err)
	}
	ref := ObjectRef{Kind: "skill", ParentID: "writer", ID: "draft"}
	if err := validateMaterializationScope(target, plan, map[string]ArchiveDecision{objectKey(ref): ArchiveDecisionArchive}); !errors.Is(err, ErrMaterializationBlocked) {
		t.Fatalf("archive skill error = %v", err)
	}
	if err := validateMaterializationScope(target, plan, map[string]ArchiveDecision{objectKey(ref): ArchiveDecisionRetain}); err != nil {
		t.Fatalf("retain skill rejected: %v", err)
	}
}

func TestApplyReceiptDigestDetectsTampering(t *testing.T) {
	applyID := util.MustParseUUID("00000000-0000-4000-8000-000000000045")
	receipt := ApplyReceipt{
		ContractVersion: ApplyReceiptContractVersion, Mode: "apply", ApplyID: util.UUIDToString(applyID),
		SourceID: "00000000-0000-4000-8000-000000000042", WorkspaceID: "00000000-0000-4000-8000-000000000001",
		SnapshotDigest: testSHA256("s"), PlanDigest: testSHA256("p"), ApprovalID: "00000000-0000-4000-8000-000000000044",
		Mappings: []ApplyMapping{},
	}
	_, digest, err := encodeApplyReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptDigest = digest
	body, _, err := encodeApplyReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	row := db.RoleSourceApply{
		ID: applyID, SourceID: util.MustParseUUID(receipt.SourceID), WorkspaceID: util.MustParseUUID(receipt.WorkspaceID),
		Mode: "apply", ActorUserID: util.MustParseUUID("00000000-0000-4000-8000-000000000003"),
		Status: "succeeded", SnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest,
		ReceiptDigest: pgtype.Text{String: digest, Valid: true}, Receipt: body,
	}
	if _, err := decodeApplyReceipt(row); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	plan := Plan{ToSnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest}
	input := ApplyPlanInput{SourceID: receipt.SourceID, WorkspaceID: receipt.WorkspaceID, ApprovalID: receipt.ApprovalID}
	if _, err := matchIdempotentApply(row, plan, input, row.ActorUserID); err != nil {
		t.Fatalf("exact retry rejected: %v", err)
	}
	input.ApprovalID = "00000000-0000-4000-8000-000000000099"
	if _, err := matchIdempotentApply(row, plan, input, row.ActorUserID); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	var tampered map[string]any
	if err := json.Unmarshal(body, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["counts"] = map[string]any{"created": 99}
	row.Receipt, _ = json.Marshal(tampered)
	if _, err := decodeApplyReceipt(row); err == nil {
		t.Fatal("tampered receipt accepted")
	}
}

func TestValidateRoleSecretPayloadRequiresExactSnapshotSetsAndMCPHash(t *testing.T) {
	definition := json.RawMessage(`{"command":"safe-mcp","env":{"TOKEN":"${TOKEN}"}}`)
	var value any
	if err := json.Unmarshal(definition, &value); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(value)
	sum := sha256.Sum256(canonical)
	role := Role{
		ID: "writer",
		Environment: []EnvironmentKey{
			{Name: "TOKEN", Configured: true}, {Name: "OPTIONAL", Configured: false},
		},
		MCP: []MCPServer{{ID: "safe", DefinitionHash: "sha256:" + hex.EncodeToString(sum[:])}},
	}
	payload := SecretEnvelopePayload{
		Environment: map[string]string{"TOKEN": "secret"},
		MCPServers:  map[string]json.RawMessage{"safe": definition},
	}
	if err := validateRoleSecretPayload(role, payload); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	payload.Environment["EXTRA"] = "exfiltration"
	if err := validateRoleSecretPayload(role, payload); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("extra environment error = %v", err)
	}
	delete(payload.Environment, "EXTRA")
	payload.MCPServers["safe"] = json.RawMessage(`{"command":"changed"}`)
	if err := validateRoleSecretPayload(role, payload); !errors.Is(err, ErrInvalidSecretEnvelope) {
		t.Fatalf("changed MCP error = %v", err)
	}
}

func TestSnapshotCASMatchesNullAndExactDigestOnly(t *testing.T) {
	digest := testSHA256("a")
	if !snapshotCASMatches(pgtype.Text{}, "") || !snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, digest) {
		t.Fatal("valid snapshot CAS did not match")
	}
	if snapshotCASMatches(pgtype.Text{}, digest) || snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, "") || snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, testSHA256("b")) {
		t.Fatal("stale snapshot CAS matched")
	}
}

func TestRetainedArchiveCandidateKeepsLastContainingSnapshot(t *testing.T) {
	ref := ObjectRef{Kind: "role", ID: "writer"}
	lastSnapshot := testSHA256("last-containing-snapshot")
	state := materializationState{
		receipt: &ApplyReceipt{},
		mappings: map[string]db.RoleSourceObjectMapping{
			objectKey(ref): {
				TargetKind: "agent", TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000009"),
				LastSnapshotDigest: lastSnapshot,
			},
		},
	}
	if err := state.appendExistingMapping(ref); err != nil {
		t.Fatal(err)
	}
	if state.mappings[objectKey(ref)].LastSnapshotDigest != lastSnapshot {
		t.Fatal("retained removed object advanced to a snapshot that cannot contain it")
	}
	if len(state.receipt.Mappings) != 1 || state.receipt.Mappings[0].Source != ref {
		t.Fatalf("retained mapping receipt = %+v", state.receipt.Mappings)
	}
}

func TestMappingMutationsAreStagedAndDeduplicatedBeforeBatchFlush(t *testing.T) {
	ref := ObjectRef{Kind: "role", ID: "writer"}
	targetID := util.MustParseUUID("00000000-0000-4000-8000-000000000019")
	state := materializationState{
		workspaceID:     util.MustParseUUID("00000000-0000-4000-8000-000000000001"),
		source:          db.RoleSource{ID: util.MustParseUUID("00000000-0000-4000-8000-000000000002")},
		snapshot:        Snapshot{SnapshotDigest: testSHA256("a")},
		mappings:        map[string]db.RoleSourceObjectMapping{},
		pendingMappings: map[string]pendingRoleSourceMapping{},
		receipt:         &ApplyReceipt{},
	}
	if err := state.upsertMapping(context.Background(), ref, "agent", targetID, testSHA256("b"), []string{"instructions"}, pgtype.Timestamptz{}); err != nil {
		t.Fatal(err)
	}
	if len(state.pendingMappings) != 1 || len(state.receipt.Mappings) != 1 || state.mappings[objectKey(ref)].TargetID != targetID {
		t.Fatalf("staged mapping state: pending=%d receipt=%d row=%+v", len(state.pendingMappings), len(state.receipt.Mappings), state.mappings[objectKey(ref)])
	}
	if err := state.upsertMapping(context.Background(), ref, "agent", targetID, testSHA256("b"), []string{"instructions"}, pgtype.Timestamptz{}); !errors.Is(err, ErrApplyConflict) {
		t.Fatalf("duplicate staged mapping error=%v", err)
	}
}

func TestApplyPreflightsObjectStorageBeforeMutationLocks(t *testing.T) {
	body, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	preflightAt := strings.Index(source, "c.preflightApply(ctx")
	beginAt := strings.Index(source, "c.database.Begin(ctx)")
	if preflightAt < 0 || beginAt < 0 || preflightAt > beginAt {
		t.Fatal("artifact preflight must complete before the apply transaction begins")
	}
	transactionSection := source[beginAt:]
	if nextFunction := strings.Index(transactionSection, "\nfunc "); nextFunction >= 0 {
		transactionSection = transactionSection[:nextFunction]
	}
	if strings.Contains(transactionSection, "readAndVerifyArtifacts") {
		t.Fatal("apply transaction performs object-storage reads while mutation locks are held")
	}

	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queryText := string(queries)
	if !strings.Contains(queryText, "ListRoleSourceArtifactsForApplyByDigests") || !strings.Contains(queryText, "FOR SHARE;") {
		t.Fatal("apply must recheck and share-lock the preflight artifact ledger in one batch")
	}
	if !strings.Contains(queryText, "UpsertRoleSourceObjectMappings") || !strings.Contains(queryText, "jsonb_to_recordset(@mappings::jsonb)") {
		t.Fatal("large applies must flush mapping mutations through one typed recordset")
	}
}

func TestApplyFailureAuditRunsAfterInnerTransactionReturns(t *testing.T) {
	body, err := os.ReadFile("apply.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	wrapperAt := strings.Index(source, "func (c *ControlPlane) ApplyPlan(")
	innerAt := strings.Index(source, "func (c *ControlPlane) applyPlan(")
	if wrapperAt < 0 || innerAt < 0 || wrapperAt >= innerAt {
		t.Fatal("apply wrapper or inner transaction function is missing")
	}
	wrapper := source[wrapperAt:innerAt]
	applyAt := strings.Index(wrapper, "c.applyPlan(ctx, input, &tracker)")
	recordAt := strings.Index(wrapper, "c.recordApplyFailure(ctx, tracker, failureCode)")
	if applyAt < 0 || recordAt < 0 || applyAt >= recordAt {
		t.Fatal("failed-attempt evidence must be recorded only after the inner apply call returns")
	}
	inner := source[innerAt:]
	beginAt := strings.Index(inner, "c.database.Begin(ctx)")
	rollbackAt := strings.Index(inner, "defer tx.Rollback(ctx)")
	if beginAt < 0 || rollbackAt < 0 || beginAt >= rollbackAt {
		t.Fatal("inner apply must install transaction rollback before mutation work")
	}
}

func TestMaterializationQueriesPreserveUserManagedFields(t *testing.T) {
	tests := []struct {
		file      string
		query     string
		forbidden []string
	}{
		{"agent.sql", "UpdateRoleSourceAgent", []string{"custom_env =", "mcp_config =", "model =", "permission_mode =", "status =", "archived_at ="}},
		{"autopilot.sql", "UpdateRoleSourceAutopilot", []string{"status =", "enabled =", "assignee_id =", "execution_mode ="}},
		{"skill.sql", "UpdateRoleSourceSkill", []string{"config =", "created_by ="}},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", test.file))
			if err != nil {
				t.Fatal(err)
			}
			start := "-- name: " + test.query + " "
			text := string(body)
			index := strings.Index(text, start)
			if index < 0 {
				t.Fatalf("query %s not found", test.query)
			}
			section := text[index:]
			if next := strings.Index(section[len(start):], "\n-- name: "); next >= 0 {
				section = section[:len(start)+next]
			}
			if set := strings.Index(section, "\nSET "); set >= 0 {
				section = section[set:]
				if where := strings.Index(section, "\nWHERE "); where >= 0 {
					section = section[:where]
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(section, forbidden) {
					t.Fatalf("%s mutates user-managed field through %q", test.query, forbidden)
				}
			}
		})
	}
}
