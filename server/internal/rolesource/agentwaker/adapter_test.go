package agentwaker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

func TestAdapterScanProducesSecretFreeNormalizedSnapshot(t *testing.T) {
	root := writeFixture(t, "super-secret-token")
	adapter := newTestAdapter(t)
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	before := listFixturePaths(t, root)
	snapshot, err := registry.Scan(context.Background(), Kind, rolesource.ScanRequest{
		WorkspaceID: "workspace-1",
		SourceID:    "source-1",
		Config:      configJSON(t, root),
	})
	if err != nil {
		t.Fatal(err)
	}
	after := listFixturePaths(t, root)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("scan mutated source tree: before=%v after=%v", before, after)
	}
	if len(snapshot.Manifest.Roles) != 1 || len(snapshot.Manifest.Capabilities) != 1 {
		t.Fatalf("snapshot counts = roles:%d capabilities:%d, want 1/1", len(snapshot.Manifest.Roles), len(snapshot.Manifest.Capabilities))
	}
	role := snapshot.Manifest.Roles[0]
	if role.ID != "writer" || len(role.Skills) != 2 || len(role.CapabilityBindings) != 1 || len(role.MCP) != 1 || len(role.Automations) != 1 {
		t.Fatalf("normalized role = %#v", role)
	}
	var researchSkill rolesource.Skill
	for _, skill := range role.Skills {
		if skill.ID == "research" {
			researchSkill = skill
		}
	}
	if len(researchSkill.Artifacts) != 1 || researchSkill.Artifacts[0].Path != "writer/writer-skills/research/template.md" {
		t.Fatalf("research supporting artifacts = %#v", researchSkill.Artifacts)
	}
	var token rolesource.EnvironmentKey
	for _, entry := range role.Environment {
		if entry.Name == "API_TOKEN" {
			token = entry
		}
	}
	if !token.Configured || !token.Required || !token.Secret || !strings.HasPrefix(token.ValueDigest, "hmac-sha256:") {
		t.Fatalf("API_TOKEN metadata = %#v", token)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"super-secret-token", "API_TOKEN=super-secret-token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked forbidden plaintext %q: %s", forbidden, encoded)
		}
	}
}

func TestAdapterScanSecretChangeChangesDigestsWithoutLeakingValue(t *testing.T) {
	root := writeFixture(t, "first-secret")
	adapter := newTestAdapter(t)
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root)}
	first, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, root, "writer/env/.env", "API_TOKEN=second-secret\nAPI_BASE=https://api.example.com\n")
	second, err := registry.Scan(context.Background(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestDigest == second.ManifestDigest || first.SourceEvidence.TreeDigest == second.SourceEvidence.TreeDigest {
		t.Fatalf("secret change did not change evidence: manifest %s/%s tree %s/%s",
			first.ManifestDigest, second.ManifestDigest, first.SourceEvidence.TreeDigest, second.SourceEvidence.TreeDigest)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "second-secret") {
		t.Fatalf("second snapshot leaked changed secret: %s", encoded)
	}
}

func TestAdapterRejectsRegistryTraversal(t *testing.T) {
	root := writeFixture(t, "secret")
	writeFixtureFile(t, root, "capabilities/registry.yaml", `schema_version: "1.0"
capabilities:
  - id: information-collection
    version: 1.0.0
    manifest: ../writer/env/.env
`)
	adapter := newTestAdapter(t)
	_, err := adapter.Scan(context.Background(), rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root),
	})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Scan error = %v, want traversal rejection", err)
	}
}

func TestAdapterRejectsSymlinkedEntrypoint(t *testing.T) {
	root := writeFixture(t, "secret")
	target := filepath.Join(root, "capabilities", "information-collection", "SKILL.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "writer", "agent-detail.en.md"), target); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	adapter := newTestAdapter(t)
	_, err := adapter.Scan(context.Background(), rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root),
	})
	if !errors.Is(err, rolesource.ErrSymlink) {
		t.Fatalf("Scan error = %v, want ErrSymlink", err)
	}
}

func TestAdapterReportsUndeclaredMCPEnvironment(t *testing.T) {
	root := writeFixture(t, "secret")
	writeFixtureFile(t, root, "writer/mcp/mcp.json", `{
  "mcpServers": {
    "platform-api": {
      "command": "platform-mcp",
      "env": {"MISSING_TOKEN": "${MISSING_TOKEN}"}
    }
  }
}`)
	adapter := newTestAdapter(t)
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Scan(context.Background(), Kind, rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Diagnostics) != 1 || snapshot.Diagnostics[0].Code != "mcp_unresolved_env" || snapshot.Diagnostics[0].Severity != rolesource.DiagnosticError {
		t.Fatalf("diagnostics = %#v", snapshot.Diagnostics)
	}
}

func TestAdapterReportsBinarySupportingArtifactWithoutReadingIt(t *testing.T) {
	root := writeFixture(t, "secret")
	writeFixtureFile(t, root, "writer/writer-skills/research/image.png", "not-a-real-image")
	adapter := newTestAdapter(t)
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := registry.Scan(context.Background(), Kind, rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Code == "artifact_binary_skipped" && diagnostic.Path == "writer/writer-skills/research/image.png" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %#v, want artifact_binary_skipped", snapshot.Diagnostics)
	}
}

func TestAdapterRejectsInvalidAutomationSchedule(t *testing.T) {
	root := writeFixture(t, "secret")
	manifestPath := filepath.Join(root, "writer", "daily-tasks", "manifest.yaml")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `expression: "0 9 * * 1-5"`, `expression: "not a cron"`, 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t)
	_, err = adapter.Scan(context.Background(), rolesource.ScanRequest{
		WorkspaceID: "workspace-1", SourceID: "source-1", Config: configJSON(t, root),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid cron") {
		t.Fatalf("Scan error = %v, want invalid cron", err)
	}
}

func TestAdapterValidateConfigRejectsUnknownFields(t *testing.T) {
	adapter := newTestAdapter(t)
	if err := adapter.ValidateConfig(json.RawMessage(`{"root_path":"/tmp/source","token":"plaintext"}`)); err == nil {
		t.Fatal("ValidateConfig accepted an unknown credential-like field")
	}
}

func TestAdapterRedactedConfigDoesNotExposeAbsolutePath(t *testing.T) {
	adapter := newTestAdapter(t)
	raw := json.RawMessage(`{"root_path":"/private/role-sources/customer-a"}`)
	redacted, err := adapter.RedactConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/private/role-sources") || !strings.Contains(string(body), "customer-a") {
		t.Fatalf("redacted config = %s", body)
	}
}

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New([]byte("0123456789abcdef0123456789abcdef"), func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func configJSON(t *testing.T, root string) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(config{RootPath: root})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeFixture(t *testing.T, secret string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"schemas/profile-v2.1.schema.json": `{"type":"object"}`,
		"capabilities/registry.yaml": `schema_version: "1.0"
capabilities:
  - id: information-collection
    version: 1.0.0
    manifest: information-collection/CAPABILITY.yaml
`,
		"capabilities/information-collection/CAPABILITY.yaml": `schema_version: "1.0"
id: information-collection
name: Information Collection
version: 1.0.0
description: Collect evidence.
entrypoint: SKILL.md
profiles:
  - id: research
    description: Research mode.
adapters:
  - id: web
    required: true
    description: Read public pages.
contracts:
  input_schema: schemas/input.schema.json
  output_schema: schemas/output.schema.json
requires:
  environment: []
  mcp: []
permissions:
  default_mode: read-only
  supports_account_actions: false
`,
		"capabilities/information-collection/SKILL.md":                   "# Information Collection\n",
		"capabilities/information-collection/schemas/input.schema.json":  `{"type":"object"}`,
		"capabilities/information-collection/schemas/output.schema.json": `{"type":"object"}`,
		"writer/agent-soul/PROFILE.yaml": `schema_version: "2.1"
id: writer
display_name: Writer
role_type: content
title: Content Writer
version: 1.0.0
lifecycle: active
mission: Produce reviewed copy.
skills:
  directory: writer-skills/
  meta_entrypoint: writer-skills/SKILL.md
  env_example: env/.env.example
  items:
    - id: research
      name: Research
      use_when: Collect evidence.
      entrypoint: writer-skills/research/SKILL.md
      status: implemented
generation:
  card_title_zh: 写作
  card_mission_zh: 生成经过审核的内容。
`,
		"writer/agent-detail.en.md":                 "# Writer instructions\n",
		"writer/agent-persona.html":                 "<section>Writer</section>\n",
		"writer/writer-skills/SKILL.md":             "# Writer routing\n",
		"writer/writer-skills/research/SKILL.md":    "# Research\n",
		"writer/writer-skills/research/template.md": "Research template\n",
		"writer/capabilities.yaml": `schema_version: "1.0"
role: writer
capabilities:
  - id: information-collection
    version: ^1.0.0
    required: true
    used_by:
      - skill: research
        profile: research
    permissions:
      mode: read-only
      account_actions: false
    fallback:
      behavior: blocked
      message: Evidence is required.
`,
		"writer/env/.env.example": "# API credential\nAPI_TOKEN=\nAPI_BASE=https://api.example.com\n",
		"writer/env/.env":         "API_TOKEN=" + secret + "\nAPI_BASE=https://api.example.com\n",
		"writer/mcp/mcp.json": `{
  "mcpServers": {
    "platform-api": {
      "command": "platform-mcp",
      "env": {
        "API_TOKEN": "${API_TOKEN}",
        "API_BASE": "${API_BASE}"
      }
    }
  }
}`,
		"writer/daily-tasks/manifest.yaml": `schema_version: "1.0"
role_id: writer
automations:
  - id: daily-review
    title: Daily review
    prompt_file: daily-review.prompt.md
    execution:
      mode: run_only
      issue_title_template: ""
    schedule:
      kind: cron
      expression: "0 9 * * 1-5"
      timezone: Asia/Shanghai
      initial_enabled: false
      label: Weekday review
    sync:
      content: source-authoritative
      schedule: source-authoritative
      activation: workspace-preserve
      missing: archive
    governance:
      external_writes: approval-required
`,
		"writer/daily-tasks/daily-review.prompt.md": "# Review\n",
	}
	for name, body := range files {
		writeFixtureFile(t, root, name, body)
	}
	return root
}

func writeFixtureFile(t *testing.T, root, name, body string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func listFixturePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}
