import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
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
