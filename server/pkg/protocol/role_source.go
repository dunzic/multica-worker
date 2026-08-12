package protocol

// RoleSourceCapabilityPin is the exact immutable capability definition a
// source-managed role resolved when its task was first enqueued. It contains
// identifiers and content digests only; secret values and artifact bodies are
// structurally absent.
type RoleSourceCapabilityPin struct {
	CapabilityID      string `json:"capability_id"`
	SkillID           string `json:"skill_id"`
	Profile           string `json:"profile"`
	VersionConstraint string `json:"version_constraint,omitempty"`
	ResolvedVersion   string `json:"resolved_version"`
	ObjectDigest      string `json:"object_digest"`
	PermissionMode    string `json:"permission_mode"`
	Required          bool   `json:"required"`
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
