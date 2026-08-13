package rolesourcedr

import "time"

const ContractVersion = "role-source-disaster-recovery-v1"

var tableNames = [...]string{
	"role_source",
	"role_source_snapshot",
	"role_source_scan_request",
	"role_source_plan",
	"role_source_plan_approval",
	"role_source_apply",
	"role_source_apply_failure",
	"role_source_audit_event",
	"role_source_artifact",
	"role_source_snapshot_artifact",
	"role_source_artifact_delete_intent",
	"role_source_object_mapping",
	"role_source_capability_version",
	"role_source_secret_transfer",
	"role_source_task_pin",
	"role_source_runtime_attestation",
	"role_source_runtime_attestation_observation",
	"role_source_legal_hold",
	"role_source_legal_hold_release",
	"role_source_retention_policy",
	"role_source_retention_candidate",
}

func TableNames() []string { return append([]string(nil), tableNames[:]...) }

type Manifest struct {
	ContractVersion       string          `json:"contract_version"`
	BackupID              string          `json:"backup_id"`
	CreatedAt             time.Time       `json:"created_at"`
	DatabaseMajorVersion  int             `json:"database_major_version"`
	MigrationCount        int64           `json:"migration_count"`
	MigrationRoot         string          `json:"migration_root"`
	Tables                []TableSummary  `json:"tables"`
	Artifacts             ArtifactSummary `json:"artifacts"`
	KeyRequirements       KeyRequirements `json:"key_requirements"`
	DatabaseDumpDigest    string          `json:"database_dump_digest,omitempty"`
	ArtifactArchiveDigest string          `json:"artifact_archive_digest,omitempty"`
	SignerKeyID           string          `json:"signer_key_id,omitempty"`
	Signature             string          `json:"signature,omitempty"`
	UnsignedDevelopment   bool            `json:"unsigned_development,omitempty"`
}

// PublicSummary is safe for ordinary operator logs and evidence tickets. The
// full signed manifest remains a restricted backup artifact because its table
// and digest commitments can still correlate tenant activity.
type PublicSummary struct {
	Status        string `json:"status"`
	Tables        int    `json:"tables"`
	Artifacts     int64  `json:"artifacts"`
	ArtifactBytes int64  `json:"artifact_bytes"`
}

func (m Manifest) PublicSummary(status string) PublicSummary {
	return PublicSummary{Status: status, Tables: len(m.Tables), Artifacts: m.Artifacts.Count, ArtifactBytes: m.Artifacts.SizeBytes}
}

type TableSummary struct {
	Name       string `json:"name"`
	RowCount   int64  `json:"row_count"`
	Commitment string `json:"commitment"`
}

type ArtifactSummary struct {
	Count      int64  `json:"count"`
	SizeBytes  int64  `json:"size_bytes"`
	Commitment string `json:"commitment"`
}

type KeyRequirements struct {
	SecretTransferKeyIDs []string `json:"secret_transfer_key_ids"`
	PendingTransfers     int64    `json:"pending_transfers"`
	SubmittedTransfers   int64    `json:"submitted_transfers"`
}

type Report struct {
	ContractVersion string               `json:"contract_version"`
	CheckedAt       time.Time            `json:"checked_at"`
	Status          string               `json:"status"`
	ManifestMatched bool                 `json:"manifest_matched"`
	Database        DatabaseVerification `json:"database"`
	Artifacts       ObjectVerification   `json:"artifacts"`
	Keys            KeyVerification      `json:"keys"`
	Findings        []Finding            `json:"findings"`
}

type DatabaseVerification struct {
	TablesChecked        int   `json:"tables_checked"`
	SnapshotsValidated   int64 `json:"snapshots_validated"`
	PlansValidated       int64 `json:"plans_validated"`
	ReceiptsValidated    int64 `json:"receipts_validated"`
	AuditEventsValidated int64 `json:"audit_events_validated"`
}

type ObjectVerification struct {
	Expected  int64 `json:"expected"`
	Verified  int64 `json:"verified"`
	SizeBytes int64 `json:"size_bytes"`
}

type KeyVerification struct {
	RequiredKeyIDs       []string `json:"required_key_ids"`
	AvailableKeyIDs      []string `json:"available_key_ids"`
	DecryptableTransfers int64    `json:"decryptable_transfers"`
}

type Finding struct {
	Code    string `json:"code"`
	Count   int64  `json:"count"`
	Subject string `json:"subject,omitempty"`
}
