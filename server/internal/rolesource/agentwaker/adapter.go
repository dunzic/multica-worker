package agentwaker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const (
	Kind                    rolesource.Kind = "agentwaker_directory"
	maxContractBytes                        = 1 << 20
	maxArtifactBytes                        = 8 << 20
	maxSupportingFileBytes                  = 1 << 20
	maxSupportingFileCount                  = 128
	maxSupportingTotalBytes                 = 8 << 20
)

var (
	sourceIDPattern   = regexp.MustCompile("^[a-z0-9]+(?:-[a-z0-9]+)*$")
	semverPattern     = regexp.MustCompile("^[0-9]+\\.[0-9]+\\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\\+[0-9A-Za-z.-]+)?$")
	versionConstraint = regexp.MustCompile("^(?:[~^]|>=?)?[0-9]+\\.[0-9]+\\.[0-9]+(?:-[0-9A-Za-z.-]+)?$")
	titleTokenPattern = regexp.MustCompile(`{{\s*([^{}\s]+)\s*}}`)
	cronParser        = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

var ignoredDirectoryNames = map[string]bool{
	".git": true, ".idea": true, "node_modules": true, "__pycache__": true,
}

var binaryExtensions = map[string]bool{
	".db": true, ".dll": true, ".dylib": true, ".exe": true, ".gif": true,
	".gz": true, ".ico": true, ".jpeg": true, ".jpg": true, ".mov": true,
	".mp3": true, ".mp4": true, ".pdf": true, ".png": true, ".pyc": true,
	".so": true, ".sqlite": true, ".tar": true, ".wav": true, ".webp": true,
	".zip": true,
}

type RootValidator func(string) error

// Adapter scans AgentWaker 2.1 directories into the source-neutral manifest.
// The digest key is mandatory because configured environment values must be
// represented by keyed digests, never reversible or dictionary-attackable
// plaintext hashes.
type Adapter struct {
	digestKey    []byte
	validateRoot RootValidator
}

func New(digestKey []byte, validateRoot RootValidator) (*Adapter, error) {
	if len(digestKey) < 32 {
		return nil, errors.New("agentwaker adapter requires an environment digest key of at least 32 bytes")
	}
	if validateRoot == nil {
		return nil, errors.New("agentwaker adapter requires a daemon root validator")
	}
	return &Adapter{digestKey: append([]byte(nil), digestKey...), validateRoot: validateRoot}, nil
}

func (a *Adapter) Descriptor() rolesource.Descriptor {
	return Descriptor()
}

// Descriptor exposes source-neutral negotiation metadata without constructing
// a filesystem-capable adapter. The server catalog uses this; only the daemon
// calls New and receives scan authority.
func Descriptor() rolesource.Descriptor {
	return rolesource.Descriptor{
		Kind:            Kind,
		DisplayName:     "AgentWaker directory",
		AdapterVersion:  adapterVersion,
		ContractVersion: rolesource.ContractVersion,
		Capabilities: rolesource.AdapterCapabilities{
			ChangeHints:     false,
			SecretTransfer:  false,
			BinaryArtifacts: false,
			Provenance:      true,
		},
	}
}

func (a *Adapter) ValidateConfig(raw json.RawMessage) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.RootPath) || filepath.Clean(cfg.RootPath) != cfg.RootPath {
		return errors.New("agentwaker root_path must be a clean absolute path")
	}
	return a.validateRoot(cfg.RootPath)
}

func (a *Adapter) RedactConfig(raw json.RawMessage) (rolesource.ConfigSummary, error) {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return rolesource.ConfigSummary{}, err
	}
	return rolesource.ConfigSummary{
		Configured: true,
		Attributes: []rolesource.ConfigAttribute{{Name: "root_name", Value: filepath.Base(cfg.RootPath)}},
	}, nil
}

func (a *Adapter) Scan(ctx context.Context, request rolesource.ScanRequest) (rolesource.ScanOutput, error) {
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
	defer sourceFS.Close()

	scanner := directoryScanner{
		ctx:       ctx,
		fs:        sourceFS,
		digestKey: a.digestKey,
		tree:      newTreeDigest(),
	}
	return scanner.scan()
}

// OpenArtifact reopens a public file through BoundedFS after scan and verifies
// exact size and digest. A source mutation between scan and upload therefore
// fails closed instead of attaching bytes that do not match the snapshot.
func (a *Adapter) OpenArtifact(ctx context.Context, request rolesource.ScanRequest, ref rolesource.ArtifactRef) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ref.SizeBytes > maxArtifactBytes {
		return nil, fmt.Errorf("AgentWaker artifact %q exceeds adapter upload limit", ref.Path)
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

type directoryScanner struct {
	ctx          context.Context
	fs           *rolesource.BoundedFS
	digestKey    []byte
	tree         *treeDigest
	diagnostics  []rolesource.Diagnostic
	requirements map[string]rolesource.CapabilityRequirements
}

func (s *directoryScanner) scan() (rolesource.ScanOutput, error) {
	if _, err := s.readPublic("schemas/profile-v2.1.schema.json", maxContractBytes); err != nil {
		return rolesource.ScanOutput{}, fmt.Errorf("agentwaker profile schema: %w", err)
	}
	registryBody, err := s.readPublic("capabilities/registry.yaml", maxContractBytes)
	if err != nil {
		return rolesource.ScanOutput{}, fmt.Errorf("agentwaker registry: %w", err)
	}
	var sourceRegistry registry
	if err := decodeYAMLStrict(registryBody, &sourceRegistry); err != nil {
		return rolesource.ScanOutput{}, fmt.Errorf("agentwaker registry: %w", err)
	}
	if sourceRegistry.SchemaVersion != registrySchemaVersion {
		return rolesource.ScanOutput{}, fmt.Errorf("agentwaker registry schema_version %q is unsupported", sourceRegistry.SchemaVersion)
	}

	sort.Slice(sourceRegistry.Capabilities, func(i, j int) bool {
		return sourceRegistry.Capabilities[i].ID < sourceRegistry.Capabilities[j].ID
	})
	s.requirements = make(map[string]rolesource.CapabilityRequirements, len(sourceRegistry.Capabilities))
	capabilities := make([]rolesource.Capability, 0, len(sourceRegistry.Capabilities))
	for _, entry := range sourceRegistry.Capabilities {
		if err := s.ctx.Err(); err != nil {
			return rolesource.ScanOutput{}, err
		}
		capability, err := s.scanCapability(entry)
		if err != nil {
			return rolesource.ScanOutput{}, err
		}
		capabilities = append(capabilities, capability)
	}

	entries, err := s.fs.ReadDir(".")
	if err != nil {
		return rolesource.ScanOutput{}, err
	}
	roles := []rolesource.Role{}
	for _, entry := range entries {
		if err := s.ctx.Err(); err != nil {
			return rolesource.ScanOutput{}, err
		}
		if !entry.IsDir() {
			continue
		}
		roleDir := entry.Name()
		profilePath := path.Join(roleDir, "agent-soul/PROFILE.yaml")
		if _, err := s.fs.Lstat(profilePath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return rolesource.ScanOutput{}, err
		}
		role, err := s.scanRole(roleDir)
		if err != nil {
			return rolesource.ScanOutput{}, err
		}
		roles = append(roles, role)
	}
	if len(roles) == 0 {
		return rolesource.ScanOutput{}, errors.New("agentwaker source contains no formal roles")
	}

	return rolesource.ScanOutput{
		Manifest: rolesource.Manifest{
			ContractVersion: rolesource.ContractVersion,
			Roles:           roles,
			Capabilities:    capabilities,
		},
		Diagnostics: s.diagnostics,
		SourceEvidence: rolesource.SourceEvidence{
			TreeDigest: s.tree.sum(),
		},
	}, nil
}

func (s *directoryScanner) scanCapability(entry registryCapability) (rolesource.Capability, error) {
	if !sourceIDPattern.MatchString(entry.ID) || !semverPattern.MatchString(entry.Version) {
		return rolesource.Capability{}, fmt.Errorf("agentwaker registry capability %q has invalid identity or version", entry.ID)
	}
	manifestPath, err := joinWithin("capabilities", entry.Manifest)
	if err != nil {
		return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q manifest: %w", entry.ID, err)
	}
	body, err := s.readPublic(manifestPath, maxContractBytes)
	if err != nil {
		return rolesource.Capability{}, err
	}
	var manifest capabilityManifest
	if err := decodeYAMLStrict(body, &manifest); err != nil {
		return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q: %w", entry.ID, err)
	}
	if manifest.SchemaVersion != capabilitySchemaVersion || manifest.ID != entry.ID || manifest.Version != entry.Version {
		return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q registry/manifest identity mismatch", entry.ID)
	}
	if !semverPattern.MatchString(manifest.Version) || !validPermissionMode(manifest.Permissions.DefaultMode) {
		return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q has invalid version or permission mode", entry.ID)
	}
	capabilityDir := path.Dir(manifestPath)
	entrypointPath, err := joinWithin(capabilityDir, manifest.Entrypoint)
	if err != nil {
		return rolesource.Capability{}, err
	}
	entrypointBody, err := s.readPublic(entrypointPath, maxArtifactBytes)
	if err != nil {
		return rolesource.Capability{}, err
	}
	profiles := make([]string, 0, len(manifest.Profiles))
	for _, profile := range manifest.Profiles {
		if !sourceIDPattern.MatchString(profile.ID) {
			return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q has invalid profile %q", entry.ID, profile.ID)
		}
		profiles = append(profiles, profile.ID)
	}
	adapterRequirements := make([]rolesource.AdapterRequirement, 0, len(manifest.Adapters))
	for _, adapter := range manifest.Adapters {
		if !sourceIDPattern.MatchString(adapter.ID) {
			return rolesource.Capability{}, fmt.Errorf("agentwaker capability %q has invalid adapter %q", entry.ID, adapter.ID)
		}
		adapterRequirements = append(adapterRequirements, rolesource.AdapterRequirement{ID: adapter.ID, Required: adapter.Required})
	}
	requirements := rolesource.CapabilityRequirements{
		Adapters: adapterRequirements, Environment: append([]string(nil), manifest.Requires.Environment...),
		MCP: append([]string(nil), manifest.Requires.MCP...),
	}
	s.requirements[manifest.ID] = requirements
	artifacts := []rolesource.ArtifactRef{}
	for _, relative := range []string{manifest.Contracts.InputSchema, manifest.Contracts.OutputSchema} {
		artifactPath, err := joinWithin(capabilityDir, relative)
		if err != nil {
			return rolesource.Capability{}, err
		}
		artifactBody, err := s.readPublic(artifactPath, maxArtifactBytes)
		if err != nil {
			return rolesource.Capability{}, err
		}
		artifacts = append(artifacts, artifactRef(artifactPath, artifactBody))
	}
	return rolesource.Capability{
		ID:              manifest.ID,
		Name:            manifest.Name,
		Version:         manifest.Version,
		Profiles:        profiles,
		PermissionModes: permissionModesThrough(manifest.Permissions.DefaultMode),
		Requirements:    requirements,
		Entrypoint:      artifactRef(entrypointPath, entrypointBody),
		Artifacts:       artifacts,
	}, nil
}

func (s *directoryScanner) scanRole(roleDir string) (rolesource.Role, error) {
	profilePath := path.Join(roleDir, "agent-soul/PROFILE.yaml")
	body, err := s.readPublic(profilePath, maxContractBytes)
	if err != nil {
		return rolesource.Role{}, err
	}
	var sourceProfile profile
	if err := decodeYAMLStrict(body, &sourceProfile); err != nil {
		return rolesource.Role{}, fmt.Errorf("agentwaker role %q profile: %w", roleDir, err)
	}
	if sourceProfile.SchemaVersion != profileSchemaVersion || !sourceIDPattern.MatchString(sourceProfile.ID) {
		return rolesource.Role{}, fmt.Errorf("agentwaker role %q has invalid profile identity", roleDir)
	}
	if sourceProfile.DisplayName == "" || !semverPattern.MatchString(sourceProfile.Version) {
		return rolesource.Role{}, fmt.Errorf("agentwaker role %q has invalid display name or version", sourceProfile.ID)
	}

	instructionsPath := path.Join(roleDir, "agent-detail.en.md")
	instructions, err := s.readPublic(instructionsPath, maxArtifactBytes)
	if err != nil {
		return rolesource.Role{}, fmt.Errorf("agentwaker role %q instructions: %w", sourceProfile.ID, err)
	}
	var profileRef *rolesource.ArtifactRef
	personaPath := path.Join(roleDir, "agent-persona.html")
	if persona, err := s.readOptionalPublic(personaPath, maxArtifactBytes); err != nil {
		return rolesource.Role{}, err
	} else if persona != nil {
		ref := artifactRef(personaPath, persona)
		profileRef = &ref
	}

	skills, err := s.scanSkills(roleDir, sourceProfile)
	if err != nil {
		return rolesource.Role{}, err
	}
	bindings, err := s.scanBindings(roleDir, sourceProfile.ID)
	if err != nil {
		return rolesource.Role{}, err
	}
	environment, err := s.scanEnvironment(roleDir, sourceProfile)
	if err != nil {
		return rolesource.Role{}, err
	}
	mcpServers, requiredEnvironment, err := s.scanMCP(roleDir, sourceProfile.ID, environment)
	if err != nil {
		return rolesource.Role{}, err
	}
	for _, binding := range bindings {
		for _, name := range s.requirements[binding.CapabilityID].Environment {
			requiredEnvironment[name] = true
		}
	}
	for index := range environment {
		environment[index].Required = requiredEnvironment[environment[index].Name]
	}
	automations, err := s.scanAutomations(roleDir, sourceProfile.ID)
	if err != nil {
		return rolesource.Role{}, err
	}

	return rolesource.Role{
		ID:                 sourceProfile.ID,
		DisplayName:        sourceProfile.DisplayName,
		Version:            sourceProfile.Version,
		Lifecycle:          sourceProfile.Lifecycle,
		Instructions:       artifactRef(instructionsPath, instructions),
		Profile:            profileRef,
		Skills:             skills,
		CapabilityBindings: bindings,
		Environment:        environment,
		MCP:                mcpServers,
		Automations:        automations,
	}, nil
}

func (s *directoryScanner) scanSkills(roleDir string, sourceProfile profile) ([]rolesource.Skill, error) {
	items := append([]profileSkillItem(nil), sourceProfile.Skills.Items...)
	if sourceProfile.Skills.MetaEntrypoint != "" {
		items = append(items, profileSkillItem{
			ID:         sourceProfile.ID + "-meta",
			Name:       sourceProfile.DisplayName + " meta",
			Entrypoint: sourceProfile.Skills.MetaEntrypoint,
			Status:     "implemented",
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	specialistDirectories := map[string]bool{}
	for _, item := range sourceProfile.Skills.Items {
		entrypointPath, err := joinWithin(roleDir, item.Entrypoint)
		if err != nil {
			return nil, err
		}
		specialistDirectories[path.Dir(entrypointPath)] = true
	}
	out := make([]rolesource.Skill, 0, len(items))
	for _, item := range items {
		if item.Status != "" && item.Status != "implemented" {
			continue
		}
		if !sourceIDPattern.MatchString(item.ID) {
			return nil, fmt.Errorf("agentwaker role %q has invalid skill id %q", sourceProfile.ID, item.ID)
		}
		entrypointPath, err := joinWithin(roleDir, item.Entrypoint)
		if err != nil {
			return nil, err
		}
		body, err := s.readPublic(entrypointPath, maxArtifactBytes)
		if err != nil {
			return nil, err
		}
		name := item.Name
		if name == "" {
			name = item.ID
		}
		skipDirectories := map[string]bool{}
		if item.ID == sourceProfile.ID+"-meta" {
			for directory := range specialistDirectories {
				skipDirectories[directory] = true
			}
		}
		artifacts, err := s.collectSupportingArtifacts(path.Dir(entrypointPath), entrypointPath, skipDirectories)
		if err != nil {
			return nil, err
		}
		out = append(out, rolesource.Skill{
			ID: item.ID, Name: name, Entrypoint: artifactRef(entrypointPath, body), Artifacts: artifacts,
		})
	}
	return out, nil
}

func (s *directoryScanner) collectSupportingArtifacts(baseDir, entrypointPath string, skipDirectories map[string]bool) ([]rolesource.ArtifactRef, error) {
	artifacts := []rolesource.ArtifactRef{}
	totalBytes := int64(0)
	visitedFiles := 0
	errLimitReached := errors.New("supporting artifact scan limit reached")
	var walk func(string) error
	walk = func(directory string) error {
		entries, err := s.fs.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := path.Join(directory, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: %s", rolesource.ErrSymlink, name)
			}
			if entry.IsDir() {
				if skipDirectories[name] || ignoredDirectoryNames[entry.Name()] {
					continue
				}
				if err := walk(name); err != nil {
					return err
				}
				continue
			}
			visitedFiles++
			if visitedFiles > maxSupportingFileCount {
				s.diagnostics = append(s.diagnostics, rolesource.Diagnostic{
					Severity: rolesource.DiagnosticError,
					Code:     "artifact_count_exceeded", Message: "Supporting artifact count exceeds the adapter limit",
					ObjectKind: "artifact", ObjectID: entry.Name(), Path: name,
				})
				return errLimitReached
			}
			if name == entrypointPath {
				continue
			}
			if binaryExtensions[strings.ToLower(path.Ext(name))] {
				s.diagnostics = append(s.diagnostics, rolesource.Diagnostic{
					Severity: rolesource.DiagnosticWarning,
					Code:     "artifact_binary_skipped", Message: "Binary supporting artifact is not supported by this adapter version",
					ObjectKind: "artifact", ObjectID: entry.Name(), Path: name,
				})
				continue
			}
			body, err := s.readPublic(name, maxSupportingFileBytes)
			if errors.Is(err, rolesource.ErrFileLimitExceeded) {
				s.diagnostics = append(s.diagnostics, rolesource.Diagnostic{
					Severity: rolesource.DiagnosticError,
					Code:     "artifact_size_exceeded", Message: "Supporting artifact exceeds the per-file limit",
					ObjectKind: "artifact", ObjectID: entry.Name(), Path: name,
				})
				return errLimitReached
			}
			if err != nil {
				return err
			}
			totalBytes += int64(len(body))
			if totalBytes > maxSupportingTotalBytes {
				s.diagnostics = append(s.diagnostics, rolesource.Diagnostic{
					Severity: rolesource.DiagnosticError,
					Code:     "artifact_total_exceeded", Message: "Supporting artifacts exceed the aggregate limit",
					ObjectKind: "artifact", ObjectID: entry.Name(), Path: name,
				})
				continue
			}
			artifacts = append(artifacts, artifactRef(name, body))
		}
		return nil
	}
	if err := walk(baseDir); err != nil {
		if errors.Is(err, errLimitReached) {
			return artifacts, nil
		}
		return nil, err
	}
	return artifacts, nil
}

func (s *directoryScanner) scanBindings(roleDir, roleID string) ([]rolesource.CapabilityBinding, error) {
	bindingPath := path.Join(roleDir, "capabilities.yaml")
	body, err := s.readOptionalPublic(bindingPath, maxContractBytes)
	if err != nil || body == nil {
		return []rolesource.CapabilityBinding{}, err
	}
	var sourceBindings roleCapabilities
	if err := decodeYAMLStrict(body, &sourceBindings); err != nil {
		return nil, err
	}
	if sourceBindings.SchemaVersion != bindingSchemaVersion || sourceBindings.Role != roleID {
		return nil, fmt.Errorf("agentwaker role %q capability binding identity mismatch", roleID)
	}
	out := []rolesource.CapabilityBinding{}
	for _, binding := range sourceBindings.Capabilities {
		if !sourceIDPattern.MatchString(binding.ID) || !versionConstraint.MatchString(binding.Version) || !validPermissionMode(binding.Permissions.Mode) {
			return nil, fmt.Errorf("agentwaker role %q has invalid capability binding %q", roleID, binding.ID)
		}
		if binding.Fallback.Behavior != "continue" && binding.Fallback.Behavior != "partial" && binding.Fallback.Behavior != "blocked" {
			return nil, fmt.Errorf("agentwaker role %q has invalid fallback for %q", roleID, binding.ID)
		}
		for _, use := range binding.UsedBy {
			out = append(out, rolesource.CapabilityBinding{
				CapabilityID:      binding.ID,
				SkillID:           use.Skill,
				Profile:           use.Profile,
				VersionConstraint: binding.Version,
				Required:          binding.Required,
				PermissionMode:    binding.Permissions.Mode,
				Fallback:          binding.Fallback.Behavior,
			})
		}
	}
	return out, nil
}

func (s *directoryScanner) scanEnvironment(roleDir string, sourceProfile profile) ([]rolesource.EnvironmentKey, error) {
	examplePath := sourceProfile.Skills.EnvExample
	if examplePath == "" {
		examplePath = "env/.env.example"
	}
	examplePath, err := joinWithin(roleDir, examplePath)
	if err != nil {
		return nil, err
	}
	exampleBody, err := s.readOptionalPublic(examplePath, maxContractBytes)
	if err != nil {
		return nil, err
	}
	actualPath := path.Join(roleDir, "env/.env")
	actualBody, err := s.readOptionalSensitive(actualPath, maxContractBytes)
	if err != nil {
		return nil, err
	}
	actual := parseEnv(actualBody)
	clear(actualBody)
	environment := mergeEnvironment(parseEnv(exampleBody), actual, s.digestKey)
	for name := range actual.values {
		actual.values[name] = ""
	}
	clear(actual.values)
	return environment, nil
}

func (s *directoryScanner) scanMCP(roleDir, roleID string, environment []rolesource.EnvironmentKey) ([]rolesource.MCPServer, map[string]bool, error) {
	mcpPath := path.Join(roleDir, "mcp/mcp.json")
	body, err := s.readOptionalSensitive(mcpPath, maxContractBytes)
	if err != nil || body == nil {
		return []rolesource.MCPServer{}, map[string]bool{}, err
	}
	var sourceConfig mcpConfig
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sourceConfig); err != nil {
		return nil, nil, fmt.Errorf("agentwaker role %q MCP: %w", roleID, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, nil, fmt.Errorf("agentwaker role %q MCP contains trailing JSON", roleID)
	}
	defer clear(body)
	declared := make(map[string]bool, len(environment))
	for _, entry := range environment {
		declared[entry.Name] = true
	}
	required := map[string]bool{}
	serverIDs := make([]string, 0, len(sourceConfig.Servers))
	for id := range sourceConfig.Servers {
		serverIDs = append(serverIDs, id)
	}
	sort.Strings(serverIDs)
	out := make([]rolesource.MCPServer, 0, len(serverIDs))
	for _, id := range serverIDs {
		if !sourceIDPattern.MatchString(id) {
			return nil, nil, fmt.Errorf("agentwaker role %q has invalid MCP server id %q", roleID, id)
		}
		raw := sourceConfig.Servers[id]
		canonical, err := canonicalJSON(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("agentwaker role %q MCP server %q: %w", roleID, id, err)
		}
		refs := envRefPattern.FindAllStringSubmatch(string(canonical), -1)
		envNames := make([]string, 0, len(refs))
		seen := map[string]bool{}
		for _, match := range refs {
			name := match[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			envNames = append(envNames, name)
			required[name] = true
			if !declared[name] {
				s.diagnostics = append(s.diagnostics, rolesource.Diagnostic{
					Severity: rolesource.DiagnosticError,
					Code:     "mcp_unresolved_env", Message: "MCP server references an undeclared environment key",
					ObjectKind: "role", ObjectID: roleID, Path: mcpPath,
				})
			}
		}
		sort.Strings(envNames)
		sum := sha256.Sum256(canonical)
		out = append(out, rolesource.MCPServer{
			ID: id, DefinitionHash: "sha256:" + hex.EncodeToString(sum[:]), Environment: envNames,
		})
		clear(raw)
		clear(canonical)
	}
	return out, required, nil
}

func (s *directoryScanner) scanAutomations(roleDir, roleID string) ([]rolesource.Automation, error) {
	manifestPath := path.Join(roleDir, "daily-tasks/manifest.yaml")
	body, err := s.readOptionalPublic(manifestPath, maxContractBytes)
	if err != nil || body == nil {
		return []rolesource.Automation{}, err
	}
	var manifest automationManifest
	if err := decodeYAMLStrict(body, &manifest); err != nil {
		return nil, fmt.Errorf("agentwaker role %q automation manifest: %w", roleID, err)
	}
	if manifest.SchemaVersion != automationSchemaVersion || manifest.RoleID != roleID {
		return nil, fmt.Errorf("agentwaker role %q automation manifest identity mismatch", roleID)
	}
	if len(manifest.Automations) == 0 || len(manifest.Automations) > 32 {
		return nil, fmt.Errorf("agentwaker role %q must declare 1..32 automations when the manifest exists", roleID)
	}
	sort.Slice(manifest.Automations, func(i, j int) bool { return manifest.Automations[i].ID < manifest.Automations[j].ID })
	out := make([]rolesource.Automation, 0, len(manifest.Automations))
	seen := map[string]bool{}
	for _, automation := range manifest.Automations {
		if !sourceIDPattern.MatchString(automation.ID) || seen[automation.ID] {
			return nil, fmt.Errorf("agentwaker role %q has invalid or duplicate automation id %q", roleID, automation.ID)
		}
		seen[automation.ID] = true
		if automation.Title == "" || automation.Schedule.Kind != "cron" || automation.Schedule.Expression == "" || automation.Schedule.Timezone == "" {
			return nil, fmt.Errorf("agentwaker role %q automation %q has an incomplete schedule", roleID, automation.ID)
		}
		if _, err := cronParser.Parse(automation.Schedule.Expression); err != nil {
			return nil, fmt.Errorf("agentwaker role %q automation %q has invalid cron: %w", roleID, automation.ID, err)
		}
		if _, err := time.LoadLocation(automation.Schedule.Timezone); err != nil {
			return nil, fmt.Errorf("agentwaker role %q automation %q has invalid timezone: %w", roleID, automation.ID, err)
		}
		if automation.Execution.Mode != "run_only" && automation.Execution.Mode != "create_issue" {
			return nil, fmt.Errorf("agentwaker role %q automation %q has unsupported execution mode", roleID, automation.ID)
		}
		if automation.Execution.Mode == "create_issue" {
			for _, match := range titleTokenPattern.FindAllStringSubmatch(automation.Execution.IssueTitleTemplate, -1) {
				if match[1] != "date" {
					return nil, fmt.Errorf("agentwaker role %q automation %q uses unsupported issue title variable %q", roleID, automation.ID, match[1])
				}
			}
		}
		promptPath, err := joinWithin(path.Join(roleDir, "daily-tasks"), automation.PromptFile)
		if err != nil {
			return nil, err
		}
		prompt, err := s.readPublic(promptPath, maxArtifactBytes)
		if err != nil {
			return nil, err
		}
		out = append(out, rolesource.Automation{
			ID: automation.ID, Name: automation.Title,
			Schedule: automation.Schedule.Expression, Timezone: automation.Schedule.Timezone,
			InitiallyEnabled: automation.Schedule.InitialEnabled,
			Prompt:           artifactRef(promptPath, prompt),
		})
	}
	return out, nil
}

func (s *directoryScanner) readPublic(name string, limit int64) ([]byte, error) {
	body, err := s.fs.ReadFile(name, limit)
	if err != nil {
		return nil, err
	}
	s.tree.addPublic(name, body)
	return body, nil
}

func (s *directoryScanner) readOptionalPublic(name string, limit int64) ([]byte, error) {
	body, err := s.readPublic(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return body, err
}

func (s *directoryScanner) readOptionalSensitive(name string, limit int64) ([]byte, error) {
	body, err := s.fs.ReadFile(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.tree.addSensitive(name, body, s.digestKey)
	return body, nil
}

func decodeConfig(raw json.RawMessage) (config, error) {
	var cfg config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return config{}, fmt.Errorf("agentwaker config: %w", err)
	}
	if cfg.RootPath == "" {
		return config{}, errors.New("agentwaker config requires root_path")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return config{}, errors.New("agentwaker config must contain one JSON object")
	}
	return cfg, nil
}

func decodeYAMLStrict(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple YAML documents are not supported")
	}
	return nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("multiple JSON values are not supported")
	}
	return json.Marshal(value)
}

func joinWithin(base, relative string) (string, error) {
	if relative == "" || strings.Contains(relative, "\\") || strings.Contains(relative, ":") || strings.HasPrefix(relative, "/") {
		return "", fmt.Errorf("unsafe AgentWaker relative path %q", relative)
	}
	joined := path.Clean(path.Join(base, relative))
	cleanBase := path.Clean(base)
	if joined == cleanBase || !strings.HasPrefix(joined, cleanBase+"/") {
		return "", fmt.Errorf("AgentWaker path %q escapes %q", relative, base)
	}
	return joined, nil
}

func artifactRef(name string, body []byte) rolesource.ArtifactRef {
	sum := sha256.Sum256(body)
	return rolesource.ArtifactRef{
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Path: name,
		MediaType: mediaType(name), SizeBytes: int64(len(body)),
	}
}

func mediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".html":
		return "text/html"
	case ".md":
		return "text/markdown"
	default:
		return "text/plain"
	}
}

func validPermissionMode(mode string) bool {
	return mode == "read-only" || mode == "local-write" || mode == "external-write"
}

func permissionModesThrough(maximum string) []string {
	switch maximum {
	case "read-only":
		return []string{"read-only"}
	case "local-write":
		return []string{"read-only", "local-write"}
	default:
		return []string{"read-only", "local-write", "external-write"}
	}
}

type treeDigest struct {
	hash hash.Hash
}

func newTreeDigest() *treeDigest {
	return &treeDigest{hash: sha256.New()}
}

func (t *treeDigest) addPublic(name string, body []byte) {
	sum := sha256.Sum256(body)
	hashPart(t.hash, name)
	hashPart(t.hash, hex.EncodeToString(sum[:]))
}

func (t *treeDigest) addSensitive(name string, body, key []byte) {
	mac := hmac.New(sha256.New, key)
	hashPart(mac, "agentwaker-sensitive-file-v1")
	hashPart(mac, name)
	_, _ = mac.Write(body)
	hashPart(t.hash, name)
	hashPart(t.hash, hex.EncodeToString(mac.Sum(nil)))
}

func (t *treeDigest) sum() string {
	return "sha256:" + hex.EncodeToString(t.hash.Sum(nil))
}
