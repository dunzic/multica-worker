package rolesource

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeAdapter struct {
	desc        Descriptor
	manifest    Manifest
	evidence    SourceEvidence
	diagnostics []Diagnostic
	configErr   error
	scanErr     error
}

func (f fakeAdapter) Descriptor() Descriptor               { return f.desc }
func (f fakeAdapter) ValidateConfig(json.RawMessage) error { return f.configErr }
func (f fakeAdapter) RedactConfig(json.RawMessage) (ConfigSummary, error) {
	return ConfigSummary{Configured: true}, nil
}
func (f fakeAdapter) Scan(context.Context, ScanRequest) (ScanOutput, error) {
	return ScanOutput{
		Manifest:       f.manifest,
		Diagnostics:    f.diagnostics,
		SourceEvidence: f.evidence,
	}, f.scanErr
}

func TestRegistrySnapshotDigestCanonicalizesDiagnosticOrder(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.diagnostics = []Diagnostic{
		{Severity: DiagnosticWarning, Code: "z_warning", Message: "z"},
		{Severity: DiagnosticInfo, Code: "a_info", Message: "a"},
	}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Scan(context.Background(), "fake_source", ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.diagnostics[0], adapter.diagnostics[1] = adapter.diagnostics[1], adapter.diagnostics[0]
	registry, err = NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Scan(context.Background(), "fake_source", ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if first.SnapshotDigest != second.SnapshotDigest {
		t.Fatalf("snapshot digest changed with diagnostic order: %s != %s", first.SnapshotDigest, second.SnapshotDigest)
	}
}

func TestRegistryRejectsNestedManifestBomb(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles[0].Environment = make([]EnvironmentKey, maxNormalizedObjects+1)
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("Scan accepted more nested objects than the generic contract limit")
	}
}

func validFakeAdapter(kind Kind) fakeAdapter {
	return fakeAdapter{
		desc: Descriptor{Kind: kind, DisplayName: "Fake", AdapterVersion: "1.2.3", ContractVersion: ContractVersion},
		evidence: SourceEvidence{
			Revision:   "abc123",
			TreeDigest: testSHA256("1"),
		},
		manifest: Manifest{
			ContractVersion: ContractVersion,
			Roles: []Role{{
				ID: "writer", DisplayName: "Writer", Instructions: testArtifact("roles/writer/instructions.md"),
			}},
		},
	}
}

func testSHA256(digit string) string {
	return "sha256:" + strings.Repeat(digit, 64)
}

func testHMACSHA256(digit string) string {
	return "hmac-sha256:" + strings.Repeat(digit, 64)
}

func testArtifact(path string) ArtifactRef {
	return ArtifactRef{Digest: testSHA256("a"), Path: path, MediaType: "text/markdown", SizeBytes: 10}
}

func TestRegistryRejectsDuplicateKinds(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	_, err := NewRegistry(adapter, adapter)
	if !errors.Is(err, ErrDuplicateAdapter) {
		t.Fatalf("NewRegistry error = %v, want ErrDuplicateAdapter", err)
	}
}

func TestRegistryRejectsInvalidDescriptor(t *testing.T) {
	adapter := validFakeAdapter("AgentWaker-Directory")
	if _, err := NewRegistry(adapter); err == nil {
		t.Fatal("NewRegistry succeeded with an invalid adapter kind")
	}
}

func TestRegistryDescriptorsAreSorted(t *testing.T) {
	registry, err := NewRegistry(validFakeAdapter("z_source"), validFakeAdapter("a_source"))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	got := []Kind{descriptors[0].Kind, descriptors[1].Kind}
	want := []Kind{"a_source", "z_source"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor order = %v, want %v", got, want)
	}
}

func TestRegistryRedactsValidatedConfig(t *testing.T) {
	registry, err := NewRegistry(validFakeAdapter("fake_source"))
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := registry.RedactConfig("fake_source", json.RawMessage(`{"token":"plaintext"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !redacted.Configured || len(redacted.Attributes) != 0 {
		t.Fatalf("redacted config = %+v", redacted)
	}
}

func TestRegistryScanCanonicalizesAndHashesManifest(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles = []Role{
		{ID: "z-role", DisplayName: "Z", Instructions: testArtifact("roles/z/instructions.md"), Skills: []Skill{
			{ID: "z-skill", Entrypoint: testArtifact("skills/z/SKILL.md")},
			{ID: "a-skill", Entrypoint: testArtifact("skills/a/SKILL.md")},
		}},
		{ID: "a-role", DisplayName: "A", Instructions: testArtifact("roles/a/instructions.md")},
	}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestDigest == "" {
		t.Fatal("manifest digest is empty")
	}
	if snapshot.SnapshotDigest == "" {
		t.Fatal("snapshot digest is empty")
	}
	if got := snapshot.Manifest.Roles[0].ID; got != "a-role" {
		t.Fatalf("first canonical role = %q, want a-role", got)
	}
	if got := snapshot.Manifest.Roles[1].Skills[0].ID; got != "a-skill" {
		t.Fatalf("first canonical skill = %q, want a-skill", got)
	}

	adapter.manifest.Roles = []Role{
		{ID: "a-role", DisplayName: "A", Instructions: testArtifact("roles/a/instructions.md")},
		{ID: "z-role", DisplayName: "Z", Instructions: testArtifact("roles/z/instructions.md"), Skills: []Skill{
			{ID: "a-skill", Entrypoint: testArtifact("skills/a/SKILL.md")},
			{ID: "z-skill", Entrypoint: testArtifact("skills/z/SKILL.md")},
		}},
	}
	registry2, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot2, err := registry2.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestDigest != snapshot2.ManifestDigest {
		t.Fatalf("digest changed with input order: %s != %s", snapshot.ManifestDigest, snapshot2.ManifestDigest)
	}
	if snapshot.SnapshotDigest != snapshot2.SnapshotDigest {
		t.Fatalf("snapshot digest changed with input order: %s != %s", snapshot.SnapshotDigest, snapshot2.SnapshotDigest)
	}
}

func TestRegistryScanRejectsDuplicateStableIDs(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles = []Role{
		{ID: "writer", Instructions: testArtifact("roles/writer/instructions.md")},
		{ID: "writer", Instructions: testArtifact("roles/writer-2/instructions.md")},
	}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("Scan succeeded with duplicate role IDs")
	}
}

func TestRegistryScanRejectsSecretMetadataWithoutKeyedDigest(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles[0].Environment = []EnvironmentKey{{
		Name: "API_TOKEN", Required: true, Secret: true, Configured: true, ValueDigest: testSHA256("b"),
	}}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("Scan accepted a reversible/non-keyed secret digest")
	}

	adapter.manifest.Roles[0].Environment[0].ValueDigest = testHMACSHA256("b")
	registry, err = NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Scan rejected a keyed secret digest: %v", err)
	}
}

func TestRegistryScanRejectsUnsafeArtifactPath(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles[0].Instructions.Path = "../secrets/.env"
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("Scan accepted a traversal artifact path")
	}
}

func TestRegistryScanRejectsBindingToUnknownObjects(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.manifest.Roles[0].CapabilityBindings = []CapabilityBinding{{
		CapabilityID: "missing", SkillID: "missing", Profile: "default", PermissionMode: "read-only",
	}}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("Scan accepted a binding to unknown source objects")
	}
}

func TestRegistryScanRequiresCapabilityConstraintToResolve(t *testing.T) {
	for _, test := range []struct {
		name       string
		constraint string
		wantError  bool
	}{
		{name: "matching caret", constraint: "^1.2.0"},
		{name: "different major", constraint: "^2.0.0", wantError: true},
		{name: "missing constraint", constraint: "", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := validFakeAdapter("fake_source")
			adapter.manifest.Capabilities = []Capability{{
				ID: "browser", Name: "Browser", Version: "1.2.3",
				Profiles: []string{"default"}, PermissionModes: []string{"read-only"},
				Entrypoint: testArtifact("capabilities/browser/SKILL.md"),
			}}
			adapter.manifest.Roles[0].Skills = []Skill{{
				ID: "draft", Name: "Draft", Entrypoint: testArtifact("skills/draft/SKILL.md"),
			}}
			adapter.manifest.Roles[0].CapabilityBindings = []CapabilityBinding{{
				CapabilityID: "browser", SkillID: "draft", Profile: "default",
				VersionConstraint: test.constraint, PermissionMode: "read-only",
			}}
			registry, err := NewRegistry(adapter)
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Scan(context.Background(), "fake_source", ScanRequest{
				WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
			})
			if (err != nil) != test.wantError {
				t.Fatalf("Scan error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestRegistryScanRejectsUnsafeSourceEvidence(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.evidence = SourceEvidence{}
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("Scan accepted missing tree evidence digest")
	}
}

func TestRegistryScanRejectsInvalidConfigBeforeAdapterScan(t *testing.T) {
	adapter := validFakeAdapter("fake_source")
	adapter.configErr = errors.New("bad config")
	adapter.scanErr = errors.New("scan should not run")
	registry, err := NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Scan(context.Background(), "fake_source", ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: json.RawMessage(`{}`),
	})
	if err == nil || !errors.Is(err, adapter.configErr) {
		t.Fatalf("Scan error = %v, want config error", err)
	}
}

func TestRegistryScanRequiresTenantIdentity(t *testing.T) {
	registry, err := NewRegistry(validFakeAdapter("fake_source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Scan(context.Background(), "fake_source", ScanRequest{}); err == nil {
		t.Fatal("Scan succeeded without workspace/source identity")
	}
}
