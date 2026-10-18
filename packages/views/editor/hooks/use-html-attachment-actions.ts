"use client";

import { useEffect, useMemo, useRef } from "react";
import { api } from "@multica/core/api";
import { paths } from "@multica/core/paths";
import { toast } from "sonner";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useDownloadAttachment } from "../use-download-attachment";
import type { HtmlAttachmentAction } from "../utils/iframe-attachment-bridge";

/** Execute only named attachment actions, never a document-supplied navigation URL. */
export function useHtmlAttachmentActions(reportId: string, wsId: string) {
  const navigation = useNavigation();
  const { t } = useT("editor");
  const download = useDownloadAttachment("markdown-link");
  const scope = `${wsId}:${reportId}`;
  const currentScope = useRef<string | null>(scope);
  useEffect(() => {
    currentScope.current = scope;
    return () => { currentScope.current = null; };
  }, [scope]);
  const baseUrl = reportId && wsId ? navigation.getShareableUrl("/") : "";
  const apiUrl = api.getBaseUrl();
  const { openInNewTab, getShareableUrl } = navigation;
  return useMemo(() => {
    if (!reportId || !wsId) return undefined;
    const origins = [baseUrl, apiUrl].flatMap((value) => {
      try { const url = new URL(value); return /^https?:$/.test(url.protocol) ? [url.origin] : []; }
      catch { return []; }
    });
    return {
      baseUrl,
      origins,
      onAction: async ({ id, action }: HtmlAttachmentAction) => {
        // Reserve the browser tab during the click's user activation. Close it
        // on denial, failure or a changed source instead of leaving a blank tab.
        const target = action === "preview" && !openInNewTab ? window.open("about:blank", "_blank") : null;
        if (target) {
          target.opener = null;
          target.document.title = t(($) => $.attachment.preview_loading);
          target.document.body.textContent = t(($) => $.attachment.preview_loading);
        }
        try {
          if (action === "preview" && !openInNewTab && !target) throw new Error("Preview tab blocked");
          // A retained report must not keep acting after its own access is revoked.
          const source = await api.getAttachmentForDownload(reportId);
          if (currentScope.current !== scope) { target?.close(); return; }
          if (source.workspace_id !== wsId) throw new Error("Report unavailable");
          if (action === "download") {
            await download(id);
            return;
          }
          const location = await api.resolveAttachmentPreview(id);
          if (!location) throw new Error("Attachment unavailable");
          if (currentScope.current !== scope) { target?.close(); return; }
          const path = paths.workspace(location.workspaceSlug).attachmentPreview(id);
          if (openInNewTab) openInNewTab(path, t(($) => $.attachment.preview), { activate: true });
          else if (target) target.location.replace(getShareableUrl(path));
        } catch {
          target?.close();
          if (currentScope.current === scope) toast.error(t(($) => action === "preview" ? $.attachment.preview_failed : $.attachment.download_failed));
        }
      },
    };
  }, [reportId, wsId, scope, baseUrl, apiUrl, openInNewTab, getShareableUrl, download, t]);
}
