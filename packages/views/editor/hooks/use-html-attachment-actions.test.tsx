import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useHtmlAttachmentActions } from "./use-html-attachment-actions";

const mocks = vi.hoisted(() => ({
  source: vi.fn(), resolve: vi.fn(), download: vi.fn(), open: vi.fn(), error: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: { getBaseUrl: () => "https://api.example", getAttachmentForDownload: mocks.source, resolveAttachmentPreview: mocks.resolve } }));
vi.mock("../../navigation", () => ({ useNavigation: () => ({ openInNewTab: mocks.open, getShareableUrl: (path: string) => `https://app.example${path}` }) }));
vi.mock("../../i18n", () => ({ useT: () => ({ t: (select: (v: unknown) => string) => select({ attachment: { preview: "Preview", preview_failed: "Preview failed", download_failed: "Download failed" } }) }) }));
vi.mock("../use-download-attachment", () => ({ useDownloadAttachment: () => mocks.download }));
vi.mock("sonner", () => ({ toast: { error: mocks.error } }));

beforeEach(() => {
  vi.clearAllMocks();
  mocks.source.mockResolvedValue({ workspace_id: "ws" });
  mocks.resolve.mockResolvedValue({ workspaceId: "target-ws", workspaceSlug: "owning-workspace" });
});
describe("authenticated report actions", () => {
  it("checks the report then opens the target in its owning workspace, preserving the report", async () => {
    const { result } = renderHook(() => useHtmlAttachmentActions("report", "ws"));
    await act(() => result.current!.onAction({ id: "source-file", action: "preview" }));
    expect(mocks.source).toHaveBeenCalledWith("report");
    expect(mocks.resolve).toHaveBeenCalledWith("source-file");
    expect(mocks.open).toHaveBeenCalledWith("/owning-workspace/attachments/source-file/preview", "Preview", { activate: true });
    expect(mocks.download).not.toHaveBeenCalled();
  });
  it("uses authenticated forced download rather than opening a preview or trusting the HTML URL", async () => {
    const { result } = renderHook(() => useHtmlAttachmentActions("report", "ws"));
    await act(() => result.current!.onAction({ id: "source-file", action: "download" }));
    expect(mocks.source).toHaveBeenCalledWith("report");
    expect(mocks.download).toHaveBeenCalledWith("source-file");
    expect(mocks.resolve).not.toHaveBeenCalled();
    expect(mocks.open).not.toHaveBeenCalled();
  });
  it.each(["preview", "download"] as const)("denies %s when access to the original report was revoked", async (action) => {
    mocks.source.mockRejectedValue(new Error("404"));
    const { result } = renderHook(() => useHtmlAttachmentActions("report", "ws"));
    await act(() => result.current!.onAction({ id: "target", action }));
    expect(mocks.error).toHaveBeenCalledOnce();
    expect(mocks.resolve).not.toHaveBeenCalled();
    expect(mocks.open).not.toHaveBeenCalled();
    expect(mocks.download).not.toHaveBeenCalled();
  });
  it("does not navigate after the reader closes the report during authorization", async () => {
    let complete!: (data: { workspace_id: string }) => void;
    mocks.source.mockImplementation(() => new Promise((resolve) => { complete = resolve; }));
    const { result, unmount } = renderHook(() => useHtmlAttachmentActions("report", "ws"));
    const pending = result.current!.onAction({ id: "target", action: "preview" });
    unmount();
    await act(async () => { complete({ workspace_id: "ws" }); await pending; });
    expect(mocks.resolve).not.toHaveBeenCalled();
    expect(mocks.open).not.toHaveBeenCalled();
  });
  it("reports missing or forbidden source files without navigating", async () => {
    mocks.resolve.mockResolvedValue(null);
    const { result } = renderHook(() => useHtmlAttachmentActions("report", "ws"));
    await act(() => result.current!.onAction({ id: "target", action: "preview" }));
    expect(mocks.error).toHaveBeenCalledWith("Preview failed");
    expect(mocks.open).not.toHaveBeenCalled();
  });
});
