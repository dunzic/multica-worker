package daemon

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/rolesource/agentwaker"
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
	"github.com/multica-ai/multica/server/internal/rolesource/signedremote"
)

func managedRoleSourceBody(t *testing.T, allowedRoot string, sources map[string]RoleSourceManagedSource) []byte {
	t.Helper()
	body, err := json.Marshal(RoleSourceManagedDocument{Version: 1, AllowedRoots: []string{allowedRoot}, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func managedManifestSource(t *testing.T, root string) RoleSourceManagedSource {
	t.Helper()
	config, err := json.Marshal(map[string]string{"root_path": root})
	if err != nil {
		t.Fatal(err)
	}
	return RoleSourceManagedSource{Kind: manifestdir.Kind, Config: config}
}

func managedAgentWakerSource(t *testing.T, root string) RoleSourceManagedSource {
	t.Helper()
	config, err := json.Marshal(map[string]string{"root_path": root})
	if err != nil {
		t.Fatal(err)
	}
	return RoleSourceManagedSource{Kind: agentwaker.Kind, Config: config}
}

func readManagedPrivateDocument(t *testing.T, path string) roleSourceConfigDocument {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := decodeRoleSourceConfigDocument(body)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestManagedRoleSourceConfigLifecycleKeepsSecretsPrivate(t *testing.T) {
	configDir := t.TempDir()
	allowedRoot := t.TempDir()
	manifestRoot := filepath.Join(allowedRoot, "catalog")
	agentWakerRoot := filepath.Join(allowedRoot, "agentwaker")
	for _, root := range []string{manifestRoot, agentWakerRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(configDir, "role-sources.json")

	manifestOnly := managedRoleSourceBody(t, allowedRoot, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, manifestRoot),
	})
	created, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, manifestOnly)
	if err != nil {
		t.Fatalf("create managed config: %v", err)
	}
	if created.DigestKeyActive || created.SourceCount != 1 || created.ConfigFileName != "role-sources.json" {
		t.Fatalf("created summary = %+v", created)
	}
	encodedSummary, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedSummary), allowedRoot) || strings.Contains(string(encodedSummary), manifestRoot) {
		t.Fatalf("summary leaked absolute path: %s", encodedSummary)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}

	shown, err := ShowRoleSourceConfig(configPath)
	if err != nil {
		t.Fatalf("show managed config: %v", err)
	}
	if shown.Revision != created.Revision {
		t.Fatalf("show revision = %q, want %q", shown.Revision, created.Revision)
	}

	withAgentWaker := managedRoleSourceBody(t, allowedRoot, map[string]RoleSourceManagedSource{
		"agentwaker-main": managedAgentWakerSource(t, agentWakerRoot),
		"manifest-main":   managedManifestSource(t, manifestRoot),
	})
	updated, err := ApplyRoleSourceManagedConfig(configPath, created.Revision, withAgentWaker)
	if err != nil {
		t.Fatalf("add AgentWaker source: %v", err)
	}
	if !updated.DigestKeyActive || updated.Revision == created.Revision {
		t.Fatalf("updated summary = %+v", updated)
	}
	private := readManagedPrivateDocument(t, configPath)
	if len(private.DigestKey) != 32 {
		t.Fatalf("digest key length = %d, want 32", len(private.DigestKey))
	}
	firstKey := append([]byte(nil), private.DigestKey...)

	preserved, err := ApplyRoleSourceManagedConfig(configPath, updated.Revision, withAgentWaker)
	if err != nil {
		t.Fatalf("reapply AgentWaker config: %v", err)
	}
	private = readManagedPrivateDocument(t, configPath)
	if string(private.DigestKey) != string(firstKey) {
		t.Fatal("ordinary apply rotated the private digest key")
	}

	removed, err := ApplyRoleSourceManagedConfig(configPath, preserved.Revision, manifestOnly)
	if err != nil {
		t.Fatalf("remove AgentWaker source: %v", err)
	}
	if removed.DigestKeyActive {
		t.Fatal("manifest-only config retained an unnecessary digest key")
	}
	private = readManagedPrivateDocument(t, configPath)
	if len(private.DigestKey) != 0 {
		t.Fatalf("persisted digest key length = %d, want 0", len(private.DigestKey))
	}
}

func TestManagedRoleSourceConfigCASAndExplicitRotation(t *testing.T) {
	configDir := t.TempDir()
	root := t.TempDir()
	configPath := filepath.Join(configDir, "role-sources.json")
	desired := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"agentwaker-main": managedAgentWakerSource(t, root),
	})
	created, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired)
	if err != nil {
		t.Fatal(err)
	}
	before := readManagedPrivateDocument(t, configPath)

	if _, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired); !errors.Is(err, ErrRoleSourceConfigConflict) {
		t.Fatalf("stale apply error = %v, want revision conflict", err)
	}
	afterConflict := readManagedPrivateDocument(t, configPath)
	if string(afterConflict.DigestKey) != string(before.DigestKey) {
		t.Fatal("stale apply changed the config")
	}

	rotated, err := RotateRoleSourceDigestKey(configPath, created.Revision)
	if err != nil {
		t.Fatalf("rotate digest key: %v", err)
	}
	if !rotated.RescanRequired || rotated.Revision == created.Revision {
		t.Fatalf("rotation summary = %+v", rotated)
	}
	afterRotation := readManagedPrivateDocument(t, configPath)
	if string(afterRotation.DigestKey) == string(before.DigestKey) || len(afterRotation.DigestKey) != 32 {
		t.Fatal("rotation did not replace the 32-byte digest key")
	}
	if _, err := RotateRoleSourceDigestKey(configPath, created.Revision); !errors.Is(err, ErrRoleSourceConfigConflict) {
		t.Fatalf("stale rotation error = %v, want revision conflict", err)
	}
}

func TestManagedRoleSourceConfigRejectsUnsafeInputAndTarget(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	desired := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, root),
	})

	for name, body := range map[string][]byte{
		"private key injection": []byte(`{"version":1,"digest_key":"c2VjcmV0","allowed_roots":[],"sources":{}}`),
		"unknown field":         []byte(`{"version":1,"allowed_roots":[],"sources":{},"surprise":true}`),
		"trailing JSON":         append(append([]byte(nil), desired...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, body); err == nil {
				t.Fatal("unsafe managed document was accepted")
			}
		})
	}

	outside := t.TempDir()
	badRoot := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, outside),
	})
	if _, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, badRoot); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside root error = %v", err)
	}

	linkPath := filepath.Join(t.TempDir(), "role-sources.json")
	if err := os.Symlink(configPath, linkPath); err == nil {
		if _, err := ApplyRoleSourceManagedConfig(linkPath, RoleSourceConfigAbsentRevision, desired); err == nil {
			t.Fatal("symlink target was accepted")
		}
	}
}

func TestManagedRoleSourceConfigShowSupportsRecoveryFromInvalidState(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	missingRoot := filepath.Join(t.TempDir(), "missing")
	privateDocument := roleSourceConfigDocument{
		Version: 1, AllowedRoots: []string{missingRoot},
		Sources: map[string]RoleSourceManagedSource{"manifest-main": managedManifestSource(t, missingRoot)},
	}
	body, err := json.Marshal(privateDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := ShowRoleSourceConfig(configPath)
	if err != nil {
		t.Fatalf("show missing-root config: %v", err)
	}
	if summary.ValidationStatus != "invalid" || summary.ValidationCode != "invalid_allowed_roots" || summary.Revision == "" {
		t.Fatalf("missing-root summary = %+v", summary)
	}

	if err := os.WriteFile(configPath, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	malformed, err := ShowRoleSourceConfig(configPath)
	if err != nil {
		t.Fatalf("show malformed config: %v", err)
	}
	if malformed.ValidationCode != "invalid_document" || malformed.Revision == "" {
		t.Fatalf("malformed summary = %+v", malformed)
	}

	validRoot := t.TempDir()
	desired := managedRoleSourceBody(t, validRoot, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, validRoot),
	})
	recovered, err := ApplyRoleSourceManagedConfig(configPath, malformed.Revision, desired)
	if err != nil {
		t.Fatalf("recover malformed config: %v", err)
	}
	if recovered.ValidationStatus != "valid" || recovered.ValidationCode != "" {
		t.Fatalf("recovered summary = %+v", recovered)
	}
}

func TestManagedRoleSourceConfigLockAndAtomicPublishFailure(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	desired := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, root),
	})

	lock, err := openAndLockRoleSourceConfig(configPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired); !errors.Is(err, ErrRoleSourceConfigBusy) {
		t.Fatalf("contended apply error = %v, want busy", err)
	}
	unlockRoleSourceConfig(lock)
	created, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired)
	if err != nil {
		t.Fatalf("initial publish: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	originalReplace := replaceRoleSourceConfigFile
	replaceRoleSourceConfigFile = func(_, _ string) error { return errors.New("injected replace failure") }
	t.Cleanup(func() { replaceRoleSourceConfigFile = originalReplace })
	changed := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-renamed": managedManifestSource(t, root),
	})
	if _, err := ApplyRoleSourceManagedConfig(configPath, created.Revision, changed); err == nil || !strings.Contains(err.Error(), "injected replace failure") {
		t.Fatalf("publish failure error = %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed atomic update changed the last-known-good target")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(configPath), ".role-source-config-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary config files remain: %v", temps)
	}
}

func TestManagedRoleSourceConfigConcurrentCreateHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	desired := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, root),
	})
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for range writers {
		go func() {
			<-start
			_, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired)
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range writers {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrRoleSourceConfigBusy) && !errors.Is(err, ErrRoleSourceConfigConflict) {
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent create successes = %d, want 1", successes)
	}
	if _, err := ShowRoleSourceConfig(configPath); err != nil {
		t.Fatalf("show winning config: %v", err)
	}
}

func TestLoadConfigDiscoversManagedRoleSourceConfigForProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MULTICA_ROLE_SOURCE_CONFIG_FILE", "")
	profileDir := filepath.Join(home, ".multica", "profiles", "production")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	configPath, err := DefaultRoleSourceConfigPath("production")
	if err != nil {
		t.Fatal(err)
	}
	desired := managedRoleSourceBody(t, root, map[string]RoleSourceManagedSource{
		"manifest-main": managedManifestSource(t, root),
	})
	if _, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired); err != nil {
		t.Fatalf("create profile config: %v", err)
	}
	stageFakeAgent(t)
	cfg, err := LoadConfig(Overrides{
		Profile: "production", ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("load daemon config: %v", err)
	}
	if cfg.roleSourceScanner == nil {
		t.Fatal("profile-managed role source config was not discovered")
	}
}

func TestManagedSignedRemoteSourceNeedsNoFilesystemRootAndRedactsTrustConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "role-sources.json")
	remoteConfig, err := json.Marshal(map[string]any{
		"bundle_url":        "https://publisher.example/bundle.json",
		"artifact_base_url": "https://publisher.example/artifacts/",
		"issuer":            "publisher.example",
		"public_keys": map[string]string{
			"primary": base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := json.Marshal(RoleSourceManagedDocument{
		Version: 1, AllowedRoots: []string{},
		Sources: map[string]RoleSourceManagedSource{
			"remote-main": {Kind: signedremote.Kind, Config: remoteConfig},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := ApplyRoleSourceManagedConfig(configPath, RoleSourceConfigAbsentRevision, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.AllowedRootNames) != 0 || summary.DigestKeyActive || len(summary.Sources) != 1 ||
		summary.Sources[0].Kind != signedremote.Kind || len(summary.Sources[0].Attributes) != 3 {
		t.Fatalf("signed remote summary=%+v", summary)
	}
	encoded, _ := json.Marshal(summary)
	if strings.Contains(string(encoded), "bundle.json") || strings.Contains(string(encoded), "artifacts/") || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))) {
		t.Fatalf("summary exposed signed remote trust config: %s", encoded)
	}
	scanner, err := loadRoleSourceScanner(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor, ok := scanner.registry.Descriptor(signedremote.Kind); !ok || descriptor.AdapterVersion != signedremote.Descriptor().AdapterVersion {
		t.Fatalf("signed remote descriptor=%+v available=%t", descriptor, ok)
	}
	attestation, err := scanner.attestationForRuntime("runtime-remote")
	if err != nil {
		t.Fatal(err)
	}
	if len(attestation.Sources) != 1 || attestation.Sources[0].Kind != string(signedremote.Kind) ||
		attestation.Sources[0].AdapterVersion != signedremote.Descriptor().AdapterVersion {
		t.Fatalf("signed remote attestation=%+v", attestation)
	}
}
