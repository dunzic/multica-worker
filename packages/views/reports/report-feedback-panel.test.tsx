import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AnchorHTMLAttributes } from "react";
import type { ReportFeedbackPage } from "@multica/core/report-feedback";
import { ReportFeedbackPanel } from "./report-feedback-panel";
import en from "../locales/en/editor.json";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  save: vi.fn(),
  schema: vi.fn(),
  history: vi.fn(),
  archive: vi.fn(),
  download: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({
  api: {
    getReportFeedback: mocks.get,
    saveReportFeedbackRow: mocks.save,
    saveReportFeedbackSchema: mocks.schema,
    getReportFeedbackHistory: mocks.history,
    archiveReportFeedbackRow: mocks.archive,
  },
}));
vi.mock("../editor/use-download-attachment", () => ({
  useDownloadAttachment: () => mocks.download,
}));
vi.mock("../navigation", () => ({
  AppLink: (props: AnchorHTMLAttributes<HTMLAnchorElement>) => <a {...props} />,
}));
vi.mock("@multica/core/paths", async (original) => ({
  ...(await original<typeof import("@multica/core/paths")>()),
  useWorkspaceSlug: () => "example",
}));
vi.mock("../i18n", () => ({
  useT: () => ({
    t: (
      selector: (value: typeof en) => string,
      params?: Record<string, unknown>,
    ) => {
      let value = selector(en);
      for (const [key, replacement] of Object.entries(params ?? {}))
        value = value.replace(`{{${key}}}`, String(replacement));
      return value;
    },
  }),
}));

const sourceId = "00000000-0000-4000-8000-000000000003";
let page: ReportFeedbackPage;
let client: QueryClient;
beforeEach(() => {
  vi.clearAllMocks();
  page = {
    fields: [
      {
        id: "note",
        label: "Review note",
        type: "text",
        required: false,
        options: [],
      },
      {
        id: "approved",
        label: "Approved",
        type: "boolean",
        required: false,
        options: [],
      },
    ],
    schemaRevision: 1,
    rows: [
      {
        id: "00000000-0000-4000-8000-000000000002",
        label: "Deliverable A",
        sourceAttachmentId: sourceId,
        values: { note: "Initial note", approved: false },
        revision: 1,
        archived: false,
        updatedAt: "2026-10-17T00:00:00Z",
      },
    ],
    nextCursor: "",
    hasRecords: true,
    canCreate: true,
    canManage: true,
    canEdit: true,
  };
  mocks.get.mockImplementation(async () => structuredClone(page));
  mocks.history.mockResolvedValue({ changes: [], nextCursor: "" });
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
});
afterEach(() => client.clear());
function mount() {
  render(
    <QueryClientProvider client={client}>
      <ReportFeedbackPanel wsId="ws-one" attachmentId="report-one" />
    </QueryClientProvider>,
  );
}

describe("generic report feedback", () => {
  it("uses a preview destination distinct from download, without executing report content", async () => {
    mount();
    await screen.findByText("Deliverable A");
    expect(screen.getByRole("link", { name: "Preview" })).toHaveAttribute(
      "href",
      `/example/attachments/${sourceId}/preview`,
    );
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(mocks.download).toHaveBeenCalledWith(sourceId);
    expect(screen.getByRole("table")).toHaveTextContent("No");
  });
  it("saves typed feedback and preserves the snapshot revision during collaborator refreshes", async () => {
    mocks.save.mockImplementation(async (_ws, _att, _id, input) => {
      page.rows[0] = { ...page.rows[0]!, values: input.values, revision: 3 };
      return page.rows[0];
    });
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit record" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Review note" }), {
      target: { value: "My feedback" },
    });
    page.rows[0] = {
      ...page.rows[0]!,
      revision: 2,
      values: { note: "Collaborator", approved: true },
    };
    await act(async () => {
      await client.invalidateQueries();
    });
    expect(screen.getByRole("textbox", { name: "Review note" })).toHaveValue(
      "My feedback",
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(mocks.save).toHaveBeenCalledWith(
        "ws-one",
        "report-one",
        page.rows[0]!.id,
        expect.objectContaining({
          expectedRevision: 1,
          schemaRevision: 1,
          values: { note: "My feedback", approved: false },
        }),
      ),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(await screen.findByText("My feedback")).toBeInTheDocument();
  });
  it("keeps input after conflict and confirms discard", async () => {
    mocks.save.mockRejectedValue(
      Object.assign(new Error("conflict"), { status: 409 }),
    );
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit record" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Review note" }), {
      target: { value: "Keep this" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Someone changed",
    );
    expect(screen.getByRole("textbox", { name: "Review note" })).toHaveValue(
      "Keep this",
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      "Discard unsaved changes?",
    );
    fireEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("textbox", { name: "Review note" })).toHaveValue(
      "Keep this",
    );
  });
  it("hides cached private rows, history and open drafts on revoked access", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit record" }));
    mocks.get.mockRejectedValue(
      Object.assign(new Error("not found"), { status: 404 }),
    );
    await act(async () => {
      await client.invalidateQueries();
    });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "access has changed",
    );
    expect(screen.queryByText("Deliverable A")).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
  it("preserves the open draft after a transient refresh failure", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit record" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Review note" }), {
      target: { value: "Keep this offline draft" },
    });
    mocks.get.mockRejectedValue(new TypeError("Failed to fetch"));
    await act(async () => {
      await client.invalidateQueries();
    });
    expect(screen.getByRole("textbox", { name: "Review note" })).toHaveValue(
      "Keep this offline draft",
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(mocks.save).not.toHaveBeenCalled();
  });
  it("offers an explicit retry after a transient refresh failure without clearing saved rows", async () => {
    mount();
    await screen.findByText("Deliverable A");
    mocks.get.mockRejectedValue(new TypeError("Failed to fetch"));
    await act(async () => { await client.invalidateQueries(); });
    expect(await screen.findByRole("alert")).toHaveTextContent(en.report_feedback.refresh_failed);
    expect(screen.getByText("Initial note")).toBeInTheDocument();
    mocks.get.mockResolvedValue(structuredClone(page));
    fireEvent.click(screen.getByRole("button", { name: en.attachment.preview_retry }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(screen.getByText("Initial note")).toBeInTheDocument();
    expect(mocks.save).not.toHaveBeenCalled();
  });
  it("keeps unsaved input when a collaborator archives its record", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit record" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Review note" }), {
      target: { value: "Do not discard this draft" },
    });
    page.rows = [];
    await act(async () => {
      await client.invalidateQueries();
    });
    expect(screen.getByRole("textbox", { name: "Review note" })).toHaveValue(
      "Do not discard this draft",
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    fireEvent.click(
      screen.getByRole("button", { name: "Discard" }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(screen.queryByText("Deliverable A")).not.toBeInTheDocument();
  });
  it("caps configuration at 20 and prevents destructive edits of populated fields", async () => {
    page.fields = Array.from({ length: 20 }, (_, index) => ({
      id: `f_${index}`,
      label: `Field ${index}`,
      type: "text",
      required: false,
      options: [],
    }));
    mount();
    fireEvent.click(
      await screen.findByRole("button", { name: "Configure fields" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByRole("button", { name: "Add field" }),
    ).toBeDisabled();
    for (const button of within(dialog).getAllByRole("button", {
      name: "Remove field",
    }))
      expect(button).toBeDisabled();
    for (const input of within(dialog).getAllByRole("combobox"))
      expect(input).toBeDisabled();
  });
  it("offers only authorized controls and leaves future fields read-only", async () => {
    page.canManage = false;
    page.fields[0]!.type = "future_type";
    mount();
    await screen.findByText("Deliverable A");
    expect(
      screen.queryByRole("button", { name: "Edit record" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Configure fields" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Archive" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Initial note")).toBeInTheDocument();
  });
  it("shows attributed before/after history as escaped data", async () => {
    mocks.history.mockResolvedValue({
      changes: [
        {
          id: "1",
          rowId: page.rows[0]!.id,
          kind: "update",
          actorName: "Collaborator",
          actorId: "user",
          actorType: "member",
          createdAt: "2026-10-17T00:00:00Z",
          before: { label: "Item", values: { note: "Before text" } },
          after: {
            label: "Item",
            values: { note: "<script>danger()</script>" },
          },
        },
      ],
      nextCursor: "",
    });
    mount();
    fireEvent.click(
      await screen.findByRole("button", { name: "Change history" }),
    );
    await screen.findByText(/Collaborator/);
    expect(screen.getByText("Before text")).toBeInTheDocument();
    expect(screen.getByText("<script>danger()</script>")).toBeInTheDocument();
    expect(document.querySelector("script")).toBeNull();
  });
});
