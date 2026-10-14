"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { api, OfficePreviewError } from "@multica/core/api";
import type { Attachment } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";

export function OfficePreview({ attachment, onDownload }: {
  attachment: Attachment;
  onDownload: () => void;
}) {
  const { t } = useT("editor");
  const query = useQuery({
    queryKey: ["attachment-office-preview", attachment.workspace_id, attachment.id],
    queryFn: ({ signal }) => api.getAttachmentOfficePreview(attachment.id, signal),
    // Recheck the original ACL on every open; do not retain private PDF bytes
    // after closing the viewer. Cancellation propagates through to conversion.
    gcTime: 0,
    staleTime: 0,
    // A cancelled conversion can restart the stateful Office process. Retry
    // its transient failure once; never retry ACL, format or size failures.
    // Each attempt still rechecks the original attachment's permissions.
    retry: (count, error) => count < 1 && error instanceof OfficePreviewError && error.reason === "unavailable",
    retryDelay: 500,
    refetchOnWindowFocus: false,
  });
  const [object, setObject] = useState<{ blob: Blob; url: string } | null>(null);
  useEffect(() => {
    if (!query.data) return;
    const url = URL.createObjectURL(query.data);
    setObject({ blob: query.data, url });
    return () => URL.revokeObjectURL(url);
  }, [query.data]);

  const url = object?.blob === query.data ? object?.url : undefined;
  if (query.error) {
    const reason = query.error instanceof OfficePreviewError ? query.error.reason : undefined;
    const message = reason === "not-configured" ? t(($) => $.attachment.preview_office_not_configured)
      : reason === "busy" ? t(($) => $.attachment.preview_office_busy)
      : reason === "timeout" ? t(($) => $.attachment.preview_office_timeout)
      : reason === "too-large" ? t(($) => $.attachment.preview_office_too_large)
      : reason === "invalid" ? t(($) => $.attachment.preview_office_invalid)
      : reason === "unsupported" ? t(($) => $.attachment.preview_unsupported)
      : t(($) => $.attachment.preview_failed);
    return (
      <div className="dark flex h-full flex-col items-center justify-center gap-3 px-8 text-center">
        <p role="alert" className="text-body text-muted-foreground">{message}</p>
        <div className="flex gap-2">
          <Button variant="outline" onClick={onDownload}>{t(($) => $.image.download)}</Button>
          <Button variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>{t(($) => $.attachment.preview_retry)}</Button>
        </div>
      </div>
    );
  }
  if (!url || query.isFetching) {
    return (
      <div role="status" className="dark flex h-full items-center justify-center gap-2 text-body text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        {t(($) => $.attachment.preview_office_loading)}
      </div>
    );
  }
  const spreadsheet = /\.xlsx?$/i.test(attachment.filename) || /(?:ms-excel|spreadsheetml)/i.test(attachment.content_type);
  return (
    <div className="flex h-full flex-col gap-2 pb-4">
      <p className="dark px-4 text-caption text-muted-foreground">
        {t(($) => $.attachment.preview_office_notice)}
        {spreadsheet && <> {t(($) => $.attachment.preview_office_sheets)}</>}
      </p>
      <iframe src={url} title={attachment.filename} className="min-h-0 w-full flex-1 rounded-lg bg-background" />
    </div>
  );
}
