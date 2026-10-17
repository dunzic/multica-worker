import {
  infiniteQueryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import type { ReportField, SaveReportRow } from "./schema";

export const reportFeedbackKeys = {
  all: (wsId: string, attachmentId: string) =>
    ["report-feedback", wsId, attachmentId] as const,
  rows: (wsId: string, attachmentId: string) =>
    [...reportFeedbackKeys.all(wsId, attachmentId), "rows"] as const,
  history: (wsId: string, attachmentId: string) =>
    [...reportFeedbackKeys.all(wsId, attachmentId), "history"] as const,
};

export function reportFeedbackOptions(wsId: string, attachmentId: string) {
  return infiniteQueryOptions({
    queryKey: reportFeedbackKeys.rows(wsId, attachmentId),
    queryFn: ({ pageParam, signal }) =>
      api.getReportFeedback(wsId, attachmentId, pageParam, signal),
    initialPageParam: "",
    getNextPageParam: (page) => page.nextCursor || undefined,
    enabled: !!wsId && !!attachmentId,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    // Collaboration is deliberately permission-checked polling, not a
    // workspace broadcast carrying private conversation data.
    refetchInterval: 5000,
  });
}

export function reportHistoryOptions(wsId: string, attachmentId: string) {
  return infiniteQueryOptions({
    queryKey: reportFeedbackKeys.history(wsId, attachmentId),
    queryFn: ({ pageParam, signal }) =>
      api.getReportFeedbackHistory(wsId, attachmentId, pageParam, signal),
    initialPageParam: "",
    getNextPageParam: (page) => page.nextCursor || undefined,
    enabled: !!wsId && !!attachmentId,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchInterval: 5000,
  });
}

export function useReportFeedbackMutations(wsId: string, attachmentId: string) {
  const client = useQueryClient();
  const invalidate = () =>
    client.invalidateQueries({
      queryKey: reportFeedbackKeys.all(wsId, attachmentId),
    });
  const schema = useMutation({
    mutationFn: (input: { fields: ReportField[]; revision: number }) =>
      api.saveReportFeedbackSchema(
        wsId,
        attachmentId,
        input.fields,
        input.revision,
      ),
    onSuccess: invalidate,
  });
  const row = useMutation({
    mutationFn: (input: SaveReportRow & { id: string }) =>
      api.saveReportFeedbackRow(wsId, attachmentId, input.id, input),
    onSuccess: invalidate,
  });
  const archive = useMutation({
    mutationFn: (input: { id: string; revision: number }) =>
      api.archiveReportFeedbackRow(
        wsId,
        attachmentId,
        input.id,
        input.revision,
      ),
    onSuccess: invalidate,
  });
  return { schema, row, archive };
}
