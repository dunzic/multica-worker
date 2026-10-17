import "./env";
import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";

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
    const attachment = await owner.uploadAttachment(
      "quality-report.html",
      Buffer.from(
        '<!doctype html><html><body><h1>Quality report</h1><p>Presentation only. Feedback is saved by the platform.</p><script>document.body.dataset.scriptRan="yes";</script></body></html>',
      ),
      issue.id,
    );
    const source = await owner.uploadAttachment(
      "source-note.txt",
      Buffer.from("Evidence for the quality report"),
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
    await page
      .getByRole("button", { name: "Report feedback", exact: true })
      .click();
    await expect(
      page.getByRole("cell", { name: "Original review", exact: true }),
    ).toBeVisible();
    // Original file preview remains a page, download remains the original bytes.
    await expect(
      page.getByRole("link", { name: "Preview", exact: true }),
    ).toHaveAttribute("href", `/${ws.slug}/attachments/${source.id}/preview`);
    const downloads = page.waitForEvent("download");
    await page
      .getByRole("table")
      .getByRole("button", { name: "Download", exact: true })
      .click();
    expect((await downloads).suggestedFilename()).toBe("source-note.txt");

    await other.goto(path);
    await other
      .getByRole("button", { name: "Report feedback", exact: true })
      .click();
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
