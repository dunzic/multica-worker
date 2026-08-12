package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/rolesource/agentwaker"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type roleSourceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f roleSourceRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func writeRoleSourceConfigForTest(t *testing.T, allowedRoot, sourceRoot string, mode os.FileMode) string {
	t.Helper()
	document := roleSourceConfigDocument{
		Version:      roleSourceConfigVersion,
		DigestKey:    []byte("0123456789abcdef0123456789abcdef"),
		AllowedRoots: []string{allowedRoot},
		Sources: map[string]roleSourceLocalConfig{
			"agentwaker-main": {
				Kind:   agentwaker.Kind,
				Config: json.RawMessage(`{"root_path":` + mustJSONRoleSourceTest(t, sourceRoot) + `}`),
			},
		},
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "role-sources.json")
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustJSONRoleSourceTest(t *testing.T, value string) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestLoadRoleSourceScannerRequiresPrivateBoundedConfiguration(t *testing.T) {
	root := t.TempDir()
	validPath := writeRoleSourceConfigForTest(t, root, root, 0o600)
	scanner, err := loadRoleSourceScanner(validPath)
	if err != nil || scanner == nil {
		t.Fatalf("valid role source config: scanner=%v err=%v", scanner, err)
	}

	publicPath := writeRoleSourceConfigForTest(t, root, root, 0o644)
	if _, err := loadRoleSourceScanner(publicPath); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("public config permissions error = %v", err)
	}

	outside := t.TempDir()
	outsidePath := writeRoleSourceConfigForTest(t, root, outside, 0o600)
	if _, err := loadRoleSourceScanner(outsidePath); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside source root error = %v", err)
	}
}

func TestLoadConfigEnablesRoleSourceScannerOnlyFromValidatedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture is unavailable on Windows")
	}
	stageFakeAgent(t)
	root := t.TempDir()
	t.Setenv("MULTICA_ROLE_SOURCE_CONFIG_FILE", writeRoleSourceConfigForTest(t, root, root, 0o600))
	cfg, err := LoadConfig(Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.roleSourceScanner == nil {
		t.Fatal("validated role source config did not enable the scanner")
	}
}

func TestLoadRoleSourceScannerRejectsSymlinkConfigAndRoot(t *testing.T) {
	root := t.TempDir()
	configPath := writeRoleSourceConfigForTest(t, root, root, 0o600)
	linkPath := filepath.Join(t.TempDir(), "role-sources-link.json")
	if err := os.Symlink(configPath, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := loadRoleSourceScanner(linkPath); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink config error = %v", err)
	}

	realRoot := t.TempDir()
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, rootLink); err != nil {
		t.Skipf("root symlink unavailable: %v", err)
	}
	rootConfig := writeRoleSourceConfigForTest(t, rootLink, rootLink, 0o600)
	if _, err := loadRoleSourceScanner(rootConfig); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink root error = %v", err)
	}
}

func TestRoleSourceScannerFailsClosedBeforeAdapterScan(t *testing.T) {
	root := t.TempDir()
	scanner, err := loadRoleSourceScanner(writeRoleSourceConfigForTest(t, root, root, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	pending := protocol.DaemonHeartbeatPendingRoleSourceScan{
		Kind: string(agentwaker.Kind), AdapterVersion: agentwaker.Descriptor().AdapterVersion,
		DaemonConfigID: "missing",
	}
	if _, code := scanner.scan(t.Context(), pending); code != "config_not_found" {
		t.Fatalf("missing config code = %q", code)
	}
	pending.DaemonConfigID = "agentwaker-main"
	pending.AdapterVersion = "999.0.0"
	if _, code := scanner.scan(t.Context(), pending); code != "adapter_version_mismatch" {
		t.Fatalf("version mismatch code = %q", code)
	}
	pending.AdapterVersion = agentwaker.Descriptor().AdapterVersion
	pending.Kind = "unknown"
	if _, code := scanner.scan(t.Context(), pending); code != "adapter_not_supported" {
		t.Fatalf("adapter mismatch code = %q", code)
	}
}

func TestRoleSourceScannerRunsAgentWakerThroughGenericRegistry(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"schemas/profile-v2.1.schema.json": `{"type":"object"}`,
		"capabilities/registry.yaml":       "schema_version: \"1.0\"\ncapabilities: []\n",
		"writer/agent-soul/PROFILE.yaml": `schema_version: "2.1"
id: writer
display_name: Writer
role_type: content
title: Writer
version: 1.0.0
lifecycle: active
mission: Write reviewed content.
skills:
  directory: writer-skills/
  meta_entrypoint: ""
  env_example: env/.env.example
  items:
    - id: draft
      name: Draft
      use_when: Draft content.
      entrypoint: writer-skills/draft/SKILL.md
      status: implemented
generation:
  card_title_zh: 写作
  card_mission_zh: 撰写内容。
`,
		"writer/agent-detail.en.md":           "# Writer\n",
		"writer/writer-skills/draft/SKILL.md": "# Draft\n",
		"writer/env/.env.example":             "API_TOKEN=\n",
	}
	for name, body := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scanner, err := loadRoleSourceScanner(writeRoleSourceConfigForTest(t, root, root, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	pending := protocol.DaemonHeartbeatPendingRoleSourceScan{
		WorkspaceID: "00000000-0000-4000-8000-000000000061",
		SourceID:    "00000000-0000-4000-8000-000000000062",
		Kind:        string(agentwaker.Kind), AdapterVersion: agentwaker.Descriptor().AdapterVersion,
		DaemonConfigID: "agentwaker-main",
	}
	snapshot, code := scanner.scan(t.Context(), pending)
	if code != "" {
		t.Fatalf("scan failed with code %q", code)
	}
	if snapshot.SnapshotDigest == "" || len(snapshot.Manifest.Roles) != 1 || snapshot.Manifest.Roles[0].ID != "writer" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRoleSourceHeartbeatPollingIsThrottledAndCapacityAware(t *testing.T) {
	root := t.TempDir()
	scanner, err := loadRoleSourceScanner(writeRoleSourceConfigForTest(t, root, root, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &Daemon{roleSources: scanner, roleSourceLastPoll: make(map[string]time.Time)}
	first := daemon.roleSourceHeartbeatOptions("runtime-1", false)
	second := daemon.roleSourceHeartbeatOptions("runtime-1", false)
	forced := daemon.roleSourceHeartbeatOptions("runtime-1", true)
	if !first.SupportsRoleSourceScan || !first.PollRoleSourceScan || second.PollRoleSourceScan || !forced.PollRoleSourceScan {
		t.Fatalf("poll options: first=%+v second=%+v forced=%+v", first, second, forced)
	}
	if option := daemon.roleSourceHeartbeatOptions("runtime-2", true); !option.SupportsRoleSourceScan || option.PollRoleSourceScan {
		t.Fatalf("capacity-full option = %+v", option)
	}
	daemon.handleHeartbeatActions(t.Context(), "runtime-1", &HeartbeatResponse{}, first.PollRoleSourceScan)
	daemon.releaseRoleSourcePollReservation(forced)
	if len(scanner.semaphore) != 0 {
		t.Fatalf("poll reservations leaked: %d", len(scanner.semaphore))
	}
}

func TestRoleSourceClientCarriesNegotiationLeaseAndIdempotentResult(t *testing.T) {
	requests := make(map[string]map[string]any)
	uploads := make(map[string]string)
	client := NewClient("http://role-source.test")
	httpClient := &http.Client{Transport: roleSourceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPut {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploads[request.URL.Path] = string(body)
			if request.Header.Get("X-Role-Source-Lease-Token") != "lease-1" || request.ContentLength != int64(len(body)) {
				t.Fatalf("artifact upload headers length=%d lease=%q", request.ContentLength, request.Header.Get("X-Role-Source-Lease-Token"))
			}
			return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"ready"}`)), Request: request}, nil
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s request: %v", request.URL.Path, err)
		}
		requests[request.URL.Path] = body
		response := `{"status":"ok"}`
		if strings.HasSuffix(request.URL.Path, "/lease") {
			response = `{"lease_expires_at":"2026-08-13T12:15:00Z"}`
		} else if strings.HasSuffix(request.URL.Path, "/artifacts/check") {
			response = `{"missing":[{"digest":"sha256:` + strings.Repeat("a", 64) + `","path":"writer/agent.md","media_type":"text/markdown","size_bytes":7}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(response)), Request: request,
		}, nil
	})}
	client.client = httpClient
	client.bundleClient = httpClient

	if _, err := client.SendHeartbeat(t.Context(), "runtime-1", HeartbeatOptions{SupportsRoleSourceScan: true, PollRoleSourceScan: true}); err != nil {
		t.Fatal(err)
	}
	pending := PendingRoleSourceScan{RequestID: "request-1", SourceID: "source-1", LeaseToken: "lease-1"}
	lease, err := client.RenewRoleSourceScanLease(t.Context(), "runtime-1", pending)
	if err != nil || lease.LeaseExpiresAt != "2026-08-13T12:15:00Z" {
		t.Fatalf("renew lease = %+v err=%v", lease, err)
	}
	if err := client.ReportRoleSourceScanResult(t.Context(), "runtime-1", pending, RoleSourceScanResult{
		Status: "failed", LeaseToken: "lease-1", ErrorCode: "source_invalid",
	}); err != nil {
		t.Fatal(err)
	}
	ref := rolesource.ArtifactRef{Digest: "sha256:" + strings.Repeat("a", 64), Path: "writer/agent.md", MediaType: "text/markdown", SizeBytes: 7}
	missing, err := client.CheckRoleSourceArtifacts(t.Context(), "runtime-1", pending, []rolesource.ArtifactRef{ref})
	if err != nil || len(missing) != 1 || missing[0] != ref {
		t.Fatalf("artifact preflight = %+v err=%v", missing, err)
	}
	if err := client.UploadRoleSourceArtifact(t.Context(), "runtime-1", pending, ref, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	heartbeat := requests["/api/daemon/heartbeat"]
	if heartbeat["supports_role_source_scan"] != true || heartbeat["poll_role_source_scan"] != true {
		t.Fatalf("heartbeat negotiation body = %#v", heartbeat)
	}
	leaseBody := requests["/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/lease"]
	if leaseBody["lease_token"] != "lease-1" {
		t.Fatalf("lease body = %#v", leaseBody)
	}
	result := requests["/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/result"]
	if result["status"] != "failed" || result["lease_token"] != "lease-1" || result["error_code"] != "source_invalid" {
		t.Fatalf("result body = %#v", result)
	}
	uploadPath := "/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/artifacts/" + ref.Digest
	if uploads[uploadPath] != "payload" {
		t.Fatalf("artifact upload = %q at %q", uploads[uploadPath], uploadPath)
	}
}
