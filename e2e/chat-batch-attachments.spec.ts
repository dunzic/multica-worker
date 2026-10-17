import "./env";
import { test, expect, type Request } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

test.use({
  channel: "chrome",
  viewport: { width: 1500, height: 1000 },
  trace: "retain-on-failure",
});

// Real browser, API and storage, synthetic files, no model or agent runtime.
for (const method of ["select", "paste", "drop"] as const) {
  test(`100 files in one group message via ${method}`, async ({
    page,
  }, info) => {
    test.setTimeout(180_000);
    const api = new TestApiClient();
    const stamp = `${Date.now()}-${method}`;
    await api.login(`batch-${stamp}@localhost`, "Batch upload tester");
    const ws = await api.ensureWorkspace(
      "Batch attachment acceptance",
      `batch-${stamp}`,
    );
    await api.markUserOnboarded();
    try {
      const { conversationId, topicId } = await api.createGroupChatFixture(
        "Generic batch files",
      );
      await page.addInitScript((token) => {
        localStorage.setItem("multica_token", token!);
        localStorage.setItem("i18nextLng", "en");
        localStorage.setItem("multica:chat:isOpen", "false");
      }, api.getToken());
      await page.goto(
        `/${ws.slug}/chat/groups?conversation=${conversationId}&topic=${topicId}`,
      );
      const editor = page.locator('[contenteditable="true"]').last();
      await expect(editor).toBeVisible({ timeout: 60_000 });
      await editor.fill("Review all 100 synthetic files as one batch.");
      const pending = new Set<Request>();
      const uploaded = new Set<string>();
      let peak = 0;
      let sends = 0;
      page.on("request", (request) => {
        if (request.url().endsWith("/api/upload-file")) {
          pending.add(request);
          peak = Math.max(peak, pending.size);
        }
        if (
          request.method() === "POST" &&
          request.url().endsWith(`/api/v2/topics/${topicId}/messages`)
        )
          sends++;
      });
      page.on("response", async (response) => {
        if (!response.url().endsWith("/api/upload-file")) return;
        pending.delete(response.request());
        if (response.status() === 200) uploaded.add((await response.json()).id);
      });
      page.on("requestfailed", (request) => pending.delete(request));
      const files = Array.from({ length: 100 }, (_, i) => ({
        name: `batch-${String(i).padStart(3, "0")}.txt`,
        content: `Synthetic batch evidence ${i}`,
      }));
      if (method === "select") {
        await page
          .locator('input[type="file"][multiple]')
          .last()
          .setInputFiles(
            files.map((f) => ({
              name: f.name,
              mimeType: "text/plain",
              buffer: Buffer.from(f.content),
            })),
          );
      } else {
        await editor.evaluate(
          (element, { method, files }) => {
            const transfer = new DataTransfer();
            for (const file of files)
              transfer.items.add(
                new File([file.content], file.name, { type: "text/plain" }),
              );
            const event =
              method === "paste"
                ? new ClipboardEvent("paste", {
                    clipboardData: transfer,
                    bubbles: true,
                    cancelable: true,
                  })
                : new DragEvent("drop", {
                    dataTransfer: transfer,
                    bubbles: true,
                    cancelable: true,
                  });
            element.dispatchEvent(event);
          },
          { method, files },
        );
      }
      await expect.poll(() => uploaded.size, { timeout: 90_000 }).toBe(100);
      await expect(editor.locator(".file-card-node")).toHaveCount(100);
      expect(peak).toBeLessThanOrEqual(3);
      expect(peak).toBeGreaterThan(0);
      if (method === "select") {
        await page
          .locator('input[type="file"][multiple]')
          .last()
          .setInputFiles({
            name: "extra.txt",
            mimeType: "text/plain",
            buffer: Buffer.from("rejected"),
          });
        await expect(
          page.getByText(/Couldn't upload extra.txt/),
        ).toBeVisible();
        await expect(editor.locator(".file-card-node")).toHaveCount(100);
      }
      await page.screenshot({ path: info.outputPath("batch-draft.png") });
      const sent = page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          r.url().endsWith(`/api/v2/topics/${topicId}/messages`),
      );
      await page.getByRole("button", { name: "Send", exact: true }).click();
      const response = await sent;
      expect(response.status(), await response.text()).toBe(201);
      const body = (await response.json()) as {
        message: { id: string };
        task_ids?: string[];
      };
      expect(body.task_ids ?? []).toHaveLength(0);
      expect(sends).toBe(1);
      const db = new pg.Client(process.env.DATABASE_URL);
      await db.connect();
      try {
        const rows = await db.query<{ id: string; filename: string }>(
          "SELECT id::text,filename FROM attachment WHERE workspace_id=$1 AND chat_message_id=$2",
          [ws.id, body.message.id],
        );
        expect(rows.rows.map((r) => r.id).sort()).toEqual([...uploaded].sort());
        expect(rows.rows.map((r) => r.filename).sort()).toEqual(
          files.map((f) => f.name).sort(),
        );
      } finally {
        await db.end();
      }
      await expect(editor.locator(".file-card-node")).toHaveCount(0);
      await page.reload();
      await expect(page.locator("main article").getByRole("button", { name: "Preview", exact: true })).toHaveCount(
        100,
        { timeout: 30_000 },
      );
      await page.screenshot({ path: info.outputPath("batch-sent.png") });
    } finally {
      await api.deleteWorkspace();
    }
  });
}
