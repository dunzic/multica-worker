import "./env";
import { test, expect, type Page } from "@playwright/test";
import { readFile, writeFile } from "node:fs/promises";
import { TestApiClient } from "./fixtures";
import { slidesFixture, spreadsheetFixture } from "./office-preview-fixtures";

test.use({ channel: "chrome", viewport: { width: 1440, height: 1000 }, actionTimeout: 30_000 });
test.skip(process.env.MULTICA_OFFICE_PREVIEW_ACCEPTANCE !== "1", "Requires an explicitly configured, isolated Office preview test service");

function assertIsolatedEnvironment() {
  const base = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
  expect(["localhost", "127.0.0.1"]).toContain(new URL(base).hostname);
  expect(new URL(process.env.DATABASE_URL!).pathname).toMatch(/^\/multica_.+/);
}

async function waitForRenderedPDF(page: Page) {
  // A pdf-viewer host appears before the PDF has finished loading. Wait for
  // its page count too so an empty Chrome plugin is not acceptance evidence.
  await expect.poll(async () => {
    for (const frame of page.frames()) {
      const selector = frame.locator("viewer-page-selector #pagelength");
      if (await selector.count() && await selector.isVisible()) {
        const text = await selector.innerText();
        if (/\b[1-9]\d*\b/.test(text)) return true;
      }
    }
    return false;
  }, { timeout: 30_000 }).toBe(true);
  // PDFium paints outside the DOM after reporting its page count. Give the
  // native surface a short paint interval before saving visual QA evidence.
  await page.waitForTimeout(500);
}

test("Office previews work in issue bodies, attachment lists, comments and replies", async ({ page }, info) => {
  test.setTimeout(240_000);
  assertIsolatedEnvironment();
  const api = new TestApiClient();
  await api.login(`office-preview-${Date.now()}@localhost`, "Office Preview Acceptance");
  const ws = await api.ensureWorkspace("Office preview acceptance", `office-preview-${Date.now()}`);
  await api.markUserOnboarded();
  const inputs = [
    { filename: "Word-review.docx", bytes: await readFile("e2e/fixtures/hr-batch/candidate-b.docx") },
    { filename: "Excel-review.xlsx", bytes: spreadsheetFixture() },
    { filename: "PPT-review.pptx", bytes: slidesFixture() },
  ];
  try {
    const issue = await api.createIssue("Office preview acceptance");
    const uploaded = [];
    for (const input of inputs) {
      const attachment = await api.uploadAttachment(input.filename, input.bytes, issue.id);
      uploaded.push(attachment);
      const converted = await api.attachmentResponse(attachment.id, "office-preview");
      expect(converted.status, input.filename).toBe(200);
      expect(converted.headers.get("content-type")).toBe("application/pdf");
      expect(converted.headers.get("cache-control")).toBe("no-store");
      const pdf = Buffer.from(await converted.arrayBuffer());
      expect(pdf.subarray(0, 5).toString()).toBe("%PDF-");
      await info.attach(`${input.filename}.pdf`, { body: pdf, contentType: "application/pdf" });
      await writeFile(info.outputPath(`${input.filename}.pdf`), pdf);
      const original = await api.attachmentResponse(attachment.id, "download");
      expect(original.status).toBe(200);
      expect(Buffer.from(await original.arrayBuffer())).toEqual(input.bytes);
    }
    const [word, excel, ppt] = uploaded;
    // Use the exact backend response: hand-built relative paths hide split
    // app/API origin regressions in self-hosted deployments.
    const bodyLink = word!.markdown_url;
    expect(new URL(bodyLink).pathname, "Acceptance requires protected attachment links, not public /uploads URLs")
      .toBe(`/api/attachments/${word!.id}/download`);
    expect(new URL(bodyLink).origin).not.toBe(new URL(info.project.use.baseURL!).origin);
    await api.updateIssue(issue.id, { description: `Review document: [Review Word body](${bodyLink})\n\n!file[Word-review.docx](${bodyLink})` });
    const comment = await api.createComment(issue.id, "Spreadsheet comment", { attachment_ids: [excel!.id] });
    const reply = await api.createComment(issue.id, "Slides reply", { parent_id: comment.id, attachment_ids: [ppt!.id] });
    await api.createComment(issue.id, `Copied file reference: [Review original Word](${bodyLink})`);
    const copied = await api.createComment(issue.id, `Copied file card:\n\n!file[Word-review.docx](${bodyLink})`);

    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
      localStorage.setItem("i18nextLng", "en");
    }, api.getToken());
    const issuePath = `/${ws.slug}/issues/${issue.id}`;
    const assertPDF = async (filename: string) => {
      await expect(page.locator(`iframe[title="${filename}"]`)).toHaveAttribute("src", /^blob:/, { timeout: 60_000 });
      await waitForRenderedPDF(page);
      await page.screenshot({ path: info.outputPath(`${filename}.png`) });
    };
    await page.goto(issuePath);
    await page.locator(`[data-comment-content="description:${issue.id}"]`).getByRole("button", { name: "Preview", exact: true }).click();
    await assertPDF(word!.filename);
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await page.getByRole("link", { name: "Review Word body", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/attachments/${word!.id}/preview`));
    await assertPDF(word!.filename);

    await page.goto(issuePath);
    const copiedBlock = page.locator(`[data-comment-content="${copied.id}"]`);
    await copiedBlock.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/attachments/${word!.id}/preview`));
    await assertPDF(word!.filename);

    await page.goto(issuePath);
    await page.getByRole("link", { name: "Review original Word", exact: true }).click();
    await assertPDF(word!.filename);

    for (const [id, file] of [[comment.id, excel!], [reply.id, ppt!]] as const) {
      await page.goto(issuePath);
      const content = page.locator(`[data-comment-content="${id}"]`);
      await expect(content).toBeVisible({ timeout: 30_000 });
      const block = content.locator("xpath=ancestor::*[@data-comment-block][1]");
      await block.getByRole("button", { name: file.filename, exact: true }).click();
      await assertPDF(file.filename);
      await page.getByRole("button", { name: "Close", exact: true }).click();
    }
  } catch (error) {
    await page.screenshot({ path: info.outputPath("failure-before-cleanup.png") });
    throw error;
  } finally {
    await api.cleanup();
    await api.deleteWorkspace();
  }
});

test("existing Markdown, CSV, JSON and PDF previews still work from comments", async ({ page }, info) => {
  test.setTimeout(120_000);
  assertIsolatedEnvironment();
  const api = new TestApiClient();
  await api.login(`existing-preview-${Date.now()}@localhost`, "Preview Regression");
  const ws = await api.ensureWorkspace("Preview regression", `existing-preview-${Date.now()}`);
  await api.markUserOnboarded();
  try {
    const issue = await api.createIssue("Existing preview regression");
    const files = [
      { filename: "notes.md", bytes: Buffer.from("# Markdown acceptance\n\nOriginal preview remains available.") },
      { filename: "scores.csv", bytes: Buffer.from("candidate,score\nSynthetic CSV candidate,95\n") },
      { filename: "result.json", bytes: Buffer.from('{"result":"JSON acceptance","count":3}') },
      { filename: "original.pdf", bytes: await readFile("e2e/fixtures/hr-batch/candidate-a.pdf") },
    ];
    for (const file of files) {
      const attachment = await api.uploadAttachment(file.filename, file.bytes, issue.id);
      await api.createComment(issue.id, `[${file.filename}](/api/attachments/${attachment.id}/download)`);
    }
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
      localStorage.setItem("i18nextLng", "en");
    }, api.getToken());
    for (const file of files) {
      await page.goto(`/${ws.slug}/issues/${issue.id}`);
      await page.getByRole("link", { name: file.filename, exact: true }).click();
      if (file.filename.endsWith(".md")) await expect(page.getByRole("heading", { name: "Markdown acceptance" })).toBeVisible();
      if (file.filename.endsWith(".csv")) await expect(page.getByRole("cell", { name: "Synthetic CSV candidate", exact: true })).toBeVisible();
      if (file.filename.endsWith(".json")) await expect(page.getByText('"JSON acceptance"', { exact: true })).toBeVisible();
      if (file.filename.endsWith(".pdf")) {
        // Existing PDF preview may use a scoped signed URL or an authenticated
        // blob. The rendered PDF, not its transport choice, is the contract.
        await expect(page.locator('iframe[title="original.pdf"]')).toBeVisible({ timeout: 30_000 });
        await waitForRenderedPDF(page);
      }
      await page.screenshot({ path: info.outputPath(`${file.filename}.png`) });
    }
  } catch (error) {
    await page.screenshot({ path: info.outputPath("failure-before-cleanup.png") });
    throw error;
  } finally {
    await api.cleanup();
    await api.deleteWorkspace();
  }
});

test("copied attachments resolve their owning workspace and respect revoked membership", async ({ page }, info) => {
  test.setTimeout(180_000);
  assertIsolatedEnvironment();
  const stamp = Date.now();
  const source = new TestApiClient();
  const reader = new TestApiClient();
  const readerEmail = `preview-reader-${stamp}@localhost`;
  await source.login(`preview-owner-${stamp}@localhost`, "Preview Owner");
  const owning = await source.ensureWorkspace("Attachment owner", `preview-owner-${stamp}`);
  await reader.login(readerEmail, "Preview Reader");
  const viewing = await reader.ensureWorkspace("Comment reader", `preview-reader-${stamp}`);
  await reader.markUserOnboarded();
  try {
    const memberId = await source.addMemberByEmail(readerEmail);
    const sourceIssue = await source.createIssue("Source attachment");
    const file = await source.uploadAttachment("cross-workspace.docx",
      await readFile("e2e/fixtures/hr-batch/candidate-b.docx"), sourceIssue.id);
    expect(new URL(file.markdown_url).pathname).toBe(`/api/attachments/${file.id}/download`);
    const issue = await reader.createIssue("Copied attachment links");
    // A query hint is not ownership evidence; the database must win.
    const wrongHint = `${file.markdown_url}?workspace_slug=${viewing.slug}`;
    const comment = await reader.createComment(issue.id,
      `Review document: [Cross workspace file](${wrongHint})\n\n!file[cross-workspace.docx](${file.markdown_url})`);
    const location = await reader.attachmentResponse(file.id, "preview-location");
    expect(location.status).toBe(200);
    expect(await location.json()).toEqual({ workspace_id: owning.id, workspace_slug: owning.slug });
    expect((await reader.attachmentResponse(file.id, "download")).status).toBe(200);
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
      localStorage.setItem("i18nextLng", "en");
    }, reader.getToken());
    const issuePath = `/${viewing.slug}/issues/${issue.id}`;
    for (const mode of ["link", "card"]) {
      await page.goto(issuePath);
      if (mode === "link") await page.getByRole("link", { name: "Cross workspace file", exact: true }).click();
      else await page.locator(`[data-comment-content="${comment.id}"]`).getByRole("button", { name: "Preview", exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/${owning.slug}/attachments/${file.id}/preview`));
      await expect(page.locator('iframe[title="cross-workspace.docx"]')).toHaveAttribute("src", /^blob:/, { timeout: 60_000 });
      await waitForRenderedPDF(page);
      await page.screenshot({ path: info.outputPath(`cross-workspace-${mode}.png`) });
    }
    await source.removeMember(memberId);
    expect((await reader.attachmentResponse(file.id, "preview-location")).status).toBe(404);
    expect((await reader.attachmentResponse(file.id, "download")).status).toBe(404);
    await page.goto(issuePath);
    await page.getByRole("link", { name: "Cross workspace file", exact: true }).click();
    await expect(page.getByTestId("attachment-preview-page-error")).toBeVisible();
    await expect(page.locator('iframe[title="cross-workspace.docx"]')).toHaveCount(0);
    await page.screenshot({ path: info.outputPath("cross-workspace-denied.png") });
  } catch (error) {
    await page.screenshot({ path: info.outputPath("failure-before-cleanup.png") });
    throw error;
  } finally {
    await reader.cleanup();
    await reader.deleteWorkspace();
    await source.cleanup();
    await source.deleteWorkspace();
  }
});

test("PPT conversion remains usable after cancellation and repeated conversions", async () => {
  test.setTimeout(120_000);
  assertIsolatedEnvironment();
  const api = new TestApiClient();
  await api.login(`preview-lifecycle-${Date.now()}@localhost`, "Preview Lifecycle");
  await api.ensureWorkspace("Preview lifecycle", `preview-lifecycle-${Date.now()}`);
  try {
    const issue = await api.createIssue("Synthetic conversion lifecycle");
    const file = await api.uploadAttachment("lifecycle.pptx", slidesFixture(), issue.id);
    for (let i = 0; i < 12; i++) {
      if (i % 2 === 0) {
        const controller = new AbortController();
        const cancelled = api.attachmentResponse(file.id, "office-preview", controller.signal).catch(() => null);
        await new Promise((resolve) => setTimeout(resolve, 20));
        controller.abort();
        await cancelled;
      }
      let result = await api.attachmentResponse(file.id, "office-preview");
      // Match the viewer's one retry for a restarted conversion process.
      if (result.status === 502) {
        await result.arrayBuffer();
        await new Promise((resolve) => setTimeout(resolve, 500));
        result = await api.attachmentResponse(file.id, "office-preview");
      }
      expect(result.status, `conversion ${i + 1}`).toBe(200);
      expect(Buffer.from(await result.arrayBuffer()).subarray(0, 5).toString()).toBe("%PDF-");
    }
  } finally {
    await api.cleanup();
    await api.deleteWorkspace();
  }
});
