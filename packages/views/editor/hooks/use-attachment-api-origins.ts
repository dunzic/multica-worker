import { useMemo } from "react";
import { api } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";

/** Only deployment-owned origins may reinterpret an attachment ID as local. */
export function useAttachmentApiOrigins(): readonly string[] {
  const publicOrigin = useConfigStore((state) => state.attachmentApiOrigin);
  const baseUrl = api.getBaseUrl?.() ?? "";
  return useMemo(() => [baseUrl, publicOrigin], [baseUrl, publicOrigin]);
}
