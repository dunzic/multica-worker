import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { OfficePreviewError } from "@multica/core/api";
import type { Attachment } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { OfficePreview } from "./office-preview";

const { convert } = vi.hoisted(() => ({ convert: vi.fn() }));
vi.mock("@multica/core/api", async (original) => ({
  ...await original<typeof import("@multica/core/api")>(),
  api: { getAttachmentOfficePreview: convert },
}));

const attachment: Attachment = {
  id: "att-1", workspace_id: "ws-1", issue_id: null, comment_id: null,
  chat_session_id: null, chat_message_id: null, uploader_type: "member", uploader_id: "user-1",
  filename: "review.docx", content_type: "application/octet-stream", size_bytes: 100,
  created_at: "2026-10-14T00:00:00Z", url: "/private/source", download_url: "/private/source", markdown_url: "/private/source",
};
const download = vi.fn();
let revoke: ReturnType<typeof vi.fn<(url: string) => void>>;

function mount(file = attachment, client = new QueryClient()) {
  return renderWithI18n(<QueryClientProvider client={client}><OfficePreview attachment={file} onDownload={download} /></QueryClientProvider>);
}

beforeEach(() => {
  convert.mockReset().mockResolvedValue(new Blob(["%PDF-test"], { type: "application/pdf" }));
  download.mockReset();
  revoke = vi.fn();
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => "blob:office-preview");
    static revokeObjectURL = revoke;
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("OfficePreview", () => {
  it.each(["review.docx", "budget.xlsx", "slides.pptx"])("shows the converted PDF without loading the raw private URL: %s", async (filename) => {
    const view = mount({ ...attachment, filename });
    expect(screen.getByRole("status")).toHaveTextContent("Preparing PDF preview");
    expect(await screen.findByTitle(filename)).toHaveAttribute("src", "blob:office-preview");
    expect(convert).toHaveBeenCalledWith("att-1", expect.any(AbortSignal));
    view.unmount();
    expect(revoke).toHaveBeenCalledWith("blob:office-preview");
  });

  it("explains whole-sheet rendering for spreadsheets", async () => {
    mount({ ...attachment, filename: "budget.xlsx" });
    await screen.findByTitle("budget.xlsx");
    expect(screen.getByText(/including hidden sheets/)).toBeVisible();
  });

  it("reports missing setup, retains original download and permits retry", async () => {
    convert.mockRejectedValueOnce(new OfficePreviewError("not-configured"));
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent("not enabled on this server");
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(download).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByTitle("review.docx");
    expect(convert).toHaveBeenCalledTimes(2);
  });

  it.each(["too-large", "invalid", "busy", "timeout"] as const)("shows a recoverable %s failure without rendering an iframe", async (reason) => {
    convert.mockRejectedValue(new OfficePreviewError(reason));
    mount();
    await screen.findByRole("alert");
    expect(screen.queryByTitle("review.docx")).toBeNull();
    expect(convert).toHaveBeenCalledOnce();
  });

  it("recovers once after a transient converter failure", async () => {
    convert.mockRejectedValueOnce(new OfficePreviewError("unavailable"));
    mount();
    expect(await screen.findByTitle("review.docx")).toHaveAttribute("src", "blob:office-preview");
    expect(convert).toHaveBeenCalledTimes(2);
  });

  it("stops retrying a persistent converter failure", async () => {
    convert.mockRejectedValue(new OfficePreviewError("unavailable"));
    mount();
    await screen.findByRole("alert");
    expect(convert).toHaveBeenCalledTimes(2);
    expect(screen.queryByTitle("review.docx")).toBeNull();
  });

  it("does not reuse a prior workspace's private preview or bytes after closing", async () => {
    const client = new QueryClient();
    const first = mount(attachment, client);
    await screen.findByTitle("review.docx");
    first.unmount();
    await waitFor(() => expect(client.getQueryCache().getAll()).toHaveLength(0));
    convert.mockRejectedValueOnce(new Error("404"));
    mount({ ...attachment, workspace_id: "ws-2" }, client);
    await screen.findByRole("alert");
    expect(screen.queryByTitle("review.docx")).toBeNull();
    expect(convert).toHaveBeenCalledTimes(2);
  });
});
