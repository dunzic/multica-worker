// Package rolesource defines the source-neutral contract for importing and
// synchronising externally managed digital-worker roles.
//
// Adapters are trusted, compile-time registered components. Source-provided
// files are always data: registering an adapter never permits a source package
// to execute code during scan or apply.
package rolesource

import (
	"context"
	"encoding/json"
	"time"
)

const ContractVersion = "1.0"

const (
	FeatureFlagRoleSourceSync  = "role_source_sync"
	FeatureFlagRoleSourceScan  = "role_source_scan"
	FeatureFlagRoleSourceApply = "role_source_apply"
)

// Kind is the stable API and persistence identity of an adapter.
type Kind string

// Descriptor is adapter capability negotiation data. It is safe to expose to
// an authenticated administrator and must never contain source credentials.
type Descriptor struct {
	Kind            Kind                `json:"kind"`
	DisplayName     string              `json:"display_name"`
	AdapterVersion  string              `json:"adapter_version"`
	ContractVersion string              `json:"contract_version"`
	Capabilities    AdapterCapabilities `json:"capabilities"`
}

// AdapterCapabilities prevents the API and UI from advertising modes that an
// adapter does not implement.
type AdapterCapabilities struct {
	ChangeHints     bool `json:"change_hints"`
	SecretTransfer  bool `json:"secret_transfer"`
	BinaryArtifacts bool `json:"binary_artifacts"`
	Provenance      bool `json:"provenance"`
}

// Adapter converts one source-specific configuration into the normalized
// manifest. Scan must be deterministic for stable source content and read-only.
type Adapter interface {
	Descriptor() Descriptor
	ValidateConfig(json.RawMessage) error
	RedactConfig(json.RawMessage) (ConfigSummary, error)
	Scan(context.Context, ScanRequest) (ScanOutput, error)
}

// ConfigSummary is the only source-configuration representation allowed in
// control-plane persistence and ordinary APIs. Attributes are adapter-defined
// labels, never raw configuration values, paths or credentials.
type ConfigSummary struct {
	Configured bool              `json:"configured"`
	Attributes []ConfigAttribute `json:"attributes"`
}

type ConfigAttribute struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ScanRequest is the source-neutral request sent to a registered adapter.
// Config is source-specific and must be redacted before it enters logs or audit.
type ScanRequest struct {
	WorkspaceID            string          `json:"workspace_id"`
	SourceID               string          `json:"source_id"`
	Config                 json.RawMessage `json:"config"`
	PreviousSnapshotDigest string          `json:"previous_snapshot_digest,omitempty"`
	RequestedAt            time.Time       `json:"requested_at"`
}

// ScanOutput is produced by an adapter before registry validation and canonical
// hashing. Adapters do not choose the persisted snapshot digest.
type ScanOutput struct {
	Manifest       Manifest       `json:"manifest"`
	Diagnostics    []Diagnostic   `json:"diagnostics"`
	SourceEvidence SourceEvidence `json:"source_evidence"`
}

// Snapshot is the validated, source-neutral scan result. Plaintext secrets and
// artifact bodies are structurally absent from this type.
type Snapshot struct {
	Kind            Kind           `json:"kind"`
	AdapterVersion  string         `json:"adapter_version"`
	ContractVersion string         `json:"contract_version"`
	SnapshotDigest  string         `json:"snapshot_digest"`
	ManifestDigest  string         `json:"manifest_digest"`
	Manifest        Manifest       `json:"manifest"`
	Diagnostics     []Diagnostic   `json:"diagnostics"`
	SourceEvidence  SourceEvidence `json:"source_evidence"`
}

// SourceEvidence is intentionally typed. Adapter-specific source metadata must
// be represented by content digests, not arbitrary fields that could smuggle
// paths, credentials, or source bodies into an immutable snapshot.
type SourceEvidence struct {
	Revision        string `json:"revision,omitempty"`
	TreeDigest      string `json:"tree_digest"`
	SignatureDigest string `json:"signature_digest,omitempty"`
	Issuer          string `json:"issuer,omitempty"`
}

type Diagnostic struct {
	Severity   DiagnosticSeverity `json:"severity"`
	Code       string             `json:"code"`
	Message    string             `json:"message"`
	ObjectKind string             `json:"object_kind,omitempty"`
	ObjectID   string             `json:"object_id,omitempty"`
	Path       string             `json:"path,omitempty"`
}

type DiagnosticSeverity string

const (
	DiagnosticInfo    DiagnosticSeverity = "info"
	DiagnosticWarning DiagnosticSeverity = "warning"
	DiagnosticError   DiagnosticSeverity = "error"
)

// Manifest is the adapter-independent source-of-truth representation used by
// snapshot, diff, apply, rollback, and runtime pinning.
type Manifest struct {
	ContractVersion string       `json:"contract_version"`
	Roles           []Role       `json:"roles"`
	Capabilities    []Capability `json:"capabilities"`
}

type Role struct {
	ID                 string              `json:"id"`
	DisplayName        string              `json:"display_name"`
	Version            string              `json:"version,omitempty"`
	Lifecycle          string              `json:"lifecycle,omitempty"`
	Instructions       ArtifactRef         `json:"instructions"`
	Profile            *ArtifactRef        `json:"profile,omitempty"`
	Skills             []Skill             `json:"skills"`
	CapabilityBindings []CapabilityBinding `json:"capability_bindings"`
	Environment        []EnvironmentKey    `json:"environment"`
	MCP                []MCPServer         `json:"mcp"`
	Automations        []Automation        `json:"automations"`
}

type Skill struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Version    string        `json:"version,omitempty"`
	Entrypoint ArtifactRef   `json:"entrypoint"`
	Artifacts  []ArtifactRef `json:"artifacts,omitempty"`
}

type Capability struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	Profiles        []string               `json:"profiles"`
	PermissionModes []string               `json:"permission_modes"`
	Requirements    CapabilityRequirements `json:"requirements"`
	Entrypoint      ArtifactRef            `json:"entrypoint"`
	Artifacts       []ArtifactRef          `json:"artifacts,omitempty"`
}

type CapabilityRequirements struct {
	Adapters    []AdapterRequirement `json:"adapters"`
	Environment []string             `json:"environment"`
	MCP         []string             `json:"mcp"`
}

type AdapterRequirement struct {
	ID       string `json:"id"`
	Required bool   `json:"required"`
}

type CapabilityBinding struct {
	CapabilityID      string `json:"capability_id"`
	SkillID           string `json:"skill_id"`
	Profile           string `json:"profile"`
	VersionConstraint string `json:"version_constraint"`
	Required          bool   `json:"required"`
	PermissionMode    string `json:"permission_mode"`
	Fallback          string `json:"fallback"`
}

// EnvironmentKey carries only declaration metadata and a keyed, non-reversible
// change digest. The secret value uses a separate one-time transfer protocol.
type EnvironmentKey struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Configured  bool   `json:"configured"`
	ValueDigest string `json:"value_digest,omitempty"`
}

type MCPServer struct {
	ID             string   `json:"id"`
	DefinitionHash string   `json:"definition_hash"`
	Environment    []string `json:"environment,omitempty"`
}

type Automation struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Schedule         string      `json:"schedule"`
	Timezone         string      `json:"timezone"`
	InitiallyEnabled bool        `json:"initially_enabled"`
	Prompt           ArtifactRef `json:"prompt"`
}

// ArtifactRef is metadata for content-addressed retrieval. Bodies never travel
// in the normalized snapshot.
type ArtifactRef struct {
	Digest    string `json:"digest"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	SizeBytes int64  `json:"size_bytes"`
}

const PlanContractVersion = "1.0"

// Plan is a deterministic, secret-free comparison between two immutable
// snapshots. It contains no timestamps or actors: those belong to the
// persisted approval/apply records and must not make the plan digest unstable.
type Plan struct {
	ContractVersion    string        `json:"contract_version"`
	SourceID           string        `json:"source_id"`
	FromSnapshotDigest string        `json:"from_snapshot_digest,omitempty"`
	ToSnapshotDigest   string        `json:"to_snapshot_digest"`
	PlanDigest         string        `json:"plan_digest"`
	Applyable          bool          `json:"applyable"`
	Summary            PlanSummary   `json:"summary"`
	Actions            []PlanAction  `json:"actions"`
	Blockers           []PlanBlocker `json:"blockers"`
}

type PlanSummary struct {
	Create           int `json:"create"`
	Update           int `json:"update"`
	Unchanged        int `json:"unchanged"`
	ArchiveCandidate int `json:"archive_candidate"`
	Blocked          int `json:"blocked"`
}

type PlanOperation string

const (
	PlanCreate           PlanOperation = "create"
	PlanUpdate           PlanOperation = "update"
	PlanUnchanged        PlanOperation = "unchanged"
	PlanArchiveCandidate PlanOperation = "archive_candidate"
	PlanBlocked          PlanOperation = "blocked"
)

type PlanRisk string

const (
	PlanRiskNone   PlanRisk = "none"
	PlanRiskLow    PlanRisk = "low"
	PlanRiskMedium PlanRisk = "medium"
	PlanRiskHigh   PlanRisk = "high"
)

// ObjectRef is the stable source identity of one independently materialized
// object. ParentID scopes role-owned objects such as skills and automations.
type ObjectRef struct {
	Kind     string `json:"kind"`
	ParentID string `json:"parent_id,omitempty"`
	ID       string `json:"id"`
}

type PlanAction struct {
	Ref                 ObjectRef     `json:"ref"`
	DisplayName         string        `json:"display_name,omitempty"`
	Operation           PlanOperation `json:"operation"`
	ProposedOperation   PlanOperation `json:"proposed_operation,omitempty"`
	Risk                PlanRisk      `json:"risk"`
	BeforeDigest        string        `json:"before_digest,omitempty"`
	AfterDigest         string        `json:"after_digest,omitempty"`
	Reason              string        `json:"reason"`
	BlockingDiagnostics []string      `json:"blocking_diagnostics,omitempty"`
}

type PlanBlocker struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Object  ObjectRef `json:"object,omitempty"`
	Global  bool      `json:"global"`
}
