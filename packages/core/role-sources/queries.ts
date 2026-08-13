import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const roleSourceKeys = {
  all: (workspaceId: string) => ["role-sources", workspaceId] as const,
  list: (workspaceId: string) =>
    [...roleSourceKeys.all(workspaceId), "list"] as const,
  adapters: (workspaceId: string) =>
    [...roleSourceKeys.all(workspaceId), "adapters"] as const,
  plans: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "plans", sourceId] as const,
  impact: (workspaceId: string, sourceId: string, planDigest: string) =>
    [...roleSourceKeys.all(workspaceId), "impact", sourceId, planDigest] as const,
  configurationReview: (workspaceId: string, sourceId: string, planDigest: string) =>
    [...roleSourceKeys.all(workspaceId), "configuration-review", sourceId, planDigest] as const,
  applyFailures: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "apply-failures", sourceId] as const,
  approvals: (workspaceId: string, sourceId: string, planDigest: string) =>
    [...roleSourceKeys.all(workspaceId), "approvals", sourceId, planDigest] as const,
  applies: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "applies", sourceId] as const,
  secretTransfers: (workspaceId: string, sourceId: string, planDigest: string, approvalId: string) =>
    [...roleSourceKeys.all(workspaceId), "secret-transfers", sourceId, planDigest, approvalId] as const,
  runtimeAttestations: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "runtime-attestations", sourceId] as const,
  legalHolds: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "legal-holds", sourceId] as const,
  retention: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "retention", sourceId] as const,
  latestScan: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "latest-scan", sourceId] as const,
  scans: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "scans", sourceId] as const,
  lifecycleEvents: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "lifecycle-events", sourceId] as const,
  snapshots: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "snapshot-summaries", sourceId] as const,
  snapshotComparison: (workspaceId: string, sourceId: string, fromDigest: string, toDigest: string, offset: number) =>
    [...roleSourceKeys.all(workspaceId), "snapshot-comparison", sourceId, fromDigest, toDigest, offset] as const,
};

export function roleSourceListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: roleSourceKeys.list(workspaceId),
    queryFn: () => api.listRoleSources(workspaceId),
  });
}

export function roleSourceAdapterListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: roleSourceKeys.adapters(workspaceId),
    queryFn: () => api.listRoleSourceAdapters(workspaceId),
  });
}

export function roleSourceLegalHoldListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.legalHolds(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceLegalHolds(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceRetentionPreviewOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.retention(workspaceId, sourceId),
    queryFn: () => api.getRoleSourceRetentionPreview(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceRuntimeAttestationListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.runtimeAttestations(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceRuntimeAttestations(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceLatestScanOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.latestScan(workspaceId, sourceId),
    queryFn: () => api.getLatestRoleSourceScan(workspaceId, sourceId),
    enabled: Boolean(sourceId),
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === "queued" || status === "claimed" ? 2000 : false;
    },
    staleTime: 0,
  });
}

export function roleSourceScanListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.scans(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceScans(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceLifecycleEventListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.lifecycleEvents(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceLifecycleEvents(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceSnapshotSummaryListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.snapshots(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceSnapshotSummaries(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceSnapshotComparisonOptions(
  workspaceId: string,
  sourceId: string,
  fromDigest: string,
  toDigest: string,
  offset: number,
) {
  return queryOptions({
    queryKey: roleSourceKeys.snapshotComparison(workspaceId, sourceId, fromDigest, toDigest, offset),
    queryFn: () => api.compareRoleSourceSnapshots(workspaceId, sourceId, fromDigest, toDigest, offset),
    enabled: Boolean(sourceId && fromDigest && toDigest && fromDigest !== toDigest),
  });
}

export function roleSourcePlanListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.plans(workspaceId, sourceId),
    queryFn: () => api.listRoleSourcePlans(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourcePlanImpactOptions(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.impact(workspaceId, sourceId, planDigest),
    queryFn: () => api.getRoleSourcePlanImpact(workspaceId, sourceId, planDigest),
    enabled: Boolean(sourceId && planDigest),
  });
}

export function roleSourceConfigurationReviewOptions(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.configurationReview(workspaceId, sourceId, planDigest),
    queryFn: () => api.getRoleSourceConfigurationReview(workspaceId, sourceId, planDigest),
    enabled: Boolean(sourceId && planDigest),
  });
}

export function roleSourceApplyFailureListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.applyFailures(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceApplyFailures(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourcePlanApprovalListOptions(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.approvals(workspaceId, sourceId, planDigest),
    queryFn: () => api.listRoleSourcePlanApprovals(workspaceId, sourceId, planDigest),
    enabled: Boolean(sourceId && planDigest),
  });
}

export function roleSourceApplyHistoryListOptions(
  workspaceId: string,
  sourceId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.applies(workspaceId, sourceId),
    queryFn: () => api.listRoleSourceApplyHistory(workspaceId, sourceId),
    enabled: Boolean(sourceId),
  });
}

export function roleSourceSecretTransferListOptions(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
  approvalId: string,
) {
  return queryOptions({
    queryKey: roleSourceKeys.secretTransfers(workspaceId, sourceId, planDigest, approvalId),
    queryFn: () => api.listRoleSourceSecretTransfers(workspaceId, sourceId, planDigest, approvalId),
    enabled: Boolean(sourceId && planDigest && approvalId),
    refetchInterval: (query) => query.state.data?.some((transfer) =>
      transfer.status === "pending" || transfer.status === "claimed" || transfer.status === "submitted") ? 5000 : false,
    staleTime: 0,
  });
}
