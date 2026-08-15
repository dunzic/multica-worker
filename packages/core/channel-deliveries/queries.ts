import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const channelDeliveryKeys = {
  all: (workspaceId: string) => ["channel-deliveries", workspaceId] as const,
  list: (workspaceId: string) => [...channelDeliveryKeys.all(workspaceId), "list"] as const,
};

export function channelDeliveryListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: channelDeliveryKeys.list(workspaceId),
    queryFn: () => api.listChannelDeliveries(workspaceId),
    enabled: Boolean(workspaceId),
  });
}
