// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { ChatSession } from "../types";
import type { Conversation } from "../types/chat-v2";
import { mergeChatThreads } from "./thread-list";

const session = (id: string, updated_at: string, extra: Partial<ChatSession> = {}): ChatSession => ({
  id, workspace_id: "ws", agent_id: "a", creator_id: "u", title: id,
  status: "active", pinned: false, has_unread: false, unread_count: 0,
  last_message: null, created_at: updated_at, updated_at, ...extra,
});
const group = (id: string, updated_at: string, extra: Partial<Conversation> = {}): Conversation => ({
  id, workspace_id: "ws", kind: "group", title: id, participants: [], created_at: updated_at, updated_at, ...extra,
});
describe("unified thread ordering", () => {
  it("keeps pinned direct chats first and interleaves both kinds by activity", () => {
    const directs = [session("older", "2026-01-01"), session("pinned", "2025-01-01", { pinned: true }), session("new", "2026-03-01")];
    const groups = [group("between", "2026-02-01"), group("newest", "2026-04-01")];
    expect(mergeChatThreads(directs, groups).map((row) => row.kind === "direct" ? row.session.id : row.conversation.id)).toEqual(["pinned", "newest", "new", "between", "older"]);
    expect(directs[0]?.id).toBe("older");
  });
  it("excludes archived rows and v2 DMs without merging identities or inventing group state", () => {
    const rows = mergeChatThreads([session("same", ""), session("archived", "", { status: "archived" })], [group("same", ""), group("old", "", { archived_at: "date" }), group("dm", "", { kind: "dm" })]);
    expect(rows.map((row) => row.kind)).toEqual(["direct", "group"]);
    expect(rows[1]).toEqual({ kind: "group", conversation: group("same", "") });
  });
});
