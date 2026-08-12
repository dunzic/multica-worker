package rolesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

var (
	stableIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	sha256Pattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	hmacSHA256Pattern = regexp.MustCompile(`^hmac-sha256:[a-f0-9]{64}$`)
)

var (
	ErrAdapterNotFound  = errors.New("role source adapter not found")
	ErrDuplicateAdapter = errors.New("role source adapter already registered")
)

// Registry is concurrency-safe because daemon scan workers and control-plane
// capability requests may access it concurrently.
type Registry struct {
	mu       sync.RWMutex
	adapters map[Kind]registeredAdapter
}

type registeredAdapter struct {
	descriptor Descriptor
	adapter    Adapter
}

func NewRegistry(adapters ...Adapter) (*Registry, error) {
	r := &Registry{adapters: make(map[Kind]registeredAdapter, len(adapters))}
	for _, adapter := range adapters {
		if err := r.Register(adapter); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Register(adapter Adapter) error {
	if adapter == nil {
		return errors.New("role source adapter is nil")
	}
	desc := adapter.Descriptor()
	if err := validateDescriptor(desc); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[desc.Kind]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateAdapter, desc.Kind)
	}
	r.adapters[desc.Kind] = registeredAdapter{descriptor: desc, adapter: adapter}
	return nil
}

func (r *Registry) Descriptor(kind Kind) (Descriptor, bool) {
	r.mu.RLock()
	registered, ok := r.adapters[kind]
	r.mu.RUnlock()
	if !ok {
		return Descriptor{}, false
	}
	return registered.descriptor, true
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	out := make([]Descriptor, 0, len(r.adapters))
	for _, registered := range r.adapters {
		out = append(out, registered.descriptor)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// RedactConfig validates source configuration and returns the adapter's safe
// API/audit representation. Raw configuration must never be serialized through
// a generic source response.
func (r *Registry) RedactConfig(kind Kind, config json.RawMessage) (json.RawMessage, error) {
	r.mu.RLock()
	registered, ok := r.adapters[kind]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAdapterNotFound, kind)
	}
	if err := registered.adapter.ValidateConfig(config); err != nil {
		return nil, fmt.Errorf("validate %s config: %w", kind, err)
	}
	redacted, err := registered.adapter.RedactConfig(config)
	if err != nil {
		return nil, fmt.Errorf("redact %s config: %w", kind, err)
	}
	if len(redacted) == 0 || len(redacted) > 64<<10 || !json.Valid(redacted) {
		return nil, fmt.Errorf("redact %s config: adapter returned invalid or oversized JSON", kind)
	}
	return append(json.RawMessage(nil), redacted...), nil
}

// Scan validates configuration, invokes the selected adapter, validates the
// normalized manifest, and assigns the canonical manifest digest.
func (r *Registry) Scan(ctx context.Context, kind Kind, request ScanRequest) (Snapshot, error) {
	r.mu.RLock()
	registered, ok := r.adapters[kind]
	r.mu.RUnlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrAdapterNotFound, kind)
	}
	if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.SourceID) == "" {
		return Snapshot{}, errors.New("role source scan requires workspace_id and source_id")
	}
	if err := registered.adapter.ValidateConfig(request.Config); err != nil {
		return Snapshot{}, fmt.Errorf("validate %s config: %w", kind, err)
	}

	output, err := registered.adapter.Scan(ctx, request)
	if err != nil {
		return Snapshot{}, err
	}
	desc := registered.descriptor
	if err := validateSourceEvidence(output.SourceEvidence); err != nil {
		return Snapshot{}, fmt.Errorf("validate %s source evidence: %w", kind, err)
	}
	if err := validateDiagnostics(output.Diagnostics); err != nil {
		return Snapshot{}, fmt.Errorf("validate %s diagnostics: %w", kind, err)
	}
	canonicalizeDiagnostics(output.Diagnostics)
	if err := validateManifest(&output.Manifest); err != nil {
		return Snapshot{}, fmt.Errorf("validate %s manifest: %w", kind, err)
	}
	digest, err := digestManifest(output.Manifest)
	if err != nil {
		return Snapshot{}, fmt.Errorf("digest %s manifest: %w", kind, err)
	}
	snapshot := Snapshot{
		Kind:            desc.Kind,
		AdapterVersion:  desc.AdapterVersion,
		ContractVersion: desc.ContractVersion,
		ManifestDigest:  digest,
		Manifest:        output.Manifest,
		Diagnostics:     output.Diagnostics,
		SourceEvidence:  output.SourceEvidence,
	}
	snapshotDigest, err := digestSnapshot(snapshot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("digest %s snapshot: %w", kind, err)
	}
	snapshot.SnapshotDigest = snapshotDigest
	return snapshot, nil
}

func validateSourceEvidence(evidence SourceEvidence) error {
	if !sha256Pattern.MatchString(evidence.TreeDigest) {
		return fmt.Errorf("invalid tree digest %q", evidence.TreeDigest)
	}
	if evidence.SignatureDigest != "" && !sha256Pattern.MatchString(evidence.SignatureDigest) {
		return fmt.Errorf("invalid signature digest %q", evidence.SignatureDigest)
	}
	if len(evidence.Revision) > 512 || len(evidence.Issuer) > 512 {
		return errors.New("source evidence text exceeds limit")
	}
	return nil
}

func validateDiagnostics(diagnostics []Diagnostic) error {
	if len(diagnostics) > 10_000 {
		return errors.New("diagnostic count exceeds hard limit")
	}
	for index, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case DiagnosticInfo, DiagnosticWarning, DiagnosticError:
		default:
			return fmt.Errorf("diagnostic %d has invalid severity %q", index, diagnostic.Severity)
		}
		if !stableIDPattern.MatchString(diagnostic.Code) {
			return fmt.Errorf("diagnostic %d has invalid code %q", index, diagnostic.Code)
		}
		if diagnostic.Message == "" || len(diagnostic.Message) > 4096 {
			return fmt.Errorf("diagnostic %d message is empty or exceeds limit", index)
		}
		if diagnostic.Path != "" {
			if _, err := normalizeSourcePath(diagnostic.Path, false); err != nil {
				return fmt.Errorf("diagnostic %d path: %w", index, err)
			}
		}
	}
	return nil
}

func canonicalizeDiagnostics(diagnostics []Diagnostic) {
	sort.Slice(diagnostics, func(i, j int) bool {
		a, b := diagnostics[i], diagnostics[j]
		return string(a.Severity)+"\x00"+a.Code+"\x00"+a.ObjectKind+"\x00"+a.ObjectID+"\x00"+a.Path+"\x00"+a.Message <
			string(b.Severity)+"\x00"+b.Code+"\x00"+b.ObjectKind+"\x00"+b.ObjectID+"\x00"+b.Path+"\x00"+b.Message
	})
}

func validateDescriptor(desc Descriptor) error {
	if !kindPattern.MatchString(string(desc.Kind)) {
		return fmt.Errorf("invalid role source adapter kind %q", desc.Kind)
	}
	if strings.TrimSpace(desc.DisplayName) == "" || strings.TrimSpace(desc.AdapterVersion) == "" {
		return fmt.Errorf("role source adapter %q requires display_name and adapter_version", desc.Kind)
	}
	if desc.ContractVersion != ContractVersion {
		return fmt.Errorf("role source adapter %q uses contract %q, expected %q", desc.Kind, desc.ContractVersion, ContractVersion)
	}
	return nil
}

func validateManifest(manifest *Manifest) error {
	if manifest.ContractVersion != ContractVersion {
		return fmt.Errorf("manifest contract %q, expected %q", manifest.ContractVersion, ContractVersion)
	}
	if len(manifest.Roles) > 10_000 || len(manifest.Capabilities) > 10_000 {
		return errors.New("manifest object count exceeds hard limit")
	}
	totalObjects := len(manifest.Roles) + len(manifest.Capabilities)
	totalArtifacts := 0
	for _, capability := range manifest.Capabilities {
		totalArtifacts += 1 + len(capability.Artifacts)
	}
	for _, role := range manifest.Roles {
		totalObjects += len(role.Skills) + len(role.CapabilityBindings) + len(role.Environment) + len(role.MCP) + len(role.Automations)
		totalArtifacts += 1 + len(role.Skills) + len(role.Automations)
		if role.Profile != nil {
			totalArtifacts++
		}
		for _, skill := range role.Skills {
			totalArtifacts += len(skill.Artifacts)
		}
	}
	if totalObjects > maxNormalizedObjects || totalArtifacts > maxNormalizedObjects {
		return errors.New("manifest nested object or artifact count exceeds hard limit")
	}
	if err := validateUniqueIDs("role", roleIDs(manifest.Roles)); err != nil {
		return err
	}
	if err := validateUniqueIDs("capability", capabilityIDs(manifest.Capabilities)); err != nil {
		return err
	}
	capabilities := make(map[string]Capability, len(manifest.Capabilities))
	for i := range manifest.Capabilities {
		capability := &manifest.Capabilities[i]
		if err := validateCapability(capability); err != nil {
			return fmt.Errorf("capability %q: %w", capability.ID, err)
		}
		capabilities[capability.ID] = *capability
	}
	for i := range manifest.Roles {
		role := &manifest.Roles[i]
		if err := validateArtifact(role.Instructions); err != nil {
			return fmt.Errorf("role %q instructions: %w", role.ID, err)
		}
		if role.Profile != nil {
			if err := validateArtifact(*role.Profile); err != nil {
				return fmt.Errorf("role %q profile: %w", role.ID, err)
			}
		}
		if err := validateUniqueIDs("skill", skillIDs(role.Skills)); err != nil {
			return fmt.Errorf("role %q: %w", role.ID, err)
		}
		skills := make(map[string]Skill, len(role.Skills))
		for j := range role.Skills {
			skill := &role.Skills[j]
			if err := validateArtifact(skill.Entrypoint); err != nil {
				return fmt.Errorf("role %q skill %q entrypoint: %w", role.ID, skill.ID, err)
			}
			if err := validateArtifactSet(skill.Artifacts); err != nil {
				return fmt.Errorf("role %q skill %q: %w", role.ID, skill.ID, err)
			}
			skills[skill.ID] = *skill
		}
		if err := validateUniqueIDs("environment key", environmentIDs(role.Environment)); err != nil {
			return fmt.Errorf("role %q: %w", role.ID, err)
		}
		if err := validateUniqueIDs("MCP server", mcpIDs(role.MCP)); err != nil {
			return fmt.Errorf("role %q: %w", role.ID, err)
		}
		if err := validateUniqueIDs("automation", automationIDs(role.Automations)); err != nil {
			return fmt.Errorf("role %q: %w", role.ID, err)
		}
		for _, environment := range role.Environment {
			if environment.Configured && !hmacSHA256Pattern.MatchString(environment.ValueDigest) {
				return fmt.Errorf("role %q environment key %q requires an hmac-sha256 value digest", role.ID, environment.Name)
			}
			if !environment.Configured && environment.ValueDigest != "" {
				return fmt.Errorf("role %q environment key %q has a digest while unconfigured", role.ID, environment.Name)
			}
		}
		for _, server := range role.MCP {
			if !sha256Pattern.MatchString(server.DefinitionHash) {
				return fmt.Errorf("role %q MCP server %q has an invalid definition hash", role.ID, server.ID)
			}
		}
		for _, automation := range role.Automations {
			if err := validateArtifact(automation.Prompt); err != nil {
				return fmt.Errorf("role %q automation %q prompt: %w", role.ID, automation.ID, err)
			}
		}
		if err := validateBindings(role, skills, capabilities); err != nil {
			return err
		}
	}
	canonicalizeManifest(manifest)
	return nil
}

func validateUniqueIDs(kind string, ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%s id is empty", kind)
		}
		if !stableIDPattern.MatchString(id) {
			return fmt.Errorf("invalid %s id %q", kind, id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate %s id %q", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateCapability(capability *Capability) error {
	if err := validateUniqueIDs("profile", capability.Profiles); err != nil {
		return err
	}
	if err := validateUniqueIDs("permission mode", capability.PermissionModes); err != nil {
		return err
	}
	adapterIDs := make([]string, len(capability.Requirements.Adapters))
	for index := range capability.Requirements.Adapters {
		adapterIDs[index] = capability.Requirements.Adapters[index].ID
	}
	if err := validateUniqueIDs("adapter requirement", adapterIDs); err != nil {
		return err
	}
	if err := validateUniqueIDs("environment requirement", capability.Requirements.Environment); err != nil {
		return err
	}
	if err := validateUniqueIDs("MCP requirement", capability.Requirements.MCP); err != nil {
		return err
	}
	if err := validateArtifact(capability.Entrypoint); err != nil {
		return fmt.Errorf("entrypoint: %w", err)
	}
	return validateArtifactSet(capability.Artifacts)
}

func validateArtifactSet(artifacts []ArtifactRef) error {
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateArtifact(artifact); err != nil {
			return err
		}
		if _, ok := seen[artifact.Path]; ok {
			return fmt.Errorf("duplicate artifact path %q", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
	}
	return nil
}

func validateArtifact(artifact ArtifactRef) error {
	if !sha256Pattern.MatchString(artifact.Digest) {
		return fmt.Errorf("invalid digest %q", artifact.Digest)
	}
	if artifact.Path == "" || strings.Contains(artifact.Path, "\\") || strings.Contains(artifact.Path, ":") || strings.HasPrefix(artifact.Path, "/") {
		return fmt.Errorf("invalid relative path %q", artifact.Path)
	}
	clean := path.Clean(artifact.Path)
	if clean != artifact.Path || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid relative path %q", artifact.Path)
	}
	if strings.TrimSpace(artifact.MediaType) == "" {
		return errors.New("media_type is required")
	}
	if artifact.SizeBytes < 0 || artifact.SizeBytes > 1<<30 {
		return fmt.Errorf("size_bytes %d is outside the allowed range", artifact.SizeBytes)
	}
	return nil
}

func validateBindings(role *Role, skills map[string]Skill, capabilities map[string]Capability) error {
	seen := make(map[string]struct{}, len(role.CapabilityBindings))
	for _, binding := range role.CapabilityBindings {
		key := binding.CapabilityID + "\x00" + binding.SkillID + "\x00" + binding.Profile
		if _, ok := seen[key]; ok {
			return fmt.Errorf("role %q has a duplicate capability binding", role.ID)
		}
		seen[key] = struct{}{}
		capability, ok := capabilities[binding.CapabilityID]
		if !ok {
			return fmt.Errorf("role %q binding references unknown capability %q", role.ID, binding.CapabilityID)
		}
		if _, ok := skills[binding.SkillID]; !ok {
			return fmt.Errorf("role %q binding references unknown skill %q", role.ID, binding.SkillID)
		}
		if !contains(capability.Profiles, binding.Profile) {
			return fmt.Errorf("role %q binding references unknown profile %q on capability %q", role.ID, binding.Profile, binding.CapabilityID)
		}
		if !contains(capability.PermissionModes, binding.PermissionMode) {
			return fmt.Errorf("role %q binding requests unsupported permission mode %q on capability %q", role.ID, binding.PermissionMode, binding.CapabilityID)
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func canonicalizeManifest(manifest *Manifest) {
	sort.Slice(manifest.Roles, func(i, j int) bool { return manifest.Roles[i].ID < manifest.Roles[j].ID })
	sort.Slice(manifest.Capabilities, func(i, j int) bool { return manifest.Capabilities[i].ID < manifest.Capabilities[j].ID })
	for i := range manifest.Roles {
		role := &manifest.Roles[i]
		sort.Slice(role.Skills, func(i, j int) bool { return role.Skills[i].ID < role.Skills[j].ID })
		sort.Slice(role.CapabilityBindings, func(i, j int) bool {
			a, b := role.CapabilityBindings[i], role.CapabilityBindings[j]
			return a.CapabilityID+"\x00"+a.SkillID+"\x00"+a.Profile < b.CapabilityID+"\x00"+b.SkillID+"\x00"+b.Profile
		})
		sort.Slice(role.Environment, func(i, j int) bool { return role.Environment[i].Name < role.Environment[j].Name })
		sort.Slice(role.MCP, func(i, j int) bool { return role.MCP[i].ID < role.MCP[j].ID })
		sort.Slice(role.Automations, func(i, j int) bool { return role.Automations[i].ID < role.Automations[j].ID })
		for j := range role.Skills {
			sort.Slice(role.Skills[j].Artifacts, func(a, b int) bool { return role.Skills[j].Artifacts[a].Path < role.Skills[j].Artifacts[b].Path })
		}
	}
	for i := range manifest.Capabilities {
		capability := &manifest.Capabilities[i]
		sort.Strings(capability.Profiles)
		sort.Strings(capability.PermissionModes)
		sort.Slice(capability.Requirements.Adapters, func(a, b int) bool {
			return capability.Requirements.Adapters[a].ID < capability.Requirements.Adapters[b].ID
		})
		sort.Strings(capability.Requirements.Environment)
		sort.Strings(capability.Requirements.MCP)
		sort.Slice(capability.Artifacts, func(a, b int) bool { return capability.Artifacts[a].Path < capability.Artifacts[b].Path })
	}
}

func digestManifest(manifest Manifest) (string, error) {
	body, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func digestSnapshot(snapshot Snapshot) (string, error) {
	snapshot.SnapshotDigest = ""
	body, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func roleIDs(values []Role) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].ID
	}
	return out
}

func capabilityIDs(values []Capability) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].ID
	}
	return out
}

func skillIDs(values []Skill) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].ID
	}
	return out
}

func environmentIDs(values []EnvironmentKey) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].Name
	}
	return out
}

func mcpIDs(values []MCPServer) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].ID
	}
	return out
}

func automationIDs(values []Automation) []string {
	out := make([]string, len(values))
	for i := range values {
		out[i] = values[i].ID
	}
	return out
}
