package rolesource

import (
	"reflect"
	"testing"
)

func planTestSnapshot(t testing.TB, manifest Manifest) Snapshot {
	t.Helper()
	if err := validateManifest(&manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := digestManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		Kind: "fake_source", AdapterVersion: "1.0.0", ContractVersion: ContractVersion,
		ManifestDigest: digest, Manifest: manifest,
		SourceEvidence: SourceEvidence{TreeDigest: testSHA256("1")},
	}
	snapshotDigest, err := digestSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.SnapshotDigest = snapshotDigest
	return snapshot
}

func refreshSnapshotDigest(t *testing.T, snapshot *Snapshot) {
	t.Helper()
	snapshot.SnapshotDigest = ""
	digest, err := digestSnapshot(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.SnapshotDigest = digest
}

func planTestManifest() Manifest {
	return Manifest{ContractVersion: ContractVersion, Roles: []Role{{
		ID: "writer", DisplayName: "Writer", Instructions: testArtifact("roles/writer/instructions.md"),
		Skills: []Skill{{ID: "draft", Name: "Draft", Entrypoint: testArtifact("roles/writer/skills/draft/SKILL.md")}},
	}}}
}

func TestCapabilityBindingObjectIDIsUnambiguous(t *testing.T) {
	left := capabilityBindingObjectID(CapabilityBinding{CapabilityID: "a/b", SkillID: "c", Profile: "d"})
	right := capabilityBindingObjectID(CapabilityBinding{CapabilityID: "a", SkillID: "b/c", Profile: "d"})
	if left == right {
		t.Fatalf("ambiguous binding identities collided: %s", left)
	}
}

func TestBuildPlanSurfacesUnsupportedCapabilityAuthorityBeforeApproval(t *testing.T) {
	manifest := planTestManifest()
	manifest.Capabilities = []Capability{{ID: "browser", Name: "Browser", Version: "1.0.0", Profiles: []string{"default"}, PermissionModes: []string{"external-write"}, Entrypoint: testArtifact("capability.md")}}
	binding := CapabilityBinding{CapabilityID: "browser", SkillID: "draft", Profile: "default", VersionConstraint: "^1.0.0", PermissionMode: "external-write"}
	manifest.Roles[0].CapabilityBindings = []CapabilityBinding{binding}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applyable || len(plan.Blockers) != 1 || plan.Blockers[0].Code != "capability_write_boundary_unavailable" {
		t.Fatalf("unsupported authority plan=%+v", plan)
	}
	ref := ObjectRef{Kind: "capability_binding", ParentID: "writer", ID: capabilityBindingObjectID(binding)}
	found := false
	for _, action := range plan.Actions {
		if action.Ref == ref {
			found = action.Operation == PlanBlocked
		}
	}
	if !found {
		t.Fatal("capability binding was not blocked in the review plan")
	}
}

func TestBuildPlanInitialImportIsDeterministic(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	first, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same immutable snapshot produced different plans")
	}
	if first.PlanDigest == "" || first.Summary.Create != 2 || !first.Applyable {
		t.Fatalf("unexpected initial plan: %+v", first)
	}
	if err := ValidatePlan(first); err != nil {
		t.Fatalf("ValidatePlan rejected generated plan: %v", err)
	}
}

func TestBuildRollbackPlanIsForwardImmutableAndDigestDistinct(t *testing.T) {
	historical := planTestSnapshot(t, planTestManifest())
	currentManifest := planTestManifest()
	currentManifest.Roles[0].DisplayName = "Writer v2"
	current := planTestSnapshot(t, currentManifest)
	rollback, err := BuildRollbackPlan("source-1", current, historical)
	if err != nil {
		t.Fatal(err)
	}
	normal, err := BuildPlan("source-1", &current, historical)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Mode != PlanModeRollback || rollback.PlanDigest == normal.PlanDigest || rollback.FromSnapshotDigest != current.SnapshotDigest || rollback.ToSnapshotDigest != historical.SnapshotDigest {
		t.Fatalf("rollback plan = %+v normal digest=%s", rollback, normal.PlanDigest)
	}
	if err := ValidatePlan(rollback); err != nil {
		t.Fatalf("rollback validation: %v", err)
	}
	if _, err := BuildRollbackPlan("source-1", historical, historical); err == nil {
		t.Fatal("rollback to active snapshot accepted")
	}
	tampered := rollback
	tampered.Mode = "rewrite_history"
	if err := ValidatePlan(tampered); err == nil {
		t.Fatal("invalid rollback mode accepted")
	}
}

func TestBuildPlanSeparatesRoleAndSkillChanges(t *testing.T) {
	baseManifest := planTestManifest()
	base := planTestSnapshot(t, baseManifest)
	targetManifest := planTestManifest()
	targetManifest.Roles[0].Skills[0].Entrypoint.Digest = testSHA256("b")
	target := planTestSnapshot(t, targetManifest)
	plan, err := BuildPlan("source-1", &base, target)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]PlanOperation{}
	for _, action := range plan.Actions {
		operations[action.Ref.Kind] = action.Operation
	}
	if operations["role"] != PlanUnchanged || operations["skill"] != PlanUpdate {
		t.Fatalf("operations = %v, want unchanged role and updated skill", operations)
	}
}

func TestBuildPlanUsesArchiveCandidatesForRemovals(t *testing.T) {
	base := planTestSnapshot(t, planTestManifest())
	targetManifest := planTestManifest()
	targetManifest.Roles[0].Skills = []Skill{}
	target := planTestSnapshot(t, targetManifest)
	plan, err := BuildPlan("source-1", &base, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.ArchiveCandidate != 1 {
		t.Fatalf("archive candidates = %d, want 1", plan.Summary.ArchiveCandidate)
	}
	for _, action := range plan.Actions {
		if action.Ref.Kind == "skill" && (action.Operation != PlanArchiveCandidate || action.Risk != PlanRiskHigh) {
			t.Fatalf("removed skill action = %+v", action)
		}
	}
}

func TestBuildPlanRoleDiagnosticBlocksRoleAndChildren(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	target.Diagnostics = []Diagnostic{{
		Severity: DiagnosticError, Code: "mcp_unresolved_env", Message: "missing environment declaration",
		ObjectKind: "role", ObjectID: "writer",
	}}
	refreshSnapshotDigest(t, &target)
	plan, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applyable || plan.Summary.Blocked != 2 {
		t.Fatalf("plan should block role and child: %+v", plan)
	}
	for _, action := range plan.Actions {
		if action.Operation != PlanBlocked || action.ProposedOperation != PlanCreate || !reflect.DeepEqual(action.BlockingDiagnostics, []string{"mcp_unresolved_env"}) {
			t.Fatalf("unexpected blocked action: %+v", action)
		}
	}
}

func TestValidatePlanRejectsTampering(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	plan, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	plan.Actions[0].DisplayName = "tampered"
	if err := ValidatePlan(plan); err == nil {
		t.Fatal("ValidatePlan accepted a plan changed after digesting")
	}
}

func TestBuildPlanGlobalDiagnosticBlocksEveryTargetObject(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	target.Diagnostics = []Diagnostic{{
		Severity: DiagnosticError, Code: "artifact_total_exceeded", Message: "too many artifact bytes",
		ObjectKind: "artifact", ObjectID: "large.txt",
	}}
	refreshSnapshotDigest(t, &target)
	plan, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applyable || plan.Summary.Blocked != len(plan.Actions) {
		t.Fatalf("global diagnostic did not block all actions: %+v", plan)
	}
}

func TestBuildPlanRejectsTamperedSnapshotDigest(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	target.Manifest.Roles[0].DisplayName = "Tampered"
	if _, err := BuildPlan("source-1", nil, target); err == nil {
		t.Fatal("BuildPlan accepted a snapshot whose manifest no longer matches its digest")
	}
}

func TestSnapshotDigestIncludesDiagnosticsAndEvidence(t *testing.T) {
	base := planTestSnapshot(t, planTestManifest())
	withDiagnostic := base
	withDiagnostic.Diagnostics = []Diagnostic{{Severity: DiagnosticWarning, Code: "skipped", Message: "file skipped"}}
	refreshSnapshotDigest(t, &withDiagnostic)
	if base.ManifestDigest != withDiagnostic.ManifestDigest || base.SnapshotDigest == withDiagnostic.SnapshotDigest {
		t.Fatal("diagnostic changes must change snapshot identity without changing manifest identity")
	}

	withEvidence := base
	withEvidence.SourceEvidence.Revision = "next"
	refreshSnapshotDigest(t, &withEvidence)
	if base.SnapshotDigest == withEvidence.SnapshotDigest {
		t.Fatal("source evidence changes must change snapshot identity")
	}
}

func TestBuildPlanNoChangeHasNoMutationActions(t *testing.T) {
	snapshot := planTestSnapshot(t, planTestManifest())
	plan, err := BuildPlan("source-1", &snapshot, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applyable || plan.Summary.Unchanged != len(plan.Actions) || plan.Summary.Create+plan.Summary.Update+plan.Summary.ArchiveCandidate+plan.Summary.Blocked != 0 {
		t.Fatalf("unexpected no-change plan: %+v", plan)
	}
}
