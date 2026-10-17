// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  isReportFieldEditable,
  ReportFeedbackPageSchema,
  ReportHistoryPageSchema,
  ReportRowSchema,
} from "./schema";
import { reportFeedbackOptions, reportHistoryOptions } from "./queries";

const id = "00000000-0000-4000-8000-000000000001";
const row = {
  id,
  label: "Item",
  values: {
    note: "safe <b>text</b>",
    score: 0,
    checked: false,
    tags: ["a"],
    empty: null,
  },
  revision: 1,
  updated_at: "2026-10-17T00:00:00Z",
};
const page = {
  fields: [{ id: "note", label: "Note", type: "text" }],
  schema_revision: 1,
  rows: [row],
};
afterEach(() => {
  vi.unstubAllGlobals();
  setCurrentWorkspace(null, null);
});

describe("report feedback contract", () => {
  it("preserves typed values and denies absent optional permissions", () => {
    const value = ReportFeedbackPageSchema.parse(page);
    expect(value.canManage).toBe(false);
    expect(value.canEdit).toBe(false);
    expect(value.canCreate).toBe(false);
    expect(value.hasRecords).toBe(true);
    expect(value.rows[0]?.values).toEqual(row.values);
    expect(value.fields[0]?.options).toEqual([]);
  });
  it("reads unknown field types without making them editable", () => {
    const parsed = ReportFeedbackPageSchema.parse({
      ...page,
      fields: [{ id: "future", label: "New type", type: "future" }],
    });
    expect(isReportFieldEditable(parsed.fields[0]!)).toBe(false);
  });
  it.each([
    null,
    {},
    { ...page, rows: null },
    { ...page, schema_revision: -1 },
    { ...page, rows: [{ ...row, revision: 0 }] },
  ])("rejects malformed data %j", (data) => {
    expect(ReportFeedbackPageSchema.safeParse(data).success).toBe(false);
  });
  it("validates rows and history, keeping bigint cursors as strings", () => {
    expect(
      ReportRowSchema.safeParse({ ...row, values: { nested: {} } }).success,
    ).toBe(false);
    const history = ReportHistoryPageSchema.parse({
      changes: [
        {
          id: "9007199254740993",
          row_id: id,
          kind: "update",
          actor_type: "member",
          actor_id: id,
          actor_name: "Person",
          before: null,
          after: row,
          created_at: row.updated_at,
        },
      ],
    });
    expect(history.changes[0]?.id).toBe("9007199254740993");
  });
  it("separates workspace caches and uses permission-checked polling", () => {
    expect(reportFeedbackOptions("one", id).queryKey).not.toEqual(
      reportFeedbackOptions("two", id).queryKey,
    );
    expect(reportHistoryOptions("one", id).queryKey).not.toEqual(
      reportFeedbackOptions("one", id).queryKey,
    );
    expect(reportFeedbackOptions("one", id)).toMatchObject({
      gcTime: 0,
      retry: false,
      refetchInterval: 5000,
    });
  });
});

describe("report feedback API", () => {
  it("scopes every operation explicitly and sends both revisions", async () => {
    // Another desktop tab may change the singleton while this report is open.
    setCurrentWorkspace("another-workspace", "workspace-two");
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(page)))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ fields: page.fields, revision: 2 })),
      )
      .mockResolvedValueOnce(new Response(JSON.stringify(row)))
      .mockResolvedValueOnce(new Response(JSON.stringify({ archived: true })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ changes: [] })));
    vi.stubGlobal("fetch", fetch);
    const api = new ApiClient("https://api.example.test");
    await api.getReportFeedback("workspace-one", id);
    await api.saveReportFeedbackSchema(
      "workspace-one",
      id,
      [
        {
          id: "note",
          label: "Note",
          type: "text",
          required: false,
          options: [],
        },
      ],
      1,
    );
    await api.saveReportFeedbackRow("workspace-one", id, id, {
      label: "Item",
      sourceAttachmentId: "",
      values: row.values,
      schemaRevision: 2,
      expectedRevision: 1,
    });
    await api.archiveReportFeedbackRow("workspace-one", id, id, 2);
    await api.getReportFeedbackHistory("workspace-one", id, "9007199254740993");
    for (const [, init] of fetch.mock.calls) {
      expect(new Headers(init.headers).get("X-Workspace-ID")).toBe(
        "workspace-one",
      );
      expect(new Headers(init.headers).get("X-Workspace-Slug")).toBe("");
    }
    expect(JSON.parse(fetch.mock.calls[2]![1].body)).toMatchObject({
      expected_revision: 1,
      schema_revision: 2,
      values: row.values,
    });
    expect(fetch.mock.calls[4]![0]).toContain("before=9007199254740993");
  });
  it("does not treat invalid save responses as success or empty reads", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => Promise.resolve(new Response("{}"))),
    );
    const api = new ApiClient("https://api.example.test");
    await expect(api.getReportFeedback("ws", id)).rejects.toThrow();
    await expect(api.getReportFeedbackHistory("ws", id)).rejects.toThrow();
    await expect(
      api.saveReportFeedbackSchema("ws", id, [], 0),
    ).rejects.toThrow();
    await expect(
      api.saveReportFeedbackRow("ws", id, id, {
        expectedRevision: 0,
        schemaRevision: 1,
        label: "a",
        sourceAttachmentId: "",
        values: {},
      }),
    ).rejects.toThrow();
    await expect(
      api.archiveReportFeedbackRow("ws", id, id, 1),
    ).rejects.toThrow();
  });
});
