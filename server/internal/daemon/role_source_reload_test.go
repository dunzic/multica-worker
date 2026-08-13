package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func roleSourceReloadTestDaemon(t *testing.T, path string, scanner *roleSourceScanner) *Daemon {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(Config{
		WorkspacesRoot: t.TempDir(), roleSourceConfigPath: path, roleSourceScanner: scanner,
	}, logger)
}

func roleSourceConfigBodyWithID(t *testing.T, path, configID string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document roleSourceConfigDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	var source RoleSourceManagedSource
	for _, candidate := range document.Sources {
		source = candidate
		break
	}
	document.Sources = map[string]RoleSourceManagedSource{configID: source}
	body, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRoleSourceConfigHotReloadPublishesGenerationAndRenegotiates(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	initial, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	d := roleSourceReloadTestDaemon(t, path, initial)
	oldAttestation, err := initial.attestationForRuntime("runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	d.acceptRoleSourceConfigAttestation("runtime-1", oldAttestation.AttestationID)
	d.roleSourceLastPoll["runtime-1"] = time.Now()

	nextBody := roleSourceConfigBodyWithID(t, path, "agentwaker-next")
	if err := writeRoleSourceConfigAtomically(path, nextBody); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	d.reloadRoleSourceConfigOnce(now)
	next := d.currentRoleSourceScanner()
	if next == nil || next == initial || next.configRevision != roleSourceConfigRevision(nextBody) {
		t.Fatalf("published scanner = %#v; initial=%p", next, initial)
	}
	if _, ok := next.configs["agentwaker-next"]; !ok {
		t.Fatalf("new generation configs = %#v", next.configs)
	}
	if len(d.roleSourceLastPoll) != 0 {
		t.Fatalf("recovery poll throttles were not reset: %#v", d.roleSourceLastPoll)
	}
	attestation := d.pendingRoleSourceConfigAttestation("runtime-1")
	if attestation == nil || attestation.AttestationID == oldAttestation.AttestationID {
		t.Fatalf("new generation was not renegotiated: %+v", attestation)
	}
	health := d.roleSourceConfigHealth()
	if health.Status != roleSourceConfigReloadStatusLoaded || health.Revision != next.configRevision ||
		health.LastAttemptAt != now.Format(time.RFC3339Nano) || health.LastSuccessfulAt != now.Format(time.RFC3339Nano) || health.ErrorCode != "" {
		t.Fatalf("reload health = %+v", health)
	}
}

func TestRoleSourceConfigHotReloadRetainsLastKnownGoodAndRecovers(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	d := roleSourceReloadTestDaemon(t, path, initial)
	failedAt := time.Date(2026, 8, 13, 12, 1, 0, 0, time.UTC)
	if err := writeRoleSourceConfigAtomically(path, []byte(`{"version":`)); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(failedAt)
	if got := d.currentRoleSourceScanner(); got != initial {
		t.Fatalf("invalid replacement discarded last-known-good scanner: got=%p want=%p", got, initial)
	}
	health := d.roleSourceConfigHealth()
	if health.Status != roleSourceConfigReloadStatusDegraded || health.Revision != initial.configRevision || health.ErrorCode != "config_invalid" || health.LastAttemptAt != failedAt.Format(time.RFC3339Nano) {
		t.Fatalf("degraded health = %+v", health)
	}

	recoveredAt := failedAt.Add(time.Minute)
	if err := writeRoleSourceConfigAtomically(path, body); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(recoveredAt)
	if got := d.currentRoleSourceScanner(); got != initial {
		t.Fatalf("same-revision recovery unnecessarily replaced scanner: got=%p want=%p", got, initial)
	}
	health = d.roleSourceConfigHealth()
	if health.Status != roleSourceConfigReloadStatusLoaded || health.ErrorCode != "" || health.LastSuccessfulAt != recoveredAt.Format(time.RFC3339Nano) {
		t.Fatalf("recovered health = %+v", health)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(recoveredAt.Add(time.Minute))
	if got := d.currentRoleSourceScanner(); got != initial {
		t.Fatal("config deletion unloaded the active generation")
	}
	if health = d.roleSourceConfigHealth(); health.Status != roleSourceConfigReloadStatusDegraded || health.ErrorCode != "file_missing" {
		t.Fatalf("deleted-file health = %+v", health)
	}
}

func TestRoleSourceConfigHotReloadEnablesPreviouslyAbsentDefault(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "role-sources.json")
	d := roleSourceReloadTestDaemon(t, path, nil)
	d.reloadRoleSourceConfigOnce(time.Now())
	if health := d.roleSourceConfigHealth(); health.Status != roleSourceConfigReloadStatusUnloaded || health.ErrorCode != "" {
		t.Fatalf("absent default health = %+v", health)
	}

	root := t.TempDir()
	seed := writeRoleSourceConfigForTest(t, root, root, 0o600)
	body, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRoleSourceConfigAtomically(path, body); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(time.Now())
	if d.currentRoleSourceScanner() == nil || d.roleSourceConfigHealth().Status != roleSourceConfigReloadStatusLoaded {
		t.Fatalf("new default config was not loaded: %+v", d.roleSourceConfigHealth())
	}
}

func TestRoleSourceConfigHotReloadReleasesCapturedGeneration(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	initial, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	d := roleSourceReloadTestDaemon(t, path, initial)
	option := d.roleSourceHeartbeatOptions("runtime-1", true)
	if !option.PollRoleSourceScan || option.roleSourceScanner != initial {
		t.Fatalf("captured heartbeat option = %+v scanner=%p", option, option.roleSourceScanner)
	}

	nextBody := roleSourceConfigBodyWithID(t, path, "agentwaker-next")
	if err := writeRoleSourceConfigAtomically(path, nextBody); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(time.Now())
	d.releaseRoleSourcePollReservation(option)
	if len(initial.semaphore) != 0 {
		t.Fatalf("old generation reservation leaked: %d", len(initial.semaphore))
	}
	if len(d.currentRoleSourceScanner().semaphore) != 0 {
		t.Fatal("release was applied to the new generation")
	}
}

func TestRoleSourceConfigHotReloadIsRaceSafe(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	initial, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	bodyA, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bodyB := roleSourceConfigBodyWithID(t, path, "agentwaker-next")
	d := roleSourceReloadTestDaemon(t, path, initial)

	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			for iteration := 0; iteration < 100; iteration++ {
				option := d.roleSourceHeartbeatOptions("runtime-race", true)
				d.acceptRoleSourceConfigAttestation("runtime-race", "sha256:invalid")
				d.releaseRoleSourcePollReservation(option)
				_ = d.roleSourceConfigHealth()
			}
		}(worker)
	}
	for iteration := 0; iteration < 50; iteration++ {
		body := bodyA
		if iteration%2 == 1 {
			body = bodyB
		}
		if err := writeRoleSourceConfigAtomically(path, body); err != nil {
			t.Fatal(err)
		}
		d.reloadRoleSourceConfigOnce(time.Now())
	}
	workers.Wait()
}

func TestRoleSourceConfigHealthEndpointExposesNoLocalAuthority(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	scanner, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	d := roleSourceReloadTestDaemon(t, path, scanner)
	recorder := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(recorder, httptest.NewRequest("GET", "/health", nil))
	var response HealthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RoleSourceConfig == nil || response.RoleSourceConfig.Status != roleSourceConfigReloadStatusLoaded || response.RoleSourceConfig.Revision != scanner.configRevision {
		t.Fatalf("role-source health = %+v", response.RoleSourceConfig)
	}
	body := recorder.Body.String()
	if contains := []string{root, path, "agentwaker-main", "0123456789abcdef"}; func() bool {
		for _, secret := range contains {
			if secret != "" && strings.Contains(body, secret) {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("health response exposed local authority: %s", body)
	}
}

func TestRoleSourceConfigReloadLogsOnlyBoundedFailureCode(t *testing.T) {
	root := t.TempDir()
	path := writeRoleSourceConfigForTest(t, root, root, 0o600)
	scanner, err := loadRoleSourceScanner(path)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	d := New(Config{
		WorkspacesRoot: t.TempDir(), roleSourceConfigPath: path, roleSourceScanner: scanner,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d.reloadRoleSourceConfigOnce(time.Now())
	output := logs.String()
	if !strings.Contains(output, "error_code=file_missing") {
		t.Fatalf("bounded error code missing from log: %s", output)
	}
	for _, private := range []string{path, root, "agentwaker-main", scanner.configRevision} {
		if strings.Contains(output, private) {
			t.Fatalf("reload log exposed local authority %q: %s", private, output)
		}
	}
}
