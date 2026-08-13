import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  ApplyRoleSourcePlanRequest,
  CreateRoleSourceApprovalRequest,
  RoleSourceApplyResult,
  RoleSourcePlanApproval,
  RoleSourcePlanRecord,
} from "../types/role-source";
import { roleSourceKeys } from "./queries";

export function useRequestRoleSourceScan(workspaceId: string, sourceId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (requestKey: string) => api.requestRoleSourceScan(workspaceId, sourceId, requestKey),
    onSuccess: (scan) => {
      if (scan) {
        queryClient.setQueryData(
          roleSourceKeys.latestScan(workspaceId, sourceId),
          scan,
        );
      }
    },
    onSettled: () => queryClient.invalidateQueries({
      queryKey: roleSourceKeys.latestScan(workspaceId, sourceId),
    }),
  });
}

export function useCreateRoleSourcePlan(workspaceId: string, sourceId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (targetSnapshotDigest: string) => api.createRoleSourcePlan(
      workspaceId,
      sourceId,
      { target_snapshot_digest: targetSnapshotDigest },
    ),
    onSuccess: (plan) => {
      if (plan) {
        queryClient.setQueryData(
          roleSourceKeys.plans(workspaceId, sourceId),
          (current: RoleSourcePlanRecord[] | undefined) => [
            plan,
            ...(current ?? []).filter((item) => item.plan.plan_digest !== plan.plan.plan_digest),
          ],
        );
      }
    },
    onSettled: () => queryClient.invalidateQueries({
      queryKey: roleSourceKeys.plans(workspaceId, sourceId),
    }),
  });
}

export function useCreateRoleSourcePlanApproval(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateRoleSourceApprovalRequest) => api.createRoleSourcePlanApproval(
      workspaceId,
      sourceId,
      planDigest,
      request,
    ),
    onSuccess: (approval) => {
      if (approval) {
        queryClient.setQueryData(
          roleSourceKeys.approvals(workspaceId, sourceId, planDigest),
          (current: RoleSourcePlanApproval[] | undefined) => [
            approval,
            ...(current ?? []).filter((item) => item.id !== approval.id),
          ],
        );
      }
    },
    onSettled: () => queryClient.invalidateQueries({
      queryKey: roleSourceKeys.approvals(workspaceId, sourceId, planDigest),
    }),
  });
}

export function useApplyRoleSourcePlan(
  workspaceId: string,
  sourceId: string,
  planDigest: string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: ApplyRoleSourcePlanRequest) => api.applyRoleSourcePlan(
      workspaceId,
      sourceId,
      planDigest,
      request,
    ),
    onSuccess: (apply) => {
      if (apply) {
        queryClient.setQueryData(
          roleSourceKeys.applies(workspaceId, sourceId),
          (current: RoleSourceApplyResult[] | undefined) => [
            apply,
            ...(current ?? []).filter((item) => item.id !== apply.id),
          ],
        );
      }
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: roleSourceKeys.all(workspaceId) }),
        queryClient.invalidateQueries({ queryKey: roleSourceKeys.applies(workspaceId, sourceId) }),
        queryClient.invalidateQueries({ queryKey: roleSourceKeys.applyFailures(workspaceId, sourceId) }),
      ]);
    },
  });
}
