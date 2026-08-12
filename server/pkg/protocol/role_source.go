package protocol

import (
	"crypto/sha256"
	"encoding/hex"
)

const RoleSourceCapabilityBundleContractV1 = "role-source-capability-bundle-v1"

// RoleSourceCapabilityPin is the exact immutable capability definition a
// source-managed role resolved when its task was first enqueued. It contains
// identifiers and content digests only; secret values and artifact bodies are
// structurally absent.
type RoleSourceCapabilityPin struct {
	CapabilityID      string `json:"capability_id"`
	SkillID           string `json:"skill_id"`
	TargetSkillID     string `json:"target_skill_id"`
	Profile           string `json:"profile"`
	VersionConstraint string `json:"version_constraint,omitempty"`
	ResolvedVersion   string `json:"resolved_version"`
	ObjectDigest      string `json:"object_digest"`
	PermissionMode    string `json:"permission_mode"`
	Required          bool   `json:"required"`
	Fallback          string `json:"fallback"`
}

// RoleSourceCapabilityBundle is the materialized, source-neutral proof stored
// inside a bound workspace skill. The daemon verifies it and every declared
// file against the immutable task pin before starting a provider process.
type RoleSourceCapabilityBundle struct {
	ContractVersion string                           `json:"contract_version"`
	CapabilityID    string                           `json:"capability_id"`
	SourceSkillID   string                           `json:"source_skill_id"`
	Profile         string                           `json:"profile"`
	ResolvedVersion string                           `json:"resolved_version"`
	ObjectDigest    string                           `json:"object_digest"`
	PermissionMode  string                           `json:"permission_mode"`
	Required        bool                             `json:"required"`
	Fallback        string                           `json:"fallback"`
	EntrypointPath  string                           `json:"entrypoint_path"`
	Files           []RoleSourceCapabilityBundleFile `json:"files"`
}

type RoleSourceCapabilityBundleFile struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	Digest     string `json:"digest"`
	SizeBytes  int64  `json:"size_bytes"`
	MediaType  string `json:"media_type"`
}

// RoleSourceCapabilityMarkerPath is deliberately derived from immutable
// source identities rather than using them as path components. This keeps the
// target path confined even when a valid source ID contains slashes or colons.
func RoleSourceCapabilityMarkerPath(capabilityID, sourceSkillID, profile string) string {
	digest := sha256.Sum256([]byte(capabilityID + "\x00" + sourceSkillID + "\x00" + profile))
	return ".multica/role-source/capability-bindings/" + hex.EncodeToString(digest[:]) + "/manifest.json"
}

// RoleSourceTaskPin proves which source role and snapshot a task was created
// against. Retries inherit the original values rather than resolving current
// source state again.
type RoleSourceTaskPin struct {
	SourceID            string                    `json:"source_id"`
	SourceRoleID        string                    `json:"source_role_id"`
	SnapshotDigest      string                    `json:"snapshot_digest"`
	RoleObjectDigest    string                    `json:"role_object_digest"`
	CapabilityPins      []RoleSourceCapabilityPin `json:"capability_pins"`
	InheritedFromTaskID string                    `json:"inherited_from_task_id,omitempty"`
}
