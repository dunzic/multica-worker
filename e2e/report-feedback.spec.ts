import "./env";
import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { readFile } from "node:fs/promises";

test.use({
  channel: "chrome",
  viewport: { width: 1500, height: 1000 },
  actionTimeout: 15_000,
  trace: "retain-on-failure",
});

async function authenticate(page: Page, api: TestApiClient) {
  await page.addInitScript((token) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("i18nextLng", "en");
    localStorage.setItem("multica:chat:isOpen", "false");
  }, api.getToken());
}

test("generic report: configure, save, collaborate, conflict, audit and revoke", async ({
  page,
  browser,
}, info) => {
  test.setTimeout(180_000);
  const stamp = Date.now();
  const owner = new TestApiClient();
  const editor = new TestApiClient();
  const editorEmail = `report-editor-${stamp}@localhost`;
  await owner.login(`report-owner-${stamp}@localhost`, "Report owner");
  const ws = await owner.ensureWorkspace(
    "Generic report acceptance",
    `report-${stamp}`,
  );
  await owner.markUserOnboarded();
  await editor.login(editorEmail, "Report collaborator");
  await editor.markUserOnboarded();
  const memberId = await owner.addMemberByEmail(editorEmail);
  editor.setWorkspaceId(ws.id);
  editor.setWorkspaceSlug(ws.slug);
  const otherContext = await browser.newContext({
    viewport: { width: 1500, height: 1000 },
  });
  const other = await otherContext.newPage();
  try {
    const issue = await owner.createIssue("Generic quality report");
    const source = await owner.uploadAttachment(
      "质量证据 source-note.txt",
      Buffer.from("Evidence for the quality report"),
      issue.id,
    );
    const pdfSource = await owner.uploadAttachment("原件审阅.pdf", await readFile("e2e/fixtures/hr-batch/candidate-a.pdf"), issue.id);
    const attachment = await owner.uploadAttachment(
      "quality-report.html",
      Buffer.from(`<!doctype html><html><head><style>body{font:18px system-ui;margin:32px;background:#17202a;color:#f5f7fa}a{color:#8ecaff}section{min-height:500px}</style></head><body><h1>Quality report</h1><p>Presentation only. Feedback is saved by the platform.</p><section id="evidence"><h2>Deliverable A</h2><p>Evidence-backed delivery review.</p><a href="/${ws.slug}/attachments/${source.id}/preview" target="_blank"><span>Preview original</span></a> · <a href="/api/attachments/${source.id}/download" target="_blank">Download original</a><p><a href="/${ws.slug}/attachments/${pdfSource.id}/preview" target="_blank">Preview PDF original</a></p></section><section><h2>Follow-up</h2>Shared feedback is in the platform panel.</section><script>document.body.dataset.scriptRan="yes";</script></body></html>`),
      issue.id,
    );
    const comment = await owner.createComment(
      issue.id,
      `Report:\n\n!file[quality-report.html](${attachment.markdown_url})`,
      { attachment_ids: [attachment.id] },
    );
    await authenticate(page, owner);
    await authenticate(other, editor);
    const path = `/${ws.slug}/attachments/${attachment.id}/preview`;
    await page.goto(`/${ws.slug}/issues/${issue.id}`, { timeout: 45_000 });
    await page
      .locator(`[data-comment-content="${comment.id}"]`)
      .getByRole("button", { name: "Preview", exact: true })
      .click();
    await expect(
      page.getByRole("dialog", { name: "quality-report.html", exact: true }),
    ).toBeVisible();
    await expect(
      page.locator('iframe[title="quality-report.html"]'),
    ).toHaveAttribute("sandbox", "allow-scripts", { timeout: 30_000 });
    await page
      .getByRole("button", { name: "Report feedback", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Configure fields", exact: true })
      .click();
    const dialog = page.getByRole("dialog", { name: "Configure fields" });
    await dialog.getByRole("button", { name: "Add field" }).click();
    await dialog.getByLabel("Field name").fill("Review note");
    await dialog.getByRole("button", { name: "Add field" }).click();
    await dialog.getByLabel("Field name").nth(1).fill("Approved");
    await dialog
      .getByRole("combobox", { name: "Type", exact: true })
      .nth(1)
      .selectOption("boolean");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    await page.getByRole("button", { name: "Add record", exact: true }).click();
    const create = page.getByRole("dialog", { name: "Add record" });
    await create.getByLabel("Record name").fill("Deliverable A");
    await create
      .getByRole("textbox", { name: "Review note", exact: true })
      .fill("Original review");
    await create
      .getByRole("combobox", { name: "Approved" })
      .selectOption("false");
    await create.getByLabel("Source attachment ID (optional)").fill(source.id);
    await create.getByRole("button", { name: "Save", exact: true }).click();
    await expect(create).not.toBeVisible();
    await expect(
      page.getByRole("cell", { name: "Original review", exact: true }),
    ).toBeVisible();
    await page.goto(path);
    await page.reload();
    await expect(page.getByRole("button", { name: "Report feedback", exact: true })).toHaveAttribute("aria-expanded", "true");
    await expect(
      page.getByRole("cell", { name: "Original review", exact: true }),
    ).toBeVisible();
    await page.screenshot({ path: info.outputPath("report-entry-visible.png") });
    // Actual sandbox click: preview opens a separately authenticated page, not a download.
    const iframe = page.frameLocator('iframe[title="quality-report.html"]');
    let previewDownloads = 0;
    page.on("download", () => previewDownloads++);
    const previewOpened = page.waitForEvent("popup");
    await iframe.getByRole("link", { name: "Preview original" }).click();
    const preview = await previewOpened;
    await expect(preview).toHaveURL(new RegExp(`/attachments/${source.id}/preview$`));
    await expect(preview.getByText("Evidence for the quality report", { exact: true })).toBeVisible();
    expect(previewDownloads).toBe(0);
    await preview.screenshot({ path: info.outputPath("report-original-preview.png") });
    await preview.close();
    const pdfOpened = page.waitForEvent("popup");
    await iframe.getByRole("link", { name: "Preview PDF original", exact: true }).click();
    const pdfPreview = await pdfOpened;
    await expect(pdfPreview).toHaveURL(new RegExp(`/attachments/${pdfSource.id}/preview$`));
    await expect.poll(async () => {
      for (const frame of pdfPreview.frames()) {
        const count = frame.locator("viewer-page-selector #pagelength");
        if (await count.count() && await count.isVisible() && /\b[1-9]\d*\b/.test(await count.innerText())) return true;
      }
      return false;
    }, { timeout: 30_000 }).toBe(true);
    expect(previewDownloads).toBe(0);
    await pdfPreview.screenshot({ path: info.outputPath("report-pdf-original-preview.png") });
    await pdfPreview.close();
    await expect(page.getByRole("button", { name: "Report feedback", exact: true })).toHaveAttribute("aria-expanded", "true");
    const htmlDownload = page.waitForEvent("download");
    await iframe.getByRole("link", { name: "Download original" }).focus();
    await page.keyboard.press("Enter");
    const original = await htmlDownload;
    expect(original.suggestedFilename()).toBe("质量证据 source-note.txt");
    expect(await readFile((await original.path())!, "utf8")).toBe("Evidence for the quality report");
    // The platform feedback entry uses the same authenticated original-file paths.
    await expect(
      page.getByRole("link", { name: "Preview", exact: true }),
    ).toHaveAttribute("href", `/${ws.slug}/attachments/${source.id}/preview`);
    const downloads = page.waitForEvent("download");
    await page
      .getByRole("table")
      .getByRole("button", { name: "Download", exact: true })
      .click();
    expect((await downloads).suggestedFilename()).toBe("质量证据 source-note.txt");

    await other.goto(path);
    await expect(other.getByRole("button", { name: "Report feedback", exact: true })).toHaveAttribute("aria-expanded", "true");
    await expect(
      other.getByRole("button", { name: "Configure fields" }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "Edit record" }).click();
    const firstDraft = page.getByRole("dialog", { name: "Edit record" });
    await firstDraft
      .getByRole("textbox", { name: "Review note", exact: true })
      .fill("Unsaved concurrent draft");
    await other.getByRole("button", { name: "Edit record" }).click();
    const secondDraft = other.getByRole("dialog", { name: "Edit record" });
    await secondDraft
      .getByRole("textbox", { name: "Review note", exact: true })
      .fill("Collaborator saved feedback");
    await secondDraft
      .getByRole("button", { name: "Save", exact: true })
      .click();
    await expect(secondDraft).not.toBeVisible();
    await firstDraft.getByRole("button", { name: "Save", exact: true }).click();
    await expect(firstDraft.getByRole("alert")).toContainText(
      "Someone changed",
    );
    await expect(
      firstDraft.getByRole("textbox", { name: "Review note", exact: true }),
    ).toHaveValue("Unsaved concurrent draft");
    await page.screenshot({ path: info.outputPath("report-conflict-preserved-draft.png") });
    await firstDraft
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await page
      .getByRole("alertdialog")
      .getByRole("button", { name: "Discard", exact: true })
      .click();
    await expect(firstDraft).not.toBeVisible();
    await expect(
      page.getByRole("cell", {
        name: "Collaborator saved feedback",
        exact: true,
      }),
    ).toBeVisible({ timeout: 12_000 });
    await page.screenshot({
      path: info.outputPath("report-with-feedback.png"),
    });
    await page.getByRole("button", { name: "Change history" }).click();
    await expect(
      page.getByText("Report collaborator · Edited a record"),
    ).toBeVisible();
    await page.getByText("Report collaborator · Edited a record").click();
    const change = page.locator("details[open]");
    await expect(
      change.getByText("Original review", { exact: true }),
    ).toBeVisible();
    await expect(
      change.getByText("Collaborator saved feedback", { exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("report-with-audited-feedback.png"),
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(
      page.getByRole("region", { name: "Report feedback" }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("report-feedback-narrow.png"),
    });
    await owner.removeMember(memberId);
    // The global member-removed handler can navigate away before polling does.
    // Both paths must clear the private report, rather than require one message.
    await expect(other.getByRole("table")).toHaveCount(0, { timeout: 15_000 });
    await expect(
      other.getByText("Collaborator saved feedback", { exact: true }),
    ).toHaveCount(0);
    expect((await editor.reportFeedbackResponse(attachment.id)).status).toBe(
      404,
    );
    await other.screenshot({ path: info.outputPath("report-access-revoked.png") });
  } catch (error) {
    await page
      .screenshot({ path: info.outputPath("failure.png") })
      .catch(() => {});
    throw error;
  } finally {
    await otherContext.close();
    await owner.cleanup();
    await owner.deleteWorkspace();
  }
});
