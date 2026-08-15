package signedremote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestSignedRemoteScanAndArtifactUsePinnedCommitments(t *testing.T) {
	fixture := newSignedFixture(t)
	registry, err := rolesource.NewRegistry(fixture.adapter)
	if err != nil {
		t.Fatal(err)
	}
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: fixture.config}
	snapshot, err := registry.Scan(t.Context(), Kind, request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceEvidence.Issuer != "publisher.example" || snapshot.SourceEvidence.KeyID != "primary" || snapshot.SourceEvidence.Revision != "release-2026.08.13" ||
		snapshot.SourceEvidence.SignatureDigest == "" || snapshot.SourceEvidence.TreeDigest != fixture.treeDigest {
		t.Fatalf("source evidence=%+v", snapshot.SourceEvidence)
	}
	reader, err := registry.OpenArtifact(t.Context(), Kind, request, fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(reader)
	reader.Close() //nolint:errcheck
	if readErr != nil || string(body) != string(fixture.artifact) {
		t.Fatalf("artifact=%q err=%v", body, readErr)
	}
	summary, err := registry.RedactConfig(Kind, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(summary)
	if strings.Contains(string(encoded), base64.StdEncoding.EncodeToString(fixture.publicKey)) || strings.Contains(string(encoded), "bundle.json") {
		t.Fatalf("redacted summary exposed authority details: %s", encoded)
	}
}

func TestSignedRemoteRejectsTamperWrongKeyAndChangedArtifact(t *testing.T) {
	fixture := newSignedFixture(t)
	request := rolesource.ScanRequest{WorkspaceID: "workspace-1", SourceID: "source-1", Config: fixture.config}

	var bundle signedBundle
	if err := json.Unmarshal(fixture.bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.Manifest.Roles[0].DisplayName = "Tampered"
	tampered, _ := json.Marshal(bundle)
	fixture.responses["https://publisher.example/bundle.json"] = tampered
	if _, err := fixture.adapter.Scan(t.Context(), request); err == nil || !strings.Contains(err.Error(), "digest commitment") {
		t.Fatalf("tampered bundle error=%v", err)
	} else if code, ok := rolesource.ScanFailureCode(err); !ok || code != rolesource.ScanFailureRemoteTrustInvalid {
		t.Fatalf("tampered bundle safe code=%q ok=%t", code, ok)
	}

	fixture.responses["https://publisher.example/bundle.json"] = fixture.bundle
	_, otherPrivate, _ := ed25519.GenerateKey(rand.Reader)
	otherSignature := ed25519.Sign(otherPrivate, fixture.message)
	bundle.Manifest = fixture.manifest
	bundle.Signature = base64.StdEncoding.EncodeToString(otherSignature)
	wrongSignature, _ := json.Marshal(bundle)
	fixture.responses["https://publisher.example/bundle.json"] = wrongSignature
	if _, err := fixture.adapter.Scan(t.Context(), request); err == nil || !strings.Contains(err.Error(), "signature verification") {
		t.Fatalf("wrong signature error=%v", err)
	}

	fixture.responses["https://publisher.example/artifacts/role.md"] = []byte("changed")
	if _, err := fixture.adapter.OpenArtifact(t.Context(), request, fixture.ref); !errors.Is(err, rolesource.ErrChangedDuringRead) {
		t.Fatalf("changed artifact error=%v", err)
	}
	delete(fixture.responses, "https://publisher.example/artifacts/role.md")
	if _, err := fixture.adapter.OpenArtifact(t.Context(), request, fixture.ref); err == nil {
		t.Fatal("missing remote artifact was accepted")
	} else if code, ok := rolesource.ScanFailureCode(err); !ok || code != rolesource.ScanFailureRemoteUnavailable {
		t.Fatalf("missing artifact safe code=%q ok=%t err=%v", code, ok, err)
	}
}

func TestSignedRemoteTrustSetSupportsStagedKeyRotation(t *testing.T) {
	fixture := newSignedFixture(t)
	secondaryPublic, secondaryPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var configBody map[string]any
	if err := json.Unmarshal(fixture.config, &configBody); err != nil {
		t.Fatal(err)
	}
	configBody["public_keys"] = map[string]string{
		"primary": base64.StdEncoding.EncodeToString(fixture.publicKey),
		"next":    base64.StdEncoding.EncodeToString(secondaryPublic),
	}
	fixture.config, _ = json.Marshal(configBody)

	var bundle signedBundle
	if err := json.Unmarshal(fixture.bundle, &bundle); err != nil {
		t.Fatal(err)
	}
	bundle.KeyID = "next"
	message, err := SignatureMessage(bundle.Issuer, bundle.KeyID, bundle.Revision, bundle.ManifestDigest, bundle.TreeDigest)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(secondaryPrivate, message))
	fixture.responses["https://publisher.example/bundle.json"], _ = json.Marshal(bundle)

	snapshot, err := fixture.adapter.Scan(t.Context(), rolesource.ScanRequest{Config: fixture.config})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SourceEvidence.KeyID != "next" {
		t.Fatalf("rotated source evidence=%+v", snapshot.SourceEvidence)
	}
	bundle.KeyID = "retired"
	fixture.responses["https://publisher.example/bundle.json"], _ = json.Marshal(bundle)
	if _, err := fixture.adapter.Scan(t.Context(), rolesource.ScanRequest{Config: fixture.config}); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("retired key error=%v", err)
	}
}

func TestSignedRemoteConfigAndNetworkBoundaryFailClosed(t *testing.T) {
	adapter, err := newWithClient(&http.Client{Timeout: time.Second, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network must not be reached during validation")
	})})
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	valid := `{"bundle_url":"https://publisher.example/bundle.json","artifact_base_url":"https://publisher.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`
	if err := adapter.ValidateConfig(json.RawMessage(valid)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"bundle_url":"http://publisher.example/bundle.json","artifact_base_url":"https://publisher.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		`{"bundle_url":"https://127.0.0.1/bundle.json","artifact_base_url":"https://127.0.0.1/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		`{"bundle_url":"https://user@publisher.example/bundle.json","artifact_base_url":"https://publisher.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		`{"bundle_url":"https://publisher.example/bundle.json?token=x","artifact_base_url":"https://publisher.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		`{"bundle_url":"https://publisher.example/bundle.json","artifact_base_url":"https://cdn.example/artifacts/","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		`{"bundle_url":"https://publisher.example/bundle.json","artifact_base_url":"https://publisher.example/artifacts","issuer":"publisher.example","public_keys":{"primary":"` + key + `"}}`,
		valid[:len(valid)-1] + `,"unknown":true}`,
		valid + `{}`,
	} {
		if err := adapter.ValidateConfig(json.RawMessage(invalid)); err == nil {
			t.Fatalf("unsafe config accepted: %s", invalid)
		}
	}
	for _, private := range []string{
		"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.1.1", "192.0.2.1", "198.18.0.1", "203.0.113.1",
		"::1", "fc00::1", "100::1", "2001:db8::1", "::",
	} {
		if isPublicIP(netip.MustParseAddr(private)) {
			t.Fatalf("non-public address accepted: %s", private)
		}
	}
	if !isPublicIP(netip.MustParseAddr("8.8.8.8")) || !isPublicIP(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public address rejected")
	}
	secure := secureHTTPClient()
	transport, ok := secure.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || !transport.DisableCompression || secure.CheckRedirect == nil || secure.Timeout <= 0 {
		t.Fatalf("secure remote transport is not fail-closed: client=%+v transport=%+v", secure, transport)
	}
	redirectRequest, _ := http.NewRequest(http.MethodGet, "https://publisher.example/next", nil)
	if !errors.Is(secure.CheckRedirect(redirectRequest, nil), http.ErrUseLastResponse) {
		t.Fatal("signed remote client accepted redirect")
	}
}

func TestSignedRemoteRejectsAuthorityItDoesNotAdvertise(t *testing.T) {
	fixture := newSignedFixture(t)
	manifest := fixture.manifest
	manifest.Roles = append([]rolesource.Role(nil), manifest.Roles...)
	manifest.Roles[0].Environment = []rolesource.EnvironmentKey{{
		Name: "TOKEN", Secret: true, Configured: true, ValueDigest: "hmac-sha256:" + strings.Repeat("a", 64),
	}}
	if err := validateReadOnlyManifest(manifest); err == nil || !strings.Contains(err.Error(), "secret-transfer authority") {
		t.Fatalf("configured secret error=%v", err)
	}
}

type signedFixture struct {
	adapter    *Adapter
	config     json.RawMessage
	manifest   rolesource.Manifest
	ref        rolesource.ArtifactRef
	artifact   []byte
	bundle     []byte
	message    []byte
	treeDigest string
	publicKey  ed25519.PublicKey
	responses  map[string][]byte
}

func newSignedFixture(t *testing.T) *signedFixture {
	t.Helper()
	artifact := []byte("# signed role\n")
	sum := sha256.Sum256(artifact)
	ref := rolesource.ArtifactRef{
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Path: "role.md", MediaType: "text/markdown", SizeBytes: int64(len(artifact)),
	}
	manifest := rolesource.Manifest{
		ContractVersion: rolesource.ContractVersion,
		Roles: []rolesource.Role{{
			ID: "writer", DisplayName: "Writer", Instructions: ref,
			Skills: []rolesource.Skill{}, CapabilityBindings: []rolesource.CapabilityBinding{},
			Environment: []rolesource.EnvironmentKey{}, MCP: []rolesource.MCPServer{}, Automations: []rolesource.Automation{},
		}},
		Capabilities: []rolesource.Capability{},
	}
	canonical, manifestDigest, err := rolesource.CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	treeDigest, err := ArtifactTreeDigest(canonical)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	message, err := SignatureMessage("publisher.example", "primary", "release-2026.08.13", manifestDigest, treeDigest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, message)
	bundle, err := json.Marshal(signedBundle{
		Version: bundleVersion, Issuer: "publisher.example", KeyID: "primary", Revision: "release-2026.08.13",
		ManifestDigest: manifestDigest, TreeDigest: treeDigest, Manifest: canonical,
		Signature: base64.StdEncoding.EncodeToString(signature),
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := map[string][]byte{
		"https://publisher.example/bundle.json":       bundle,
		"https://publisher.example/artifacts/role.md": artifact,
	}
	client := &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, ok := responses[request.URL.String()]
		if !ok {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing")), Request: request}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(string(body))), Request: request,
		}, nil
	})}
	adapter, err := newWithClient(client)
	if err != nil {
		t.Fatal(err)
	}
	configBody, err := json.Marshal(map[string]any{
		"bundle_url": "https://publisher.example/bundle.json", "artifact_base_url": "https://publisher.example/artifacts/",
		"issuer": "publisher.example", "public_keys": map[string]string{"primary": base64.StdEncoding.EncodeToString(publicKey)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &signedFixture{
		adapter: adapter, config: configBody, manifest: canonical, ref: ref, artifact: artifact,
		bundle: bundle, message: message, treeDigest: treeDigest, publicKey: publicKey, responses: responses,
	}
}

func TestSignatureMessageIsStableAndDomainSeparated(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := SignatureMessage("publisher.example", "primary", "release-1", digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := SignatureMessage("publisher.example", "primary", "release-1", digest, digest)
	if string(first) != string(second) || !strings.Contains(string(first), signatureDomain) {
		t.Fatalf("signature message=%s", first)
	}
	if _, err := SignatureMessage("publisher.example\nforged", "primary", "release-1", digest, digest); err == nil {
		t.Fatal("unsafe issuer accepted")
	}
}

func TestSignedRemoteContextCancellationIsPropagated(t *testing.T) {
	fixture := newSignedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.adapter.Scan(ctx, rolesource.ScanRequest{Config: fixture.config})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan error=%v", err)
	}
}
