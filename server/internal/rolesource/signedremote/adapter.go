// Package signedremote implements an HTTPS, Ed25519-pinned role-source
// adapter. Remote bytes remain inert data: the daemon verifies a bounded
// signed manifest and content-addressed artifacts, and never executes source
// code during scan or apply.
package signedremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

const (
	Kind             rolesource.Kind = "multica_signed_remote"
	adapterVersion                   = "0.1.0"
	bundleVersion                    = 1
	maxConfigBytes                   = 64 << 10
	maxBundleBytes                   = 8 << 20
	maxArtifactBytes                 = 8 << 20
	signatureDomain                  = "multica-role-source-signed-bundle-v1"
)

var (
	evidencePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+\-]{0,199}$`)
	remoteDeniedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
)

type Adapter struct {
	client *http.Client
}

type config struct {
	BundleURL       string            `json:"bundle_url"`
	ArtifactBaseURL string            `json:"artifact_base_url"`
	Issuer          string            `json:"issuer"`
	PublicKeys      map[string]string `json:"public_keys"`
	parsedBundle    *url.URL
	parsedArtifacts *url.URL
	keys            map[string]ed25519.PublicKey
}

type signedBundle struct {
	Version        int                 `json:"version"`
	Issuer         string              `json:"issuer"`
	KeyID          string              `json:"key_id"`
	Revision       string              `json:"revision"`
	ManifestDigest string              `json:"manifest_digest"`
	TreeDigest     string              `json:"tree_digest"`
	Manifest       rolesource.Manifest `json:"manifest"`
	Signature      string              `json:"signature"`
}

type signatureCommitment struct {
	Domain         string `json:"domain"`
	Version        int    `json:"version"`
	Issuer         string `json:"issuer"`
	KeyID          string `json:"key_id"`
	Revision       string `json:"revision"`
	ManifestDigest string `json:"manifest_digest"`
	TreeDigest     string `json:"tree_digest"`
}

type artifactCommitment struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
	MediaType string `json:"media_type"`
}

func Descriptor() rolesource.Descriptor {
	return rolesource.Descriptor{
		Kind: Kind, DisplayName: "Multica signed remote bundle", AdapterVersion: adapterVersion,
		ContractVersion: rolesource.ContractVersion,
		Capabilities: rolesource.AdapterCapabilities{
			ChangeHints: false, SecretTransfer: false, BinaryArtifacts: false, Provenance: true,
		},
	}
}

func New() (*Adapter, error) {
	return newWithClient(secureHTTPClient())
}

func newWithClient(client *http.Client) (*Adapter, error) {
	if client == nil {
		return nil, errors.New("signed remote adapter requires an HTTP client")
	}
	if client.Timeout <= 0 {
		return nil, errors.New("signed remote adapter requires a bounded HTTP client timeout")
	}
	return &Adapter{client: client}, nil
}

func (a *Adapter) Descriptor() rolesource.Descriptor { return Descriptor() }

func (a *Adapter) ValidateConfig(raw json.RawMessage) error {
	_, err := decodeConfig(raw)
	return err
}

func (a *Adapter) RedactConfig(raw json.RawMessage) (rolesource.ConfigSummary, error) {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return rolesource.ConfigSummary{}, err
	}
	keySetDigest := digestKeySet(cfg.keys)
	return rolesource.ConfigSummary{Configured: true, Attributes: []rolesource.ConfigAttribute{
		{Name: "host", Value: strings.ToLower(cfg.parsedBundle.Hostname())},
		{Name: "issuer", Value: cfg.Issuer},
		{Name: "key_set_digest", Value: keySetDigest},
	}}, nil
}

func (a *Adapter) Scan(ctx context.Context, request rolesource.ScanRequest) (rolesource.ScanOutput, error) {
	cfg, err := decodeConfig(request.Config)
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	body, err := a.fetch(ctx, cfg.parsedBundle, maxBundleBytes, "application/json")
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteUnavailable, fmt.Errorf("fetch signed role-source bundle: %w", err))
	}
	bundle, err := decodeBundle(body)
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteContent, err)
	}
	if bundle.Version != bundleVersion || bundle.Issuer != cfg.Issuer || !evidencePattern.MatchString(bundle.Revision) || !evidencePattern.MatchString(bundle.KeyID) {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, errors.New("signed role-source bundle identity is invalid"))
	}
	key, ok := cfg.keys[bundle.KeyID]
	if !ok {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, errors.New("signed role-source bundle key_id is not trusted"))
	}
	canonical, manifestDigest, err := rolesource.CanonicalManifest(bundle.Manifest)
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteContent, fmt.Errorf("validate signed role-source manifest: %w", err))
	}
	if err := validateReadOnlyManifest(canonical); err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteContent, err)
	}
	treeDigest, err := ArtifactTreeDigest(canonical)
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteContent, err)
	}
	if bundle.ManifestDigest != manifestDigest || bundle.TreeDigest != treeDigest {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, errors.New("signed role-source bundle digest commitment does not match content"))
	}
	signature, err := decodeSignature(bundle.Signature)
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, err)
	}
	message, err := SignatureMessage(bundle.Issuer, bundle.KeyID, bundle.Revision, manifestDigest, treeDigest)
	if err != nil {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, err)
	}
	if !ed25519.Verify(key, message, signature) {
		return rolesource.ScanOutput{}, rolesource.NewScanFailure(rolesource.ScanFailureRemoteTrustInvalid, errors.New("signed role-source bundle signature verification failed"))
	}
	signatureDigest := sha256.Sum256(signature)
	return rolesource.ScanOutput{
		Manifest: canonical,
		SourceEvidence: rolesource.SourceEvidence{
			Revision: bundle.Revision, TreeDigest: treeDigest,
			SignatureDigest: "sha256:" + hex.EncodeToString(signatureDigest[:]), Issuer: bundle.Issuer, KeyID: bundle.KeyID,
		},
	}, nil
}

func (a *Adapter) OpenArtifact(ctx context.Context, request rolesource.ScanRequest, ref rolesource.ArtifactRef) (io.ReadCloser, error) {
	if ref.SizeBytes < 0 || ref.SizeBytes > maxArtifactBytes {
		return nil, fmt.Errorf("signed remote artifact %q exceeds adapter upload limit", ref.Path)
	}
	cfg, err := decodeConfig(request.Config)
	if err != nil {
		return nil, err
	}
	artifactURL, err := artifactURL(cfg.parsedArtifacts, ref.Path)
	if err != nil {
		return nil, err
	}
	body, err := a.fetch(ctx, artifactURL, maxArtifactBytes, "application/octet-stream")
	if err != nil {
		return nil, rolesource.NewScanFailure(rolesource.ScanFailureRemoteUnavailable, fmt.Errorf("fetch signed remote artifact: %w", err))
	}
	if int64(len(body)) != ref.SizeBytes {
		return nil, fmt.Errorf("%w: artifact %q size changed", rolesource.ErrChangedDuringRead, ref.Path)
	}
	sum := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(sum[:]) != ref.Digest {
		return nil, fmt.Errorf("%w: artifact %q digest changed", rolesource.ErrChangedDuringRead, ref.Path)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// SignatureMessage is the stable, domain-separated byte contract publishers
// sign with Ed25519. It contains no manifest body, URL, or credential.
func SignatureMessage(issuer, keyID, revision, manifestDigest, treeDigest string) ([]byte, error) {
	if !evidencePattern.MatchString(issuer) || !evidencePattern.MatchString(keyID) || !evidencePattern.MatchString(revision) ||
		!validSHA256(manifestDigest) || !validSHA256(treeDigest) {
		return nil, errors.New("invalid signed role-source commitment")
	}
	return json.Marshal(signatureCommitment{
		Domain: signatureDomain, Version: bundleVersion, Issuer: issuer, KeyID: keyID, Revision: revision,
		ManifestDigest: manifestDigest, TreeDigest: treeDigest,
	})
}

// ArtifactTreeDigest commits to every content-addressed artifact reference in
// canonical order. The body is verified separately when the daemon uploads it.
func ArtifactTreeDigest(manifest rolesource.Manifest) (string, error) {
	refs := manifestArtifactRefs(manifest)
	commitments := make([]artifactCommitment, 0, len(refs))
	for _, ref := range refs {
		commitments = append(commitments, artifactCommitment{
			Path: ref.Path, Digest: ref.Digest, SizeBytes: ref.SizeBytes, MediaType: ref.MediaType,
		})
	}
	sort.Slice(commitments, func(i, j int) bool {
		a, b := commitments[i], commitments[j]
		return a.Path+"\x00"+a.Digest+"\x00"+a.MediaType+"\x00"+strconv.FormatInt(a.SizeBytes, 10) <
			b.Path+"\x00"+b.Digest+"\x00"+b.MediaType+"\x00"+strconv.FormatInt(b.SizeBytes, 10)
	})
	body, err := json.Marshal(commitments)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeConfig(raw json.RawMessage) (config, error) {
	if len(raw) == 0 || len(raw) > maxConfigBytes {
		return config{}, errors.New("signed remote config size is outside the allowed range")
	}
	var cfg config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode signed remote config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config{}, errors.New("signed remote config contains trailing JSON")
	}
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	if !evidencePattern.MatchString(cfg.Issuer) {
		return config{}, errors.New("signed remote issuer is invalid")
	}
	bundleURL, err := validateRemoteURL(cfg.BundleURL, false)
	if err != nil {
		return config{}, fmt.Errorf("bundle_url: %w", err)
	}
	artifactBase, err := validateRemoteURL(cfg.ArtifactBaseURL, true)
	if err != nil {
		return config{}, fmt.Errorf("artifact_base_url: %w", err)
	}
	if !strings.EqualFold(bundleURL.Host, artifactBase.Host) {
		return config{}, errors.New("signed remote bundle and artifacts must use the same HTTPS authority")
	}
	if len(cfg.PublicKeys) == 0 || len(cfg.PublicKeys) > 3 {
		return config{}, errors.New("signed remote public_keys count must be between 1 and 3")
	}
	keys := make(map[string]ed25519.PublicKey, len(cfg.PublicKeys))
	for keyID, encoded := range cfg.PublicKeys {
		if !evidencePattern.MatchString(keyID) {
			return config{}, errors.New("signed remote public key id is invalid")
		}
		key, err := decodePublicKey(encoded)
		if err != nil {
			return config{}, fmt.Errorf("signed remote public key %q: %w", keyID, err)
		}
		keys[keyID] = key
	}
	cfg.parsedBundle, cfg.parsedArtifacts, cfg.keys = bundleURL, artifactBase, keys
	return cfg, nil
}

func digestKeySet(keys map[string]ed25519.PublicKey) string {
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hash := sha256.New()
	for _, id := range ids {
		hash.Write([]byte(id)) //nolint:errcheck -- hash.Hash writes never fail.
		hash.Write([]byte{0})  //nolint:errcheck -- hash.Hash writes never fail.
		hash.Write(keys[id])   //nolint:errcheck -- hash.Hash writes never fail.
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func validateRemoteURL(raw string, directory bool) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, errors.New("URL length is outside the allowed range")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("URL is invalid")
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, errors.New("URL must be credential-free canonical HTTPS without query or fragment")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, errors.New("URL must use the default HTTPS port")
	}
	host := parsed.Hostname()
	if !validDNSHostname(host) || net.ParseIP(host) != nil {
		return nil, errors.New("URL requires a canonical DNS hostname")
	}
	if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || path.Clean(parsed.Path) != strings.TrimSuffix(parsed.Path, "/") {
		return nil, errors.New("URL path must be canonical")
	}
	if directory && !strings.HasSuffix(parsed.Path, "/") {
		return nil, errors.New("artifact base URL must end with a slash")
	}
	return parsed, nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	if len(encoded) == 0 || len(encoded) > 128 {
		return nil, errors.New("signed remote public_key is invalid")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("signed remote public_key must be one base64 Ed25519 public key")
	}
	return ed25519.PublicKey(append([]byte(nil), decoded...)), nil
}

func decodeSignature(encoded string) ([]byte, error) {
	if len(encoded) == 0 || len(encoded) > 256 {
		return nil, errors.New("signed role-source bundle signature is invalid")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return nil, errors.New("signed role-source bundle signature is invalid")
	}
	return decoded, nil
}

func decodeBundle(body []byte) (signedBundle, error) {
	var bundle signedBundle
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return signedBundle{}, fmt.Errorf("decode signed role-source bundle: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return signedBundle{}, errors.New("signed role-source bundle contains trailing JSON")
	}
	return bundle, nil
}

func artifactURL(base *url.URL, relative string) (*url.URL, error) {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "../") {
		return nil, errors.New("signed remote artifact path is invalid")
	}
	copyURL := *base
	copyURL.Path = base.Path + relative
	return &copyURL, nil
}

func (a *Adapter) fetch(ctx context.Context, target *url.URL, limit int64, accept string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "multica-role-source-daemon/1")
	response, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, errors.New("remote response exceeds size limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("remote response exceeds size limit")
	}
	return body, nil
}

func secureHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true, DisableCompression: true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12}, //nolint:gosec -- TLS 1.2 remains required for enterprise registries.
		ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, candidate := range addresses {
				candidate = candidate.Unmap()
				if !isPublicIP(candidate) {
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errors.New("signed remote hostname resolved only to non-public addresses")
		},
	}
	return &http.Client{
		Timeout: 30 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func isPublicIP(value netip.Addr) bool {
	value = value.Unmap()
	if !value.IsValid() || !value.IsGlobalUnicast() || value.IsPrivate() || value.IsLoopback() ||
		value.IsLinkLocalUnicast() || value.IsLinkLocalMulticast() || value.IsMulticast() || value.IsUnspecified() {
		return false
	}
	for _, prefix := range remoteDeniedPrefixes {
		if prefix.Contains(value) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validDNSHostname(host string) bool {
	if host == "" || len(host) > 253 || strings.ToLower(host) != host {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, value := range label {
			if (value < 'a' || value > 'z') && (value < '0' || value > '9') && value != '-' {
				return false
			}
		}
	}
	return true
}

func validateReadOnlyManifest(manifest rolesource.Manifest) error {
	for _, role := range manifest.Roles {
		for _, environment := range role.Environment {
			if environment.Configured {
				return fmt.Errorf("role %q declares configured environment but signed remote has no secret-transfer authority", role.ID)
			}
		}
		if len(role.MCP) != 0 {
			return fmt.Errorf("role %q declares MCP servers but signed remote has no secret-transfer authority", role.ID)
		}
	}
	for _, ref := range manifestArtifactRefs(manifest) {
		switch ref.MediaType {
		case "text/markdown", "text/plain", "application/json", "application/yaml":
		default:
			return fmt.Errorf("artifact %q media type %q is unsupported by signed remote", ref.Path, ref.MediaType)
		}
		if ref.SizeBytes > maxArtifactBytes {
			return fmt.Errorf("artifact %q exceeds signed remote upload limit", ref.Path)
		}
	}
	return nil
}

func manifestArtifactRefs(manifest rolesource.Manifest) []rolesource.ArtifactRef {
	refs := make([]rolesource.ArtifactRef, 0)
	for _, capability := range manifest.Capabilities {
		refs = append(refs, capability.Entrypoint)
		refs = append(refs, capability.Artifacts...)
	}
	for _, role := range manifest.Roles {
		refs = append(refs, role.Instructions)
		if role.Profile != nil {
			refs = append(refs, *role.Profile)
		}
		for _, skill := range role.Skills {
			refs = append(refs, skill.Entrypoint)
			refs = append(refs, skill.Artifacts...)
		}
		for _, automation := range role.Automations {
			refs = append(refs, automation.Prompt)
		}
	}
	return refs
}
