/**
 * Explicit real-provider acceptance. Run only through the agentintegration Go
 * test, which starts/stops this checkout's isolated managed daemon.
 * All resumes are fictional; no real recruiting decisions are made.
 */
import "./env";
import { test, expect } from "@playwright/test";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import pg from "pg";
import { TestApiClient } from "./fixtures";

test.use({ channel: "chrome", viewport: { width: 1440, height: 1000 }, actionTimeout: 30_000 });
test.skip(process.env.MULTICA_RUN_REAL_AGENT_SMOKE !== "1" || process.env.MULTICA_HR_ACCEPTANCE !== "1", "Explicit real-agent acceptance only");

test("HR uploads three resumes, previews originals, receives linked feedback and continues the topic", async ({ page }) => {
  test.setTimeout(840_000);
  const base = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
  expect(["localhost", "127.0.0.1"]).toContain(new URL(base).hostname);
  const database = new URL(process.env.DATABASE_URL!);
  expect(["localhost", "127.0.0.1"]).toContain(database.hostname);
  expect(database.pathname).toMatch(/^\/multica_.+/);
  const provider = process.env.MULTICA_HR_PROVIDER || "codex";
  expect(["codex", "claude"]).toContain(provider);
  const api = new TestApiClient();
  await api.login("dev@localhost", "HR Acceptance");
  const ws = await api.ensureWorkspace("HR synthetic acceptance", "hr-acceptance");
  expect(ws.slug).toBe("hr-acceptance");
  await api.markUserOnboarded();
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  const evidence = resolve(".context/hr-uat/acceptance", new Date().toISOString().replace(/[:.]/g, "-"));
  await mkdir(evidence, { recursive: true });
  console.log(`HR evidence: ${evidence}`);
  const request = async (path: string, method = "GET", body?: unknown) => {
    const res = await fetch(base + path, { method, headers: {
      Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": ws.id, "Content-Type": "application/json",
    }, body: body === undefined ? undefined : JSON.stringify(body) });
    expect(res.ok, `${method} ${path}: ${res.status} ${res.ok ? "" : await res.text()}`).toBeTruthy();
    return res.status === 204 ? null : res.json();
  };
  let agentId: string | undefined;
  let topicId: string | undefined;
  try {
    let runtimes: Array<{ id: string; provider: string; status: string }> = [];
    await expect.poll(async () => {
      runtimes = await request("/api/runtimes");
      return runtimes.some((r) => r.provider === provider && r.status === "online");
    }, { timeout: 60_000 }).toBe(true);
    const runtime = runtimes.find((r) => r.provider === provider && r.status === "online")!;
    console.log(`HR acceptance provider: ${runtime.provider}`);
    const agent = await request("/api/agents", "POST", {
      name: "HR synthetic reviewer", runtime_id: runtime.id, runtime_config: {},
      visibility: "private", max_concurrent_tasks: 1,
      instructions: "Use only these fictional resumes and the supplied job criteria. Report evidence and missing information, not final hiring decisions. Use multica CLI only in the current workspace. Create unassigned backlog issues so no other agent is dispatched. Do not access external services or repositories. Keep output concise.",
    });
    agentId = agent.id;
    const conv = await request("/api/v2/conversations", "POST", { kind: "group", title: "HR batch acceptance", participants: [{ type: "agent", id: agent.id }] });
    const topics = await request(`/api/v2/conversations/${conv.id}/topics`);
    const topic = topics[0] ?? await request(`/api/v2/conversations/${conv.id}/topics`, "POST", { title: "Batch one" });
    topicId = topic.id;
    await page.addInitScript((token) => {
      localStorage.setItem("multica_token", token!);
      localStorage.setItem("multica:chat:isOpen", "false");
      localStorage.setItem("i18nextLng", "en");
    }, api.getToken());
    await page.goto(`/${ws.slug}/chat/groups?conversation=${conv.id}&topic=${topic.id}`);
    const editor = page.locator('[contenteditable="true"]').last();
    await expect(editor).toBeVisible({ timeout: 60_000 });
    await editor.fill("JD: QA Engineer. Required skills: API test design, SQL queries, and pytest or Playwright automation. Compare explicit evidence only; do not infer absent skills. These are fictional test resumes. Keep the final decision with HR. Use batch title HR-UAT and include original file links in each issue.\n");
    const uploaded: Array<{ id: string; filename: string }> = [];
    page.on("response", async (res) => {
      if (res.url().endsWith("/api/upload-file") && res.status() === 200) uploaded.push(await res.json());
    });
    const files = ["candidate-a.pdf", "candidate-b.docx", "candidate-c.pdf"];
    await page.locator('input[type="file"][multiple]').last().setInputFiles(files.map((file) => resolve("e2e/fixtures/hr-batch", file)));
    await expect.poll(() => uploaded.length, { timeout: 30_000 }).toBe(3);
    await expect(editor.locator(".file-card-node")).toHaveCount(3);
    await page.getByRole("button", { name: "Screen a batch of résumés", exact: true }).click();
    await page.getByRole("menuitem", { name: "HR synthetic reviewer", exact: true }).click();
    await expect(editor).toContainText("逐份读取所有附件");
    await expect(editor.locator(".file-card-node")).toHaveCount(3);
    await page.screenshot({ path: resolve(evidence, "01-batch-draft.png") });

    const word = editor.locator(".file-card-node").filter({ hasText: "candidate-b.docx" });
    await word.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(page.getByRole("dialog")).toContainText("Three years designing user interfaces", { timeout: 20_000 });
    await page.screenshot({ path: resolve(evidence, "02-docx-preview.png") });
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    const pdf = editor.locator(".file-card-node").filter({ hasText: "candidate-a.pdf" });
    await pdf.getByRole("button", { name: "Preview", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    const pdfFrame = page.locator('iframe[title="candidate-a.pdf"]');
    await expect(pdfFrame).toHaveAttribute("src", /^blob:/, { timeout: 20_000 });
    await expect.poll(async () => {
      const viewers = await Promise.all(page.frames().map((frame) => frame.locator("pdf-viewer").count()));
      return viewers.some((count) => count > 0);
    }, { timeout: 20_000 }).toBe(true);
    await page.screenshot({ path: resolve(evidence, "03-pdf-preview.png") });
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    for (const att of uploaded) {
      const meta = await request(`/api/attachments/${att.id}`);
      const downloaded = await fetch(base + `/api/attachments/${att.id}/download`, { headers: { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": ws.id } });
      expect(downloaded.status).toBe(200);
      expect(Buffer.from(await downloaded.arrayBuffer())).toEqual(await readFile(resolve("e2e/fixtures/hr-batch", att.filename)));
      expect(meta.id).toBe(att.id);
    }
    const sent = page.waitForResponse((res) => res.request().method() === "POST" && res.url().endsWith(`/api/v2/topics/${topic.id}/messages`));
    await page.getByRole("button", { name: "Send", exact: true }).click();
    const sentBody = await (await sent).json();
    expect(sentBody.task_ids).toHaveLength(1);
    const firstTask = sentBody.task_ids[0];
    const waitForRun = async (id: string, timeout = 600_000) => {
      await expect.poll(async () => {
        const runs = await request(`/api/v2/topics/${topic.id}/runs`);
        const run = runs.find((run: { id: string }) => run.id === id);
        if (run?.status === "failed" || run?.status === "cancelled") throw new Error(`HR run ${run.status}: ${run.error}`);
        return run?.status;
      }, { timeout, intervals: [1000, 3000, 5000] }).toBe("completed");
    };
    await waitForRun(firstTask);
    const messages = await request(`/api/v2/topics/${topic.id}/messages`);
    const reply = messages.find((m: { sender_type: string; task_id?: string }) => m.sender_type === "agent" && m.task_id === firstTask);
    expect(reply).toBeTruthy();
    for (const filename of files) expect(reply.content).toContain(filename);
    const issues = await db.query("SELECT id, title, description, parent_issue_id FROM issue WHERE workspace_id=$1 ORDER BY created_at", [ws.id]);
    expect(issues.rows).toHaveLength(4);
    const children = issues.rows.filter((i) => i.parent_issue_id);
    expect(children).toHaveLength(3);
    expect(new Set(children.map((i) => i.parent_issue_id)).size).toBe(1);
    for (const att of uploaded) {
      expect(children.some((i) => i.description?.includes(att.id))).toBe(true);
      expect(reply.content).toContain(att.id);
    }
    for (const issue of issues.rows) {
      await request(`/api/issues/${issue.id}`);
      expect(reply.content.includes(issue.id) || reply.content.includes(issue.title)).toBe(true);
    }
    const prior = await db.query("SELECT session_id,work_dir FROM agent_task_queue WHERE id=$1", [firstTask]);
    expect(prior.rows[0].session_id).toBeTruthy();
    expect(prior.rows[0].work_dir).toBeTruthy();
    const feedback = page.locator("main article").filter({ hasText: "HR synthetic reviewer" }).last();
    for (const filename of files) await expect(feedback).toContainText(filename, { timeout: 30_000 });
    await feedback.scrollIntoViewIfNeeded();
    await page.screenshot({ path: resolve(evidence, "04-feedback-and-links.png") });
    console.log("HR first-turn feedback and original-file references verified in the page");

    await editor.fill("");
    await editor.pressSequentially("@HR synthetic");
    await page.getByRole("button", { name: /HR synthetic reviewer\s+Agent$/ }).click();
    await editor.press("End");
    await editor.pressSequentially(" Continue the previous batch. Which candidate explicitly used pytest and how many checks were stated? Do not create more issues. Reply with the original file link.");
    const continued = page.waitForResponse((res) => res.request().method() === "POST" && res.url().endsWith(`/api/v2/topics/${topic.id}/messages`));
    await page.getByRole("button", { name: "Send", exact: true }).click();
    const next = await (await continued).json();
    expect(next.task_ids).toHaveLength(1);
    await waitForRun(next.task_ids[0], 180_000);
    const follow = await db.query("SELECT session_id,work_dir FROM agent_task_queue WHERE id=$1", [next.task_ids[0]]);
    expect(follow.rows[0]).toEqual(prior.rows[0]);
    const final = await request(`/api/v2/topics/${topic.id}/messages`);
    const continuation = final.find((m: { sender_type: string; task_id?: string }) => m.sender_type === "agent" && m.task_id === next.task_ids[0]);
    expect(continuation).toBeTruthy();
    expect(continuation.content).toContain("120");
    expect(continuation.content).toContain(uploaded.find((a) => a.filename === "candidate-a.pdf")!.id);
    const afterFollowUp = await db.query("SELECT id FROM issue WHERE workspace_id=$1 ORDER BY id", [ws.id]);
    expect(afterFollowUp.rows.map((i) => i.id)).toEqual(issues.rows.map((i) => i.id).sort());
    await expect(page.locator("main article")).toHaveCount(final.length, { timeout: 30_000 });
    await expect(feedback).toContainText("120", { timeout: 30_000 });
    await expect(feedback).toContainText("candidate-a.pdf");
    await page.reload();
    await expect(feedback).toContainText("120");
    await expect(feedback).toContainText("candidate-a.pdf");
    await feedback.scrollIntoViewIfNeeded();
    await page.screenshot({ path: resolve(evidence, "05-follow-up.png") });
    await writeFile(resolve(evidence, "result.json"), JSON.stringify({
      provider: runtime.provider, files: uploaded, messages: final, issues: issues.rows,
      sessionReused: follow.rows[0].session_id === prior.rows[0].session_id,
      workdirReused: follow.rows[0].work_dir === prior.rows[0].work_dir,
      followUpSentFromUI: true, noDuplicateIssues: true,
    }, null, 2));
  } catch (error) {
    await writeFile(resolve(evidence, "failure.txt"), error instanceof Error ? error.stack ?? error.message : String(error));
    await writeFile(resolve(evidence, "failure-page.txt"), await page.locator("body").ariaSnapshot()).catch(() => {});
    throw error;
  } finally {
    await page.screenshot({ path: resolve(evidence, "last-page.png") }).catch(() => {});
    if (topicId) {
      const runs = await request(`/api/v2/topics/${topicId}/runs`).catch(() => []);
      const messages = await request(`/api/v2/topics/${topicId}/messages`).catch(() => []);
      const issues = await db.query("SELECT id,title,description,parent_issue_id FROM issue WHERE workspace_id=$1 ORDER BY created_at", [ws.id]);
      await writeFile(resolve(evidence, "runs-and-messages.json"), JSON.stringify({ runs, messages, issues: issues.rows }, null, 2));
      for (const run of runs) {
        if (!["completed", "failed", "cancelled"].includes(run.status)) {
          await request(`/api/tasks/${run.id}/cancel`, "POST").catch(() => {});
        }
      }
    }
    await api.cleanup();
    // This dedicated workspace is created solely by the managed acceptance.
    // Its deletion cleans group files/messages/runs through application code.
    if (agentId) await request(`/api/agents/${agentId}`, "DELETE").catch(() => {});
    await api.deleteWorkspace();
    const remaining = await db.query("SELECT id FROM workspace WHERE id=$1", [ws.id]);
    expect(remaining.rows).toHaveLength(0);
    await db.end();
  }
});
