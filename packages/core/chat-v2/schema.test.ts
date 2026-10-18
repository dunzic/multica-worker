// @vitest-environment node
import { describe, it, expect } from "vitest";
import { parseWithFallback } from "../api/schema";
import { ConversationListSchema, MessageSchema, SendMessageSchema } from "./schema";

describe("chat collaboration response compatibility", () => {
  it("keeps valid summaries and discards a malformed optional summary without hiding the group", () => {
    const group = { id: "c", workspace_id: "ws", kind: "group" };
    const summary = { content: "Latest update", created_at: "2026-10-18T01:00:00Z" };
    expect(ConversationListSchema.parse([{ ...group, last_message: summary }])[0]?.last_message).toEqual(summary);
    expect(ConversationListSchema.parse([{ ...group, last_message: { content: 1 } }])[0]?.last_message).toBeNull();
  });
  it("defaults missing participants and tolerates a newer kind", () => {
    const value = ConversationListSchema.parse([{ id: "c", workspace_id: "ws", kind: "future" }]);
    expect(value[0]?.participants).toEqual([]);
    expect(value[0]?.kind).toBe("future");
  });
  it.each([null, {}, [{ workspace_id: "ws" }], [{ id: 42 }]])("falls back for malformed list %j", (raw) => {
    expect(parseWithFallback(raw, ConversationListSchema, [], { endpoint: "test" })).toEqual([]);
  });
  it("defaults omitted and null attachments in messages", () => {
    const message = { id: "m", topic_id: "t", sender_type: "agent", sender_id: "a" };
    expect(MessageSchema.parse(message).attachments).toEqual([]);
    expect(MessageSchema.parse({ ...message, attachments: null }).attachments).toEqual([]);
  });
  it("retains multiple run ids and malformed sends fail closed", () => {
    const data = { message: { id: "m", topic_id: "t", sender_type: "member", sender_id: "u" }, task_ids: ["run1", "run2"] };
    expect(SendMessageSchema.parse(data).task_ids).toEqual(["run1", "run2"]);
    expect(parseWithFallback({ message: null }, SendMessageSchema, null, { endpoint: "send" })).toBeNull();
  });
});
