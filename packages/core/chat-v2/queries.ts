import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { Principal } from "../types/chat-v2";

export const collaborationKeys = {
  all: (wsId: string) => ["chat-collaboration", wsId] as const,
  conversations: (wsId: string) => ["chat-collaboration", wsId, "conversations"] as const,
  topics: (wsId: string, id: string) => ["chat-collaboration", wsId, "topics", id] as const,
  messages: (wsId: string, id: string) => ["chat-collaboration", wsId, "messages", id] as const,
  runs: (wsId: string, id: string) => ["chat-collaboration", wsId, "runs", id] as const,
};

export const isTopicRunActive = (status: string) => ["queued", "dispatched", "running", "waiting_local_directory"].includes(status);
export const topicRunsOptions = (wsId: string, id: string) => queryOptions({
  queryKey: collaborationKeys.runs(wsId, id), queryFn: () => api.listTopicRunsV2(id), enabled: !!wsId && !!id,
  refetchInterval: (query) => query.state.data?.some((run) => isTopicRunActive(run.status)) ? 2_000 : false,
});
export function useCancelTopicRun(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.cancelTaskById(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: collaborationKeys.all(wsId) }),
  });
}

export const conversationsOptions = (wsId: string) => queryOptions({
  queryKey: collaborationKeys.conversations(wsId), queryFn: () => api.listConversationsV2(), enabled: !!wsId,
});
export const topicsOptions = (wsId: string, id: string) => queryOptions({
  queryKey: collaborationKeys.topics(wsId, id), queryFn: () => api.listTopicsV2(id), enabled: !!wsId && !!id,
});
export const topicMessagesOptions = (wsId: string, id: string) => queryOptions({
  queryKey: collaborationKeys.messages(wsId, id), queryFn: () => api.listTopicMessagesV2(id), enabled: !!wsId && !!id,
});

export function useCreateConversation(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { kind: "group" | "dm"; title?: string; participants: Principal[] }) => api.createConversationV2(data),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: collaborationKeys.all(wsId) }),
  });
}

export function useConversationChange(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data:
      | { action: "rename"; id: string; title: string }
      | { action: "leave"; id: string }
      | { action: "add"; id: string; principal: Principal; role?: string }
      | { action: "remove"; id: string; principal: Principal }) => {
      switch (data.action) {
        case "rename": return api.updateConversationV2(data.id, data.title).then(() => undefined);
        case "leave": return api.leaveConversationV2(data.id);
        case "add": return api.addConversationParticipantV2(data.id, data.principal, data.role);
        case "remove": return api.removeConversationParticipantV2(data.id, data.principal);
      }
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: collaborationKeys.all(wsId) }),
  });
}
export function useCreateTopic(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, title }: { id: string; title: string }) => api.createTopicV2(id, title),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: collaborationKeys.all(wsId) }),
  });
}
export function useSendTopicMessage(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, content, attachmentIds }: { id: string; content: string; attachmentIds?: string[] }) => api.sendTopicMessageV2(id, content, attachmentIds),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: collaborationKeys.all(wsId) }),
  });
}
