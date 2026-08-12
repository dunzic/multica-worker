package manifestdir

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

func TestManifestDirectoryAdapterUsesGenericSnapshotAndArtifactContracts(t *testing.T) {
	root := t.TempDir()
	artifactBody := []byte("# Writer\n")
	artifactPath := "roles/writer.md"
	writeFile(t, filepath.Join(root, artifactPath), artifactBody)
	ref := artifactRef(artifactPath, artifactBody)
	manifest := rolesource.Manifest{
		ContractVersion: rolesource.ContractVersion,
		Roles: []rolesource.Role{{
			ID: "writer", DisplayName: "Writer", Version: "1.0.0", Lifecycle: "active", Instructions: ref,
			Skills: []rolesource.Skill{}, CapabilityBindings: []rolesource.CapabilityBinding{},
			Environment: []rolesource.EnvironmentKey{}, MCP: []rolesource.MCPServer{}, Automations: []rolesource.Automation{},
		}},
		Capabilities: []rolesource.Capability{},
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, defaultManifest), manifestBody)

	adapter, err := New(func(candidate string) error {
		if candidate != root {
			return errors.New("outside allowed root")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1",
		Config: json.RawMessage(`{"root_path":` + mustJSON(t, root) + `}`),
	}
	snapshot, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Kind != Kind || snapshot.Manifest.Roles[0].ID != "writer" || snapshot.SnapshotDigest == "" || snapshot.SourceEvidence.Issuer != string(Kind) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	reader, err := registry.OpenArtifact(context.Background(), Kind, request, ref)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(reader)
	reader.Close() //nolint:errcheck
	if err != nil || string(opened) != string(artifactBody) {
		t.Fatalf("opened artifact=%q err=%v", opened, err)
	}

	writeFile(t, filepath.Join(root, artifactPath), []byte("changed"))
	if _, err := registry.OpenArtifact(context.Background(), Kind, request, ref); !errors.Is(err, rolesource.ErrChangedDuringRead) {
		t.Fatalf("changed artifact error=%v", err)
	}
	if _, err := registry.ExportSecretPayload(context.Background(), Kind, request, snapshot, "writer"); !errors.Is(err, rolesource.ErrSecretUnavailable) {
		t.Fatalf("secret export error=%v", err)
	}
}

func TestManifestDirectoryAdapterRedactsPathsAndRejectsUnsafeConfig(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"root_path":` + mustJSON(t, root) + `,"manifest_path":"catalog/roles.json"}`)
	summary, err := adapter.RedactConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(summary)
	if strings.Contains(string(body), root) || !strings.Contains(string(body), "roles.json") || !strings.Contains(string(body), filepath.Base(root)) {
		t.Fatalf("redacted config=%s", body)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"root_path":` + mustJSON(t, root) + `,"manifest_path":"../outside.json"}`),
		json.RawMessage(`{"root_path":` + mustJSON(t, root) + `,"unknown":true}`),
		json.RawMessage(`{"root_path":` + mustJSON(t, root) + `} {}`),
	} {
		if err := adapter.ValidateConfig(invalid); err == nil {
			t.Fatalf("unsafe config accepted: %s", invalid)
		}
	}
}

func TestManifestDirectoryAdapterRejectsAuthorityItDoesNotAdvertise(t *testing.T) {
	ref := artifactRef("role.md", []byte("role"))
	base := rolesource.Manifest{
		ContractVersion: rolesource.ContractVersion,
		Roles: []rolesource.Role{{
			ID: "writer", DisplayName: "Writer", Instructions: ref,
			Skills: []rolesource.Skill{}, CapabilityBindings: []rolesource.CapabilityBinding{}, Environment: []rolesource.EnvironmentKey{},
			MCP: []rolesource.MCPServer{}, Automations: []rolesource.Automation{},
		}}, Capabilities: []rolesource.Capability{},
	}
	configuredSecret := base
	configuredSecret.Roles = append([]rolesource.Role(nil), base.Roles...)
	configuredSecret.Roles[0].Environment = []rolesource.EnvironmentKey{{Name: "TOKEN", Secret: true, Configured: true, ValueDigest: "hmac-sha256:" + strings.Repeat("a", 64)}}
	if err := validateSupportedManifest(configuredSecret); err == nil || !strings.Contains(err.Error(), "secret-transfer authority") {
		t.Fatalf("configured secret error=%v", err)
	}
	binary := base
	binary.Roles = append([]rolesource.Role(nil), base.Roles...)
	binary.Roles[0].Instructions.MediaType = "application/octet-stream"
	if err := validateSupportedManifest(binary); err == nil || !strings.Contains(err.Error(), "text-only") {
		t.Fatalf("binary artifact error=%v", err)
	}
}

func TestManifestDirectoryAdapterAcceptsTenThousandRoleContract(t *testing.T) {
	root := t.TempDir()
	body := []byte("shared instructions")
	ref := artifactRef("shared/instructions.md", body)
	writeFile(t, filepath.Join(root, filepath.FromSlash(ref.Path)), body)
	roles := make([]rolesource.Role, 10_000)
	for index := range roles {
		roles[index] = rolesource.Role{
			ID: fmt.Sprintf("role-%05d", index), DisplayName: fmt.Sprintf("Role %05d", index), Version: "1.0.0", Lifecycle: "active", Instructions: ref,
			Skills: []rolesource.Skill{}, CapabilityBindings: []rolesource.CapabilityBinding{}, Environment: []rolesource.EnvironmentKey{},
			MCP: []rolesource.MCPServer{}, Automations: []rolesource.Automation{},
		}
	}
	manifestBody, err := json.Marshal(rolesource.Manifest{ContractVersion: rolesource.ContractVersion, Roles: roles, Capabilities: []rolesource.Capability{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifestBody) > maxManifestBytes {
		t.Fatalf("10,000-role fixture exceeds adapter manifest limit: %d", len(manifestBody))
	}
	writeFile(t, filepath.Join(root, defaultManifest), manifestBody)
	adapter, err := New(func(candidate string) error {
		if candidate != root {
			return errors.New("outside allowed root")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Scan(context.Background(), Kind, rolesource.ScanRequest{
		WorkspaceID: "workspace-scale", SourceID: "source-scale", Config: json.RawMessage(`{"root_path":` + mustJSON(t, root) + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Manifest.Roles) != 10_000 || snapshot.Manifest.Roles[0].ID != "role-00000" || snapshot.Manifest.Roles[9_999].ID != "role-09999" {
		t.Fatalf("10,000-role snapshot boundary is wrong: count=%d", len(snapshot.Manifest.Roles))
	}
}

func FuzzDecodeConfigAndManifest(f *testing.F) {
	f.Add([]byte(`{"root_path":"/srv/roles"}`), false)
	f.Add([]byte(`{"contract_version":"1.0","roles":[],"capabilities":[]}`), true)
	f.Add([]byte(`{"root_path":"/srv/roles"} {}`), false)
	f.Fuzz(func(t *testing.T, body []byte, manifest bool) {
		if len(body) > maxConfigBytes {
			t.Skip()
		}
		if manifest {
			_, _ = decodeManifest(body)
			return
		}
		_, _ = decodeConfig(body)
	})
}

func artifactRef(path string, body []byte) rolesource.ArtifactRef {
	sum := sha256.Sum256(body)
	return rolesource.ArtifactRef{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "text/markdown", SizeBytes: int64(len(body))}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func writeFile(t *testing.T, name string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
