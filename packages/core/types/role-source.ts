export interface RoleSourceConfigAttribute {
  name: string;
  value: string;
}

export interface RoleSourceConfigSummary {
  configured: boolean;
  attributes: RoleSourceConfigAttribute[];
}

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
  operation: RoleSourcePlanOperation;
  proposed_operation?: RoleSourcePlanOperation;
  risk: "none" | "low" | "medium" | "high";
  before_digest?: string;
  after_digest?: string;
  reason: string;
  blocking_diagnostics?: string[];
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
