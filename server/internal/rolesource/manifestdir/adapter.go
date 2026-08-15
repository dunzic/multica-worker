// Package manifestdir implements the source-neutral Multica manifest
// directory adapter. It is intentionally small: the source publishes the
// normalized, secret-free manifest contract directly and keeps artifact bodies
// beside it. The daemon remains the only process with filesystem authority.
package manifestdir

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/rolesource"
)

const (
	Kind             rolesource.Kind = "multica_manifest_directory"
	adapterVersion                   = "0.1.0"
	defaultManifest                  = "multica-role-source.json"
	maxConfigBytes                   = 64 << 10
	maxManifestBytes                 = 8 << 20
	maxArtifactBytes                 = 8 << 20
)

type RootValidator func(string) error

type Adapter struct {
	validateRoot RootValidator
}

type config struct {
	RootPath     string `json:"root_path"`
	ManifestPath string `json:"manifest_path,omitempty"`
}

func New(validateRoot RootValidator) (*Adapter, error) {
	if validateRoot == nil {
		return nil, errors.New("manifest directory adapter requires a daemon root validator")
	}
	return &Adapter{validateRoot: validateRoot}, nil
}

func Descriptor() rolesource.Descriptor {
	return rolesource.Descriptor{
		Kind: Kind, DisplayName: "Multica manifest directory", AdapterVersion: adapterVersion,
		ContractVersion: rolesource.ContractVersion,
		Capabilities: rolesource.AdapterCapabilities{
			ChangeHints: false, SecretTransfer: false, BinaryArtifacts: false, Provenance: true,
		},
	}
}

func (a *Adapter) Descriptor() rolesource.Descriptor { return Descriptor() }

func (a *Adapter) ValidateConfig(raw json.RawMessage) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	return a.validateRoot(cfg.RootPath)
}

func (a *Adapter) RedactConfig(raw json.RawMessage) (rolesource.ConfigSummary, error) {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return rolesource.ConfigSummary{}, err
	}
	return rolesource.ConfigSummary{Configured: true, Attributes: []rolesource.ConfigAttribute{
		{Name: "manifest_name", Value: path.Base(cfg.ManifestPath)},
		{Name: "root_name", Value: filepath.Base(cfg.RootPath)},
	}}, nil
}

func (a *Adapter) Scan(ctx context.Context, request rolesource.ScanRequest) (rolesource.ScanOutput, error) {
	if err := ctx.Err(); err != nil {
		return rolesource.ScanOutput{}, err
	}
	cfg, err := decodeConfig(request.Config)
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	if err := a.validateRoot(cfg.RootPath); err != nil {
		return rolesource.ScanOutput{}, err
	}
	sourceFS, err := rolesource.OpenBoundedFS(cfg.RootPath)
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	defer sourceFS.Close() //nolint:errcheck
	body, err := sourceFS.ReadFile(cfg.ManifestPath, maxManifestBytes)
	if err != nil {
		return rolesource.ScanOutput{}, fmt.Errorf("read Multica role-source manifest: %w", err)
	}
	manifest, err := decodeManifest(body)
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	if err := validateSupportedManifest(manifest); err != nil {
		return rolesource.ScanOutput{}, err
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	sum := sha256.Sum256(canonical)
	treeDigest := "sha256:" + hex.EncodeToString(sum[:])
	return rolesource.ScanOutput{
		Manifest: manifest,
		SourceEvidence: rolesource.SourceEvidence{
			Revision: treeDigest, TreeDigest: treeDigest, Issuer: string(Kind),
		},
	}, nil
}

func validateSupportedManifest(manifest rolesource.Manifest) error {
	for _, role := range manifest.Roles {
		for _, environment := range role.Environment {
			if environment.Configured {
				return fmt.Errorf("role %q declares configured environment but this adapter has no secret-transfer authority", role.ID)
			}
		}
		if len(role.MCP) != 0 {
			return fmt.Errorf("role %q declares MCP servers but this adapter has no secret-transfer authority", role.ID)
		}
	}
	for _, ref := range manifestArtifactRefs(manifest) {
		switch ref.MediaType {
		case "text/markdown", "text/plain", "application/json", "application/yaml":
		default:
			return fmt.Errorf("artifact %q media type %q is unsupported by the text-only adapter", ref.Path, ref.MediaType)
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

func (a *Adapter) OpenArtifact(ctx context.Context, request rolesource.ScanRequest, ref rolesource.ArtifactRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.SizeBytes > maxArtifactBytes {
		return nil, fmt.Errorf("manifest directory artifact %q exceeds adapter upload limit", ref.Path)
	}
	cfg, err := decodeConfig(request.Config)
	if err != nil {
		return nil, err
	}
	if err := a.validateRoot(cfg.RootPath); err != nil {
		return nil, err
	}
	sourceFS, err := rolesource.OpenBoundedFS(cfg.RootPath)
	if err != nil {
		return nil, err
	}
	defer sourceFS.Close() //nolint:errcheck
	body, err := sourceFS.ReadFile(ref.Path, maxArtifactBytes)
	if err != nil {
		return nil, err
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

func decodeConfig(raw json.RawMessage) (config, error) {
	if len(raw) == 0 || len(raw) > maxConfigBytes {
		return config{}, errors.New("manifest directory config size is outside the allowed range")
	}
	var cfg config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("decode manifest directory config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config{}, errors.New("manifest directory config contains trailing JSON")
	}
	cfg.RootPath = strings.TrimSpace(cfg.RootPath)
	if !filepath.IsAbs(cfg.RootPath) || filepath.Clean(cfg.RootPath) != cfg.RootPath {
		return config{}, errors.New("manifest directory root_path must be a clean absolute path")
	}
	cfg.ManifestPath = strings.TrimSpace(cfg.ManifestPath)
	if cfg.ManifestPath == "" {
		cfg.ManifestPath = defaultManifest
	}
	if len(cfg.ManifestPath) > 1024 || path.IsAbs(cfg.ManifestPath) || path.Clean(cfg.ManifestPath) != cfg.ManifestPath || cfg.ManifestPath == "." ||
		cfg.ManifestPath == ".." || strings.HasPrefix(cfg.ManifestPath, "../") || strings.Contains(cfg.ManifestPath, "\\") {
		return config{}, errors.New("manifest_path must be a clean root-relative slash path")
	}
	return cfg, nil
}

func decodeManifest(body []byte) (rolesource.Manifest, error) {
	var manifest rolesource.Manifest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return rolesource.Manifest{}, fmt.Errorf("decode Multica role-source manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return rolesource.Manifest{}, errors.New("Multica role-source manifest contains trailing JSON")
	}
	return manifest, nil
}
