package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
)

func TestReadRoleSourceManagedInputSecureFileOrBoundedStdin(t *testing.T) {
	body := []byte(`{"version":1}`)
	fromStdin, err := readRoleSourceManagedInput(bytes.NewReader(body), "-")
	if err != nil || string(fromStdin) != string(body) {
		t.Fatalf("stdin input = %q, err=%v", fromStdin, err)
	}

	privatePath := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(privatePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := readRoleSourceManagedInput(nil, privatePath)
	if err != nil || string(fromFile) != string(body) {
		t.Fatalf("file input = %q, err=%v", fromFile, err)
	}

	publicPath := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(publicPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRoleSourceManagedInput(nil, publicPath); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("public file error = %v", err)
	}

	linkPath := filepath.Join(t.TempDir(), "desired.json")
	if err := os.Symlink(privatePath, linkPath); err == nil {
		if _, err := readRoleSourceManagedInput(nil, linkPath); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("symlink file error = %v", err)
		}
	}

	oversized := bytes.Repeat([]byte("x"), maxRoleSourceManagedInputBytes+1)
	if _, err := readRoleSourceManagedInput(bytes.NewReader(oversized), "-"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized stdin error = %v", err)
	}
}

func TestPrintRoleSourceConfigSummaryIsRedactedAndTerminalSafe(t *testing.T) {
	summary := daemon.RoleSourceConfigSummary{
		Revision: "sha256:" + strings.Repeat("a", 64), Version: 1,
		ConfigFileName: "role\nsources.json", AllowedRootNames: []string{"private\troot"}, SourceCount: 1,
		Sources: []daemon.RoleSourceManagedSourceSummary{{
			ID: "source-1", Kind: manifestdir.Kind, Configured: true,
			Attributes: []rolesource.ConfigAttribute{{Name: "root_name", Value: "catalog\nforged"}},
		}},
	}
	var out bytes.Buffer
	cmd := daemonRoleSourceShowCmd
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("output", "table"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.SetOut(os.Stdout)
		_ = cmd.Flags().Set("output", "table")
	})
	if err := printRoleSourceConfigSummary(cmd, summary); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "role\nsources") || strings.Contains(got, "private\troot") || strings.Contains(got, "catalog\nforged") {
		t.Fatalf("table output contains terminal control characters: %q", got)
	}
	if !strings.Contains(got, "role�sources.json") || !strings.Contains(got, "catalog�forged") {
		t.Fatalf("table output did not preserve a safe summary: %q", got)
	}
}

func TestDaemonRoleSourceApplyCommandCreatesDefaultPrivateConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MULTICA_ROLE_SOURCE_CONFIG_FILE", "")
	profileDir := filepath.Join(home, ".multica")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	allowedRoot := t.TempDir()
	desired, err := json.Marshal(daemon.RoleSourceManagedDocument{
		Version: 1, AllowedRoots: []string{allowedRoot},
		Sources: map[string]daemon.RoleSourceManagedSource{
			"manifest-main": {Kind: manifestdir.Kind, Config: json.RawMessage(`{"root_path":` + mustJSONCLI(t, allowedRoot) + `}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := daemonRoleSourceApplyCmd
	var out bytes.Buffer
	cmd.SetIn(bytes.NewReader(desired))
	cmd.SetOut(&out)
	for key, value := range map[string]string{
		"document": "-", "expected-revision": daemon.RoleSourceConfigAbsentRevision, "output": "json",
	} {
		if err := cmd.Flags().Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cmd.SetIn(os.Stdin)
		cmd.SetOut(os.Stdout)
		_ = cmd.Flags().Set("document", "-")
		_ = cmd.Flags().Set("expected-revision", "")
		_ = cmd.Flags().Set("output", "table")
	})
	if err := runDaemonRoleSourceApply(cmd, nil); err != nil {
		t.Fatalf("apply command: %v", err)
	}
	configPath, err := daemon.DefaultRoleSourceConfigPath("")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
	if strings.Contains(out.String(), allowedRoot) || strings.Contains(out.String(), `"digest_key":`) {
		t.Fatalf("command output leaked private config: %s", out.String())
	}
}

func TestDaemonRoleSourceCommandsRejectManagedTaskContext(t *testing.T) {
	t.Setenv("MULTICA_AGENT_ID", "agent-test")
	t.Setenv("MULTICA_TASK_ID", "task-test")
	if err := runDaemonRoleSourceShow(daemonRoleSourceShowCmd, nil); err == nil || !strings.Contains(err.Error(), "not available inside a daemon-managed task") {
		t.Fatalf("task-context show error = %v", err)
	}
}

func mustJSONCLI(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
