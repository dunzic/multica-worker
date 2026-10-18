import "./env";
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { TestApiClient } from "./fixtures";

test.use({ channel: "chrome", viewport: { width: 1500, height: 1000 }, trace: "retain-on-failure" });

test("generic report: 20 fields, pagination, all value types and preserved identities", async ({ page }, info) => {
  test.setTimeout(180_000);
  const owner = new TestApiClient();
  const stamp = Date.now();
  await owner.login(`report-boundary-${stamp}@localhost`, "Quality reviewer");
  const ws = await owner.ensureWorkspace("Quality review boundaries", `quality-boundary-${stamp}`);
  await owner.markUserOnboarded();
  try {
    const issue = await owner.createIssue("Quality checklist");
    const report = await owner.uploadAttachment("quality-checklist.html", Buffer.from("<!doctype html><html><body><h1>Quality checklist</h1><p>51 review items. Feedback is managed by the platform.</p></body></html>"), issue.id);
    const fields = [
      { id: "note", label: "Review note", type: "text", required: false, options: [] },
      { id: "score", label: "Quality score", type: "number", required: false, options: [] },
      { id: "date", label: "Follow-up date", type: "date", required: false, options: [] },
      { id: "confirmed", label: "Confirmed", type: "boolean", required: false, options: [] },
      { id: "decision", label: "Review decision", type: "select", required: false, options: ["Follow up", "Approved"] },
      { id: "areas", label: "Review areas", type: "multi_select", required: false, options: ["Evidence", "Delivery", "Documentation"] },
      ...Array.from({ length: 14 }, (_, i) => ({ id: `detail_${i}`, label: `Additional detail ${i + 1}`, type: "text", required: false, options: [] })),
    ];
    const schema = await owner.reportFeedbackResponse(report.id, "schema", "PUT", { expected_revision: 0, fields });
    expect(schema.status).toBe(200);
    const ids = Array.from({ length: 51 }, () => randomUUID()).sort();
    for (const [index, id] of ids.entries()) {
      const response = await owner.reportFeedbackResponse(report.id, `rows/${id}`, "PUT", {
        expected_revision: 0, schema_revision: 1, label: `Review item ${String(index + 1).padStart(2, "0")}`,
        source_attachment_id: "", values: { note: `Existing human note ${index + 1}` },
      });
      expect(response.status).toBe(200);
    }
    const baseline = await (await owner.reportFeedbackResponse(report.id)).json();
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("i18nextLng", "en");
      localStorage.setItem("multica:chat:isOpen", "false");
    }, owner.getToken());
    await page.goto(`/${ws.slug}/attachments/${report.id}/preview`);
    const feedback = page.getByRole("region", { name: "Report feedback", exact: true });
    await expect(feedback.getByRole("table").getByRole("row")).toHaveCount(51);
    await page.getByRole("button", { name: "Configure fields", exact: true }).click();
    const schemaEditor = page.getByRole("dialog", { name: "Configure fields" });
    await expect(schemaEditor.getByRole("button", { name: "Add field" })).toBeDisabled();
    await schemaEditor.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.getByRole("button", { name: "Load more", exact: true }).click();
    const last = page.getByRole("row").filter({ has: page.getByRole("rowheader", { name: /^Review item 51/ }) });
    await last.getByRole("button", { name: "Edit record", exact: true }).click();
    const edit = page.getByRole("dialog", { name: "Edit record" });
    await expect(edit.getByRole("textbox", { name: "Review note", exact: true })).toHaveValue("Existing human note 51");
    await edit.getByRole("textbox", { name: "Review note", exact: true }).fill("Quality follow-up after reviewing evidence. ".repeat(10));
    await edit.getByRole("spinbutton", { name: "Quality score" }).fill("88.5");
    await edit.getByLabel("Follow-up date", { exact: true }).fill("2026-10-20");
    await edit.getByRole("combobox", { name: "Confirmed", exact: true }).selectOption("false");
    await edit.getByRole("combobox", { name: "Review decision", exact: true }).selectOption("Follow up");
    await edit.getByRole("checkbox", { name: "Evidence", exact: true }).check();
    await edit.getByRole("checkbox", { name: "Documentation", exact: true }).check();
    await page.screenshot({ path: info.outputPath("twenty-fields-edit.png") });
    await edit.getByRole("button", { name: "Save", exact: true }).click();
    await expect(edit).not.toBeVisible();
    await page.reload();
    await page.getByRole("button", { name: "Load more", exact: true }).click();
    await expect(last).toContainText("Quality follow-up after reviewing evidence.");
    await expect(last).toContainText("88.5");
    await expect(last).toContainText("2026-10-20");
    await expect(last).toContainText("Evidence, Documentation");
    const after = await (await owner.reportFeedbackResponse(report.id)).json();
    expect(after.rows).toEqual(baseline.rows);
    const history = await (await owner.reportFeedbackResponse(report.id, "history")).json();
    expect(history.changes[0].row_id).toBe(ids[50]);
    expect(history.changes[0].after.values).toMatchObject({ confirmed: false, score: 88.5, date: "2026-10-20", decision: "Follow up", areas: ["Evidence", "Documentation"] });
    await page.screenshot({ path: info.outputPath("fifty-one-records-saved.png") });
    await page.setViewportSize({ width: 390, height: 844 });
    await last.getByRole("button", { name: "Edit record", exact: true }).click();
    await expect(edit.getByRole("textbox", { name: "Review note", exact: true })).toBeVisible();
    await edit.getByRole("textbox", { name: "Review note", exact: true }).fill("Narrow layout draft is preserved");
    await page.keyboard.press("Escape");
    const discard = page.getByRole("alertdialog");
    await expect(discard).toBeVisible();
    await discard.getByRole("button", { name: "Keep editing", exact: true }).click();
    await expect(edit.getByRole("textbox", { name: "Review note", exact: true })).toHaveValue("Narrow layout draft is preserved");
    await page.screenshot({ path: info.outputPath("narrow-fields-preserved-draft.png") });
  } finally {
    await owner.cleanup();
    await owner.deleteWorkspace();
  }
});
