/**
 * HTTP contract: upload binds a chat attachment; send back-fills its message.
 * Uses an inert runtime, not a real agent. UI wiring lives in chat-input tests.
 */
import "./env";
import { test, expect } from "@playwright/test";
import pg from "pg";
import { TestApiClient } from "./fixtures";

const DATABASE_URL =
  process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

test.describe("Chat attachments", () => {
  let api: TestApiClient;

  test.beforeEach(async () => {
    api = new TestApiClient();
    const run = `${Date.now()}-${process.pid}`;
    await api.login(`chat-attachments-${run}@localhost`, "Chat attachment test");
    await api.ensureWorkspace("Chat attachment test", `chat-attachments-${run}`);
  });

  test.afterEach(async () => {
    // The real deletion path cleans tasks and other non-FK dependents too.
    await api.deleteWorkspace();
  });

  test("upload-file binds attachment to the chat_session; send back-fills chat_message_id", async () => {
    const session = await api.createChatSessionFixture();
    const pngBytes = new Uint8Array([
      0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
      0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
    ]);
    const uploaded = await api.uploadChatAttachment(session.id, "e2e.png", pngBytes);
    expect(uploaded.chat_session_id).toBe(session.id);
    expect(uploaded.chat_message_id).toBeNull();
    expect(uploaded.url).toBeTruthy();

    const sendRes = await api.sendChatMessage(session.id, `look at this ![](${uploaded.url})`, [uploaded.id]);
    expect(sendRes.status, await sendRes.clone().text()).toBe(201);
    const sendBody = (await sendRes.json()) as { message_id: string; task_id: string };
    expect(sendBody.message_id).toBeTruthy();

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const after = await client.query<{ chat_message_id: string | null }>(
        `SELECT chat_message_id::text FROM attachment WHERE id = $1`,
        [uploaded.id],
      );
      expect(after.rows[0]?.chat_message_id).toBe(sendBody.message_id);
    } finally {
      await client.end();
    }
  });
});
