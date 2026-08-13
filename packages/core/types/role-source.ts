export interface RoleSourceConfigAttribute {
  name: string;
  value: string;
}

export interface RoleSourceConfigSummary {
  configured: boolean;
  attributes: RoleSourceConfigAttribute[];
}

export type RoleSourceAttestationStatus =
  | "unattested"
  | "not_loaded"
  | "config_missing"
  | "kind_mismatch"
  | "adapter_version_mismatch"
  | "invalid_attestation"
  | "loaded";

export type RoleSourceRuntimeConfigStatus =
  | RoleSourceAttestationStatus
  | "runtime_unavailable";

export interface RoleSource {
  id: string;
  workspace_id: string;
  runtime_id: string;
  name: string;
  kind: string;
  adapter_version: string;
  config_summary: RoleSourceConfigSummary;
  policy: Record<string, unknown>;
  state: string;
  current_snapshot_digest: string | null;
  version: number;
  created_at: string;
  updated_at: string;
  runtime_config: {
    status: RoleSourceRuntimeConfigStatus;
    attestation_status: RoleSourceAttestationStatus;
    runtime_status: "online" | "offline" | "unknown";
    attestation_id: string | null;
    revision: string | null;
    observed_at: string | null;
    changed_at: string | null;
  };
}

export type RoleSourceLifecycleAction =
  | "pause"
  | "resume"
  | "detach"
  | "rebind";

export interface UpdateRoleSourceLifecycleRequest {
  action: RoleSourceLifecycleAction;
  expected_version: number;
  runtime_id?: string;
  daemon_config_id?: string;
}

export type RoleSourceLegalHoldScope = "source" | "snapshot";
export type RoleSourceLegalHoldReason =
  | "investigation"
  | "litigation"
  | "regulatory"
  | "customer_request"
  | "security_incident";
export type RoleSourceLegalHoldReleaseReason =
  | "resolved"
  | "court_order"
  | "entered_in_error"
  | "authorization_expired";

export interface RoleSourceLegalHold {
  id: string;
  workspace_id: string;
  source_id: string;
  scope: RoleSourceLegalHoldScope;
  snapshot_digest?: string;
  reason_code: RoleSourceLegalHoldReason;
  reference_digest?: string;
  created_by: string;
  created_at: string;
  status: "active" | "released";
  release_reason_code?: RoleSourceLegalHoldReleaseReason;
  release_reference_digest?: string;
  released_by?: string;
  released_at?: string;
}

export interface CreateRoleSourceLegalHoldRequest {
  request_key: string;
  scope: RoleSourceLegalHoldScope;
  snapshot_digest?: string;
  reason_code: RoleSourceLegalHoldReason;
  reference_digest?: string;
}

export interface ReleaseRoleSourceLegalHoldRequest {
  request_key: string;
  reason_code: RoleSourceLegalHoldReleaseReason;
  reference_digest?: string;
}

export interface RoleSourceRetentionPolicy {
  workspace_id: string;
  source_id: string;
  version: number;
  enabled: boolean;
  minimum_age_days: number;
  keep_successful_snapshots: number;
  created_by?: string;
  created_at?: string;
}

export interface RoleSourceRetentionCandidate {
  snapshot_digest: string;
  created_at: string;
  estimated_bytes: number;
}

export interface RoleSourceRetentionPreview {
  policy: RoleSourceRetentionPolicy;
  eligible_count: number;
  estimated_bytes: number;
  truncated: boolean;
  candidates: RoleSourceRetentionCandidate[];
}

export interface UpdateRoleSourceRetentionPolicyRequest {
  request_key: string;
  expected_version: number;
  enabled: boolean;
  minimum_age_days: number;
  keep_successful_snapshots: number;
}

export interface RoleSourceRuntimeAttestation {
  status: RoleSourceAttestationStatus;
  contract_version: string;
  loaded: boolean;
  attestation_id: string;
  revision: string | null;
  first_observed_at: string;
  last_observed_at: string;
  observation_count: number;
}

export type RoleSourceScanStatus =
  | "queued"
  | "claimed"
  | "succeeded"
  | "failed"
  | "cancelled";

export interface RoleSourceScan {
  id: string;
  source_id: string;
  workspace_id: string;
  status: RoleSourceScanStatus;
  expected_adapter_version: string;
  snapshot_digest: string | null;
  error_code: string | null;
  requested_at: string;
  claimed_at: string | null;
  completed_at: string | null;
}

export interface RoleSourceLifecycleEvent {
  sequence: number;
  event_type: "source_paused" | "source_resumed" | "source_detached" | "source_rebound" | string;
  actor_type: "user" | "runtime" | "system" | string;
  actor_id?: string;
  previous_state: string;
  state: string;
  previous_runtime_id?: string;
  runtime_id?: string;
  cancelled_scan_count: number;
  cancelled_transfer_count: number;
  event_digest: string;
  occurred_at: string;
}

export type RoleSourcePlanOperation =
  | "create"
  | "update"
  | "unchanged"
  | "archive_candidate"
  | "blocked";

export interface RoleSourceObjectRef {
  kind: string;
  parent_id?: string;
  id: string;
}

export interface RoleSourcePlanAction {
  ref: RoleSourceObjectRef;
  display_name: string;
  needs_secret_transfer?: boolean;
  operation: RoleSourcePlanOperation;
  proposed_operation?: RoleSourcePlanOperation;
  risk: "none" | "low" | "medium" | "high";
  before_digest?: string;
  after_digest?: string;
  reason: string;
  blocking_diagnostics?: string[];
}

export interface RoleSourceSecretTransferStatus {
  id: string;
  role_id: string;
  status: "pending" | "claimed" | "submitted" | "consumed" | "expired" | "failed" | string;
  expires_at: string;
  created_at: string;
  submitted_at?: string;
  consumed_at?: string;
  error_code?: string;
}

export interface RequestRoleSourceSecretTransferRequest {
  request_key: string;
  approval_id: string;
  role_id: string;
}

export interface RoleSourcePlanBlocker {
  code: string;
  message: string;
  global: boolean;
  object?: RoleSourceObjectRef;
}

export interface RoleSourcePlanSummary {
  create: number;
  update: number;
  unchanged: number;
  archive_candidate: number;
  blocked: number;
}

export interface RoleSourcePlan {
  contract_version: string;
  mode?: string;
  source_id: string;
  from_snapshot_digest?: string;
  to_snapshot_digest: string;
  plan_digest: string;
  applyable: boolean;
  summary: RoleSourcePlanSummary;
  actions: RoleSourcePlanAction[];
  blockers: RoleSourcePlanBlocker[];
}

export interface RoleSourcePlanRecord {
  source_id: string;
  workspace_id: string;
  plan: RoleSourcePlan;
  created_by: string;
  created_at: string;
}

export type RoleSourceArchiveDecision = "archive" | "retain";

export interface RoleSourceArchiveActionDecision {
  ref: RoleSourceObjectRef;
  decision: RoleSourceArchiveDecision;
}

export interface RoleSourceApprovalDecisions {
  contract_version: string;
  archives: RoleSourceArchiveActionDecision[];
}

export interface CreateRoleSourcePlanRequest {
  target_snapshot_digest: string;
}

export interface CreateRoleSourceApprovalRequest {
  request_key: string;
  decision: "approved" | "rejected";
  decisions?: RoleSourceApprovalDecisions;
}

export interface RoleSourcePlanApproval {
  id: string;
  source_id: string;
  workspace_id: string;
  plan_digest: string;
  decision: "approved" | "rejected" | string;
  decisions?: RoleSourceApprovalDecisions;
  actor_user_id: string;
  created_at: string;
}

export interface ApplyRoleSourcePlanRequest {
  request_key: string;
  approval_id: string;
  secret_transfer_ids?: Record<string, string>;
}

export interface RoleSourceApplyCounts {
  created: number;
  updated: number;
  unchanged: number;
  archived: number;
  retained: number;
}

export interface RoleSourceApplyReceipt {
  contract_version: string;
  mode?: string;
  apply_id: string;
  source_id: string;
  workspace_id: string;
  snapshot_digest: string;
  from_snapshot_digest?: string;
  plan_digest: string;
  approval_id: string;
  counts: RoleSourceApplyCounts;
  receipt_digest: string;
}

export interface RoleSourceApplyResult {
  id: string;
  source_id: string;
  workspace_id: string;
  status: string;
  mode: string;
  actor_user_id: string;
  receipt: RoleSourceApplyReceipt;
  completed_at: string | null;
}

export interface RoleSourcePlanImpactSummary {
  new_roles: number;
  mandatory_refresh_roles: number;
  conditional_archive_roles: number;
  unmapped_existing_roles: number;
  cancel_on_apply: number;
  conditional_cancel_on_archive: number;
  continue_current_version: number;
  worker_details_truncated: boolean;
  task_details_truncated: boolean;
}

export interface RoleSourcePlanImpactWorker {
  source_role_id: string;
  agent_id: string;
  agent_name: string;
  effect: "provenance_refresh" | "conditional_archive";
  pre_start_tasks: number;
  running_tasks: number;
  current_snapshot_digest: string;
}

export interface RoleSourcePlanImpactTask {
  task_id: string;
  source_role_id: string;
  agent_id: string;
  status: string;
  effect: "cancel_on_apply" | "cancel_if_archived" | "continue_current_version";
  created_at: string;
}

export interface RoleSourcePlanImpact {
  contract_version: string;
  source_id: string;
  plan_digest: string;
  target_snapshot_digest: string;
  applyable: boolean;
  generated_at: string;
  summary: RoleSourcePlanImpactSummary;
  workers: RoleSourcePlanImpactWorker[];
  tasks: RoleSourcePlanImpactTask[];
}

export interface RoleSourceApplyFailure {
  id: string;
  source_id: string;
  workspace_id: string;
  plan_digest: string;
  approval_id: string;
  actor_user_id: string;
  mode: "apply" | "rollback" | "unknown";
  failure_stage:
    | "preflight"
    | "transaction"
    | "materialization"
    | "finalize"
    | "commit";
  failure_code: string;
  occurred_at: string;
}
