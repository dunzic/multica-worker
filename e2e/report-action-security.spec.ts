import "./env";
import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { TestApiClient } from "./fixtures";

test.use({ channel: "chrome", viewport: { width: 1500, height: 1000 }, trace: "retain-on-failure" });

test("report actions keep the HTML sandbox, recheck private originals and recover from failures", async ({ page }, info) => {
  test.setTimeout(180_000);
  const stamp = Date.now();
  const owner = new TestApiClient();
  const reader = new TestApiClient();
  await owner.login(`action-owner-${stamp}@localhost`, "Report owner");
  const ws = await owner.ensureWorkspace("Report action boundaries", `action-${stamp}`);
  await owner.markUserOnboarded();
  await reader.login(`action-reader-${stamp}@localhost`, "Report reader");
  await owner.addMemberByEmail(reader.getEmail());
  await reader.markUserOnboarded();
  reader.setWorkspaceId(ws.id);
  reader.setWorkspaceSlug(ws.slug);
  try {
    const issue = await owner.createIssue("Delivery action boundaries");
    const publicFile = await owner.uploadAttachment("delivery-note.txt", Buffer.from("Authorized delivery evidence"), issue.id);
    const privateGroup = await owner.createGroupChatFixture("Restricted evidence team");
    const privateFile = await owner.uploadTopicAttachment(privateGroup.topicId, "private-source.txt", Buffer.from("Restricted evidence must not leak"));
    await owner.sendGroupMessage(privateGroup.topicId, "Private evidence", [privateFile.id]);
    const report = await owner.uploadAttachment("action-boundaries.html", Buffer.from(`<!doctype html><html><body>
      <h1>Delivery action boundaries</h1>
      <a href="/${ws.slug}/attachments/${publicFile.id}/preview" target="_blank">Preview allowed original</a>
      <a href="/api/attachments/${publicFile.id}/download" target="_blank">Download allowed original</a>
      <a href="/${ws.slug}/attachments/${privateFile.id}/preview" target="_blank">Preview restricted original</a>
      <a href="/api/attachments/${privateFile.id}/download" target="_blank">Download restricted original</a>
      <button id="negative">Try unsupported actions</button><output id="probe"></output>
      <script>
        var bridge=Array.from(document.scripts).find(function(s){return s.textContent.includes('var S=')});
        var scope=JSON.parse(bridge.textContent.match(/var S=("[^"\\n]+")/)[1]);
        var requests=[{scope:scope,href:'javascript:alert(1)'},{scope:scope,href:'https://invalid.example/api/attachments/${publicFile.id}/download'},{scope:'old-report',href:'/api/attachments/${publicFile.id}/download'}];
        function unsupported(){
          requests.forEach(function(value){parent.postMessage(Object.assign({__multicaAttachment:1},value),'*')});
          var fake=new MessageChannel();
          parent.postMessage({__multicaAttachment:1,type:'connect',scope:scope},'*',[fake.port2]);
          fake.port1.postMessage({__multicaAttachment:1,scope:scope,href:'/api/attachments/${publicFile.id}/download'});
        }
        document.getElementById('negative').onclick=unsupported;
        parent.postMessage({__multicaAttachment:1,scope:scope,href:'/api/attachments/${publicFile.id}/download'},'*');
        document.querySelector('a').click();
        var denied=0;[function(){return document.cookie},function(){return localStorage.length},function(){return parent.document.body}].forEach(function(read){try{read()}catch(e){denied++}});
        document.getElementById('probe').textContent='Sandbox denials: '+denied;
      </script></body></html>`), issue.id);
    expect((await owner.reportFeedbackResponse(report.id, "schema", "PUT", { expected_revision: 0, fields: [{ id: "note", label: "Review note", type: "text", required: false, options: [] }] })).status).toBe(200);
    const rowId = randomUUID();
    expect((await owner.reportFeedbackResponse(report.id, `rows/${rowId}`, "PUT", { expected_revision: 0, schema_revision: 1, label: "Delivery item", source_attachment_id: "", values: { note: "Saved baseline" } })).status).toBe(200);
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("i18nextLng", "en");
      localStorage.setItem("multica:chat:isOpen", "false");
    }, reader.getToken());
    let downloads = 0;
    const targetRequests: string[] = [];
    page.on("download", () => downloads++);
    page.on("request", (request) => {
      if (request.url().includes(publicFile.id) || request.url().includes(privateFile.id)) targetRequests.push(new URL(request.url()).pathname);
    });
    await page.goto(`/${ws.slug}/attachments/${report.id}/preview`);
    const frame = page.frameLocator('iframe[title="action-boundaries.html"]');
    await expect(frame.getByText("Sandbox denials: 3")).toBeVisible();
    await expect(page.locator('iframe[title="action-boundaries.html"]')).toHaveAttribute("sandbox", "allow-scripts");
    expect(targetRequests).toHaveLength(0);
    await frame.getByRole("button", { name: "Try unsupported actions" }).click();
    // A following UI event drains the posted messages without fabricating activation.
    await page.getByRole("button", { name: "Report feedback", exact: true }).click();
    expect(targetRequests).toHaveLength(0);
    expect(downloads).toBe(0);
    await expect(page.getByRole("button", { name: "Report feedback", exact: true })).toHaveAttribute("aria-expanded", "false");
    await expect.poll(async () => page.getByRole("region", { name: "Report feedback", exact: true }).count(), { timeout: 6000 }).toBe(0);
    await page.getByRole("button", { name: "Report feedback", exact: true }).click();

    const rejectedPreview = page.waitForEvent("popup");
    await frame.getByRole("link", { name: "Preview restricted original" }).click();
    const deniedTab = await rejectedPreview;
    await expect.poll(() => deniedTab.isClosed()).toBe(true);
    await expect(page.getByText("Couldn't load preview", { exact: true })).toBeVisible();
    const rejectedDownload = page.waitForResponse((response) => response.url().includes(`/api/attachments/${privateFile.id}/download-metadata`));
    await frame.getByRole("link", { name: "Download restricted original" }).click();
    expect((await rejectedDownload).status()).toBe(404);
    await expect(page.getByText("Couldn't fetch a download link. Try again in a moment.")).toBeVisible();
    expect(downloads).toBe(0);
    await page.screenshot({ path: info.outputPath("restricted-original-denied.png") });

    // Transport fault injection against the real application, not a mocked page.
    const downloadPath = `**/api/attachments/${publicFile.id}/download-metadata`;
    await page.route(downloadPath, (route) => route.abort("internetdisconnected"));
    await frame.getByRole("link", { name: "Download allowed original" }).click();
    await expect(page.getByText("Couldn't fetch a download link. Try again in a moment.").first()).toBeVisible();
    await page.unroute(downloadPath);
    const recoveredDownload = page.waitForEvent("download");
    await frame.getByRole("link", { name: "Download allowed original" }).click();
    expect((await recoveredDownload).suggestedFilename()).toBe("delivery-note.txt");

    await page.getByRole("button", { name: "Edit record", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "Edit record", exact: true });
    await editor.getByRole("textbox", { name: "Review note", exact: true }).fill("Retry must preserve this review");
    const savePath = `**/report-feedback/rows/${rowId}`;
    await page.route(savePath, (route) => route.abort("internetdisconnected"));
    await editor.getByRole("button", { name: "Save", exact: true }).click();
    await expect(editor.getByRole("alert")).toContainText("Could not save");
    await expect(editor.getByRole("textbox", { name: "Review note", exact: true })).toHaveValue("Retry must preserve this review");
    await page.screenshot({ path: info.outputPath("save-failure-preserved-input.png") });
    await page.unroute(savePath);
    await editor.getByRole("button", { name: "Save", exact: true }).click();
    await expect(editor).not.toBeVisible();
    await page.reload();
    await expect(page.getByRole("cell", { name: "Retry must preserve this review", exact: true })).toBeVisible();
    const persisted = await (await reader.reportFeedbackResponse(report.id)).json();
    expect(persisted.rows[0]).toMatchObject({ id: rowId, values: { note: "Retry must preserve this review" } });
    await page.screenshot({ path: info.outputPath("save-retry-reopened.png") });

    // A failed background read is recoverable and must not discard an open draft.
    const feedbackPath = `**/api/attachments/${report.id}/report-feedback?*`;
    await page.route(feedbackPath, (route) => route.abort("internetdisconnected"));
    await expect(page.getByRole("alert").filter({ hasText: "Updates could not be refreshed" })).toBeVisible({ timeout: 10_000 });
    await page.screenshot({ path: info.outputPath("feedback-refresh-retry.png") });
    await page.unroute(feedbackPath);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect(page.getByRole("alert").filter({ hasText: "Updates could not be refreshed" })).toHaveCount(0);
    await page.getByRole("button", { name: "Edit record", exact: true }).click();
    await editor.getByRole("textbox", { name: "Review note", exact: true }).fill("Unsubmitted collaboration draft");
    await page.route(feedbackPath, (route) => route.abort("internetdisconnected"));
    await page.waitForRequest((request) => request.url().includes(`${report.id}/report-feedback?`));
    await expect(editor.getByRole("textbox", { name: "Review note", exact: true })).toHaveValue("Unsubmitted collaboration draft");
    await page.unroute(feedbackPath);
    const otherRowId = randomUUID();
    expect((await owner.reportFeedbackResponse(report.id, `rows/${otherRowId}`, "PUT", { expected_revision: 0, schema_revision: 1, label: "Collaborator follow-up", source_attachment_id: "", values: { note: "Another row updated" } })).status).toBe(200);
    const refreshed = await page.waitForResponse((response) => response.ok() && response.url().includes(`${report.id}/report-feedback?`));
    expect((await refreshed.json()).rows).toEqual(expect.arrayContaining([expect.objectContaining({ id: otherRowId })]));
    await expect(editor.getByRole("textbox", { name: "Review note", exact: true })).toHaveValue("Unsubmitted collaboration draft");
    await page.screenshot({ path: info.outputPath("feedback-refresh-preserved-draft.png") });
    await editor.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.getByRole("alertdialog").getByRole("button", { name: "Discard", exact: true }).click();
    await expect(page.getByRole("cell", { name: "Another row updated", exact: true })).toBeVisible();

    // Group membership is an additional boundary, even while workspace access remains.
    const readerId = await reader.currentUserId();
    const sharedGroup = await owner.createGroup("Revocable delivery team", [readerId]);
    const topic = (await owner.groupTopics(sharedGroup.id))[0] ?? await owner.createGroupTopic(sharedGroup.id, "");
    const sharedReport = await owner.uploadTopicAttachment(topic.id, "group-delivery.html", Buffer.from("<!doctype html><h1>Private team delivery report</h1>"));
    await owner.sendGroupMessage(topic.id, "Team delivery report", [sharedReport.id]);
    expect((await owner.reportFeedbackResponse(sharedReport.id, "schema", "PUT", { expected_revision: 0, fields: [{ id: "note", label: "Team note", type: "text", required: false, options: [] }] })).status).toBe(200);
    const sharedRowId = randomUUID();
    expect((await owner.reportFeedbackResponse(sharedReport.id, `rows/${sharedRowId}`, "PUT", { expected_revision: 0, schema_revision: 1, label: "Private team item", source_attachment_id: "", values: { note: "Private team baseline" } })).status).toBe(200);
    await page.goto(`/${ws.slug}/attachments/${sharedReport.id}/preview`);
    await expect(page.getByRole("cell", { name: "Private team baseline", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Edit record", exact: true }).click();
    await owner.removeGroupParticipant(sharedGroup.id, readerId);
    await expect(page.getByRole("dialog", { name: "Edit record", exact: true })).toHaveCount(0, { timeout: 15_000 });
    await expect(page.getByRole("cell", { name: "Private team baseline", exact: true })).toHaveCount(0);
    await expect(page.locator('iframe[title="group-delivery.html"]')).toHaveCount(0);
    for (const action of ["", "history"]) expect((await reader.reportFeedbackResponse(sharedReport.id, action)).status).toBe(404);
    expect((await reader.reportFeedbackResponse(sharedReport.id, `rows/${sharedRowId}`, "PUT", { expected_revision: 1, schema_revision: 1, label: "Private team item", source_attachment_id: "", values: { note: "Must not save" } })).status).toBe(404);
    expect((await reader.attachmentResponse(sharedReport.id, "preview-location")).status).toBe(404);
    expect((await reader.attachmentResponse(sharedReport.id, "download")).status).toBe(404);
    expect((await reader.reportFeedbackResponse(report.id)).status).toBe(200);
    await page.screenshot({ path: info.outputPath("group-report-access-revoked.png") });
  } catch (error) {
    await page.screenshot({ path: info.outputPath("failure.png") }).catch(() => {});
    throw error;
  } finally {
    await owner.cleanup();
    await owner.deleteWorkspace();
  }
});
