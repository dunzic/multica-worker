package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type codedScanFailureAdapter struct{}

func (codedScanFailureAdapter) Descriptor() rolesource.Descriptor {
	return rolesource.Descriptor{
		Kind: "coded_scan_failure", DisplayName: "Coded scan failure", AdapterVersion: "1.0.0",
		ContractVersion: rolesource.ContractVersion,
	}
}
func (codedScanFailureAdapter) ValidateConfig(json.RawMessage) error { return nil }
func (codedScanFailureAdapter) RedactConfig(json.RawMessage) (rolesource.ConfigSummary, error) {
	return rolesource.ConfigSummary{Configured: true}, nil
}
func (codedScanFailureAdapter) Scan(context.Context, rolesource.ScanRequest) (rolesource.ScanOutput, error) {
	return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, io.ErrUnexpectedEOF)
}

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
	body, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	attestation, err := scanner.attestationForRuntime("runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	expectedRevision, err := protocol.RoleSourceConfigRevisionDigest("runtime-1", roleSourceConfigRevision(body))
	if err != nil {
		t.Fatal(err)
	}
	if !attestation.Loaded || attestation.Revision != expectedRevision || len(attestation.Sources) != 1 {
		t.Fatalf("loaded attestation = %+v", attestation)
	}
	expectedConfigDigest, err := protocol.RoleSourceConfigIDDigest("runtime-1", "agentwaker-main")
	if err != nil {
		t.Fatal(err)
	}
	if source := attestation.Sources[0]; source.ConfigIDDigest != expectedConfigDigest || source.Kind != string(agentwaker.Kind) || source.AdapterVersion != agentwaker.Descriptor().AdapterVersion {
		t.Fatalf("attested source = %+v", source)
	}
	if err := protocol.ValidateRoleSourceConfigAttestation(attestation); err != nil {
		t.Fatalf("loaded attestation invalid: %v", err)
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

	missingKeyBody, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	var missingKey map[string]any
	if err := json.Unmarshal(missingKeyBody, &missingKey); err != nil {
		t.Fatal(err)
	}
	delete(missingKey, "digest_key")
	missingKeyBody, err = json.Marshal(missingKey)
	if err != nil {
		t.Fatal(err)
	}
	missingKeyPath := filepath.Join(t.TempDir(), "role-sources.json")
	if err := os.WriteFile(missingKeyPath, missingKeyBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoleSourceScanner(missingKeyPath); err == nil || !strings.Contains(err.Error(), "when AgentWaker is configured") {
		t.Fatalf("AgentWaker without digest key error=%v", err)
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

func TestLoadConfigRejectsExplicitMissingRoleSourceConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixture is unavailable on Windows")
	}
	stageFakeAgent(t)
	missing := filepath.Join(t.TempDir(), "missing-role-sources.json")
	t.Setenv("MULTICA_ROLE_SOURCE_CONFIG_FILE", missing)
	if _, err := LoadConfig(Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("explicit missing config error = %v", err)
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

func TestRoleSourceScannerReturnsClosedAdapterFailureCode(t *testing.T) {
	adapter := codedScanFailureAdapter{}
	registry, err := rolesource.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	scanner := &roleSourceScanner{
		registry: registry,
		configs: map[string]roleSourceLocalConfig{
			"remote-main": {Kind: adapter.Descriptor().Kind, Config: json.RawMessage(`{}`)},
		},
	}
	pending := protocol.DaemonHeartbeatPendingRoleSourceScan{
		WorkspaceID: "workspace-1", SourceID: "source-1", DaemonConfigID: "remote-main",
		Kind: string(adapter.Descriptor().Kind), AdapterVersion: adapter.Descriptor().AdapterVersion,
	}
	if _, code := scanner.scan(t.Context(), pending); code != rolesource.ScanFailureRemoteTrustInvalid {
		t.Fatalf("coded adapter failure=%q", code)
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
		"writer/env/.env":                     "API_TOKEN=super-secret-token\n",
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
	keyPair, err := rolesource.NewSecretEnvelopeKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(keyPair.PrivateKey)
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	secretPending := protocol.DaemonHeartbeatPendingRoleSourceSecretTransfer{
		TransferID: "00000000-0000-4000-8000-000000000063", SourceID: pending.SourceID, WorkspaceID: pending.WorkspaceID,
		Kind: pending.Kind, AdapterVersion: pending.AdapterVersion, DaemonConfigID: pending.DaemonConfigID,
		RoleID: "writer", SnapshotDigest: snapshot.SnapshotDigest, ContractVersion: rolesource.SecretEnvelopeContractVersion,
		PublicKey: keyPair.PublicKey, ExpiresAt: expiresAt,
	}
	envelope, code := scanner.sealSecretTransfer(t.Context(), secretPending)
	if code != "" {
		t.Fatalf("secret transfer failed with code %q", code)
	}
	payload, err := rolesource.OpenSecretEnvelope(keyPair.PrivateKey, envelope, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer rolesource.ClearSecretEnvelopePayload(&payload)
	if payload.Environment["API_TOKEN"] != "super-secret-token" {
		t.Fatalf("decrypted transfer environment keys = %v", payload.Environment)
	}
	if envelope.Claims.SnapshotDigest != snapshot.SnapshotDigest || envelope.Claims.RoleID != "writer" {
		t.Fatalf("envelope claims = %+v", envelope.Claims)
	}
}

func TestRoleSourceScannerRunsManifestDirectoryThroughSameGenericRegistry(t *testing.T) {
	root := t.TempDir()
	artifactBody := []byte("# Standard role\n")
	artifactPath := "roles/standard.md"
	if err := os.MkdirAll(filepath.Join(root, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(artifactPath)), artifactBody, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(artifactBody)
	ref := rolesource.ArtifactRef{Path: artifactPath, Digest: "sha256:" + hex.EncodeToString(sum[:]), MediaType: "text/markdown", SizeBytes: int64(len(artifactBody))}
	manifest := rolesource.Manifest{
		ContractVersion: rolesource.ContractVersion,
		Roles: []rolesource.Role{{
			ID: "standard", DisplayName: "Standard", Version: "1.0.0", Lifecycle: "active", Instructions: ref,
			Skills: []rolesource.Skill{}, CapabilityBindings: []rolesource.CapabilityBinding{}, Environment: []rolesource.EnvironmentKey{},
			MCP: []rolesource.MCPServer{}, Automations: []rolesource.Automation{},
		}},
		Capabilities: []rolesource.Capability{},
	}
	manifestBody, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "multica-role-source.json"), manifestBody, 0o600); err != nil {
		t.Fatal(err)
	}
	document := roleSourceConfigDocument{
		Version: roleSourceConfigVersion, AllowedRoots: []string{root},
		Sources: map[string]roleSourceLocalConfig{
			"standard-main": {Kind: manifestdir.Kind, Config: json.RawMessage(`{"root_path":` + mustJSONRoleSourceTest(t, root) + `}`)},
		},
	}
	configBody, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	if err := os.WriteFile(configPath, configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	scanner, err := loadRoleSourceScanner(configPath)
	if err != nil {
		t.Fatal(err)
	}
	pending := protocol.DaemonHeartbeatPendingRoleSourceScan{
		WorkspaceID: "00000000-0000-4000-8000-000000000061", SourceID: "00000000-0000-4000-8000-000000000062",
		Kind: string(manifestdir.Kind), AdapterVersion: manifestdir.Descriptor().AdapterVersion, DaemonConfigID: "standard-main",
	}
	snapshot, code := scanner.scan(t.Context(), pending)
	if code != "" || snapshot.Kind != manifestdir.Kind || len(snapshot.Manifest.Roles) != 1 || snapshot.Manifest.Roles[0].ID != "standard" {
		t.Fatalf("manifest-directory scan snapshot=%+v code=%q", snapshot, code)
	}
	reader, err := scanner.openArtifact(t.Context(), pending, ref)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(reader)
	reader.Close() //nolint:errcheck
	if err != nil || string(opened) != string(artifactBody) {
		t.Fatalf("manifest-directory artifact=%q err=%v", opened, err)
	}
}

func TestRoleSourceHeartbeatPollingIsThrottledAndCapacityAware(t *testing.T) {
	root := t.TempDir()
	scanner, err := loadRoleSourceScanner(writeRoleSourceConfigForTest(t, root, root, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &Daemon{roleSourceLastPoll: make(map[string]time.Time)}
	daemon.roleSources.Store(scanner)
	attestation, err := scanner.attestationForRuntime("runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	first := daemon.roleSourceHeartbeatOptions("runtime-1", false)
	second := daemon.roleSourceHeartbeatOptions("runtime-1", false)
	forced := daemon.roleSourceHeartbeatOptions("runtime-1", true)
	if !first.SupportsRoleSourceScan || !first.SupportsRoleSourceSecretTransfer || !first.PollRoleSourceScan || !first.PollRoleSourceSecretTransfer || second.PollRoleSourceScan || second.PollRoleSourceSecretTransfer || !forced.PollRoleSourceScan || !forced.PollRoleSourceSecretTransfer {
		t.Fatalf("poll options: first=%+v second=%+v forced=%+v", first, second, forced)
	}
	if !first.SupportsRoleSourceConfigAttestation || first.RoleSourceConfigAttestation == nil || first.RoleSourceConfigAttestation.AttestationID != attestation.AttestationID {
		t.Fatalf("first attestation negotiation = %+v", first)
	}
	daemon.acceptRoleSourceConfigAttestation("runtime-1", attestation.AttestationID)
	if option := daemon.roleSourceHeartbeatOptions("runtime-1", false); option.RoleSourceConfigAttestation != nil {
		t.Fatalf("acknowledged attestation was resent: %+v", option.RoleSourceConfigAttestation)
	}
	daemon.acceptRoleSourceConfigAttestation("runtime-2", "sha256:"+strings.Repeat("f", 64))
	if option := daemon.roleSourceHeartbeatOptions("runtime-2", false); option.RoleSourceConfigAttestation == nil {
		t.Fatal("mismatched acknowledgement suppressed attestation")
	}
	if option := daemon.roleSourceHeartbeatOptions("runtime-2", true); !option.SupportsRoleSourceScan || !option.SupportsRoleSourceSecretTransfer || option.PollRoleSourceScan || option.PollRoleSourceSecretTransfer {
		t.Fatalf("capacity-full option = %+v", option)
	}
	daemon.handleHeartbeatActions(t.Context(), "runtime-1", &HeartbeatResponse{}, first)
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

	attestation, err := protocol.NewRoleSourceConfigAttestation(false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendHeartbeat(t.Context(), "runtime-1", HeartbeatOptions{
		SupportsRoleSourceScan: true, PollRoleSourceScan: true,
		SupportsRoleSourceSecretTransfer: true, PollRoleSourceSecretTransfer: true,
		SupportsRoleSourceConfigAttestation: true, RoleSourceConfigAttestation: &attestation,
	}); err != nil {
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
	secretPending := PendingRoleSourceSecretTransfer{TransferID: "transfer-1", SourceID: "source-1", LeaseToken: "lease-2"}
	if err := client.ReportRoleSourceSecretTransferResult(t.Context(), "runtime-1", secretPending, RoleSourceSecretTransferResult{
		Status: "failed", LeaseToken: "lease-2", ErrorCode: "snapshot_changed",
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
	if heartbeat["supports_role_source_scan"] != true || heartbeat["poll_role_source_scan"] != true ||
		heartbeat["supports_role_source_secret_transfer"] != true || heartbeat["poll_role_source_secret_transfer"] != true ||
		heartbeat["supports_role_source_config_attestation"] != true {
		t.Fatalf("heartbeat negotiation body = %#v", heartbeat)
	}
	attestedBody, ok := heartbeat["role_source_config_attestation"].(map[string]any)
	if !ok || attestedBody["loaded"] != false || attestedBody["attestation_id"] != attestation.AttestationID {
		t.Fatalf("heartbeat attestation body = %#v", heartbeat["role_source_config_attestation"])
	}
	leaseBody := requests["/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/lease"]
	if leaseBody["lease_token"] != "lease-1" {
		t.Fatalf("lease body = %#v", leaseBody)
	}
	result := requests["/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/result"]
	if result["status"] != "failed" || result["lease_token"] != "lease-1" || result["error_code"] != "source_invalid" {
		t.Fatalf("result body = %#v", result)
	}
	secretResult := requests["/api/daemon/runtimes/runtime-1/role-sources/source-1/secret-transfers/transfer-1/result"]
	if secretResult["status"] != "failed" || secretResult["lease_token"] != "lease-2" || secretResult["error_code"] != "snapshot_changed" {
		t.Fatalf("secret result body = %#v", secretResult)
	}
	uploadPath := "/api/daemon/runtimes/runtime-1/role-sources/source-1/scans/request-1/artifacts/" + ref.Digest
	if uploads[uploadPath] != "payload" {
		t.Fatalf("artifact upload = %q at %q", uploads[uploadPath], uploadPath)
	}
}
