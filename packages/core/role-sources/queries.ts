import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const roleSourceKeys = {
  all: (workspaceId: string) => ["role-sources", workspaceId] as const,
  list: (workspaceId: string) =>
    [...roleSourceKeys.all(workspaceId), "list"] as const,
  plans: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "plans", sourceId] as const,
  impact: (workspaceId: string, sourceId: string, planDigest: string) =>
    [...roleSourceKeys.all(workspaceId), "impact", sourceId, planDigest] as const,
  applyFailures: (workspaceId: string, sourceId: string) =>
    [...roleSourceKeys.all(workspaceId), "apply-failures", sourceId] as const,
};

export function roleSourceListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: roleSourceKeys.list(workspaceId),
    queryFn: () => api.listRoleSources(workspaceId),
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
