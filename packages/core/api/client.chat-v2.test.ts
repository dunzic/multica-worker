// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
function response(body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } })));
  return new ApiClient("https://example.test");
}
describe("chat v2 API response boundary", () => {
  it("pins conversation reads and creates to the initiating workspace", async () => {
    const client = response([]);
    await client.listConversationsV2("origin-workspace");
    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.headers).toEqual(expect.objectContaining({ "X-Workspace-ID": "origin-workspace", "X-Workspace-Slug": "" }));
    const creator = response({ id: "group", workspace_id: "origin-workspace", kind: "group" });
    await creator.createConversationV2("origin-workspace", { kind: "group", title: "Delivery", participants: [] });
    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.headers).toEqual(expect.objectContaining({ "X-Workspace-ID": "origin-workspace", "X-Workspace-Slug": "" }));
  });
  it.each(["conversations", "topics", "messages", "runs"] as const)("falls back for malformed %s reads", async (kind) => {
    const client = response({ unexpected: true });
    const value = kind === "conversations" ? await client.listConversationsV2("ws") : kind === "topics" ? await client.listTopicsV2("c") : kind === "runs" ? await client.listTopicRunsV2("t") : await client.listTopicMessagesV2("t");
    expect(value).toEqual([]);
  });
  it("rejects malformed create without returning an empty destination", async () => {
    await expect(response({ id: 7 }).createConversationV2("ws", { kind: "group", title: "HR", participants: [] })).rejects.toThrow("Invalid conversation response");
  });
  it("rejects malformed topic and message mutations", async () => {
    const client = response(null);
    await expect(client.createTopicV2("c", "JD")).rejects.toThrow("Invalid topic response");
    await expect(client.sendTopicMessageV2("t", "screen", ["one", "two"])).rejects.toThrow("Invalid message response");
    await expect(client.updateConversationV2("c", "HR")).rejects.toThrow("Invalid conversation response");
    await expect(client.addConversationParticipantV2("c", { type: "agent", id: "a" })).rejects.toThrow("Invalid participant response");
  });
  it("sends every attachment id and parses multiple dispatched runs", async () => {
    const client = response({ message: { id: "m", topic_id: "t", sender_type: "member", sender_id: "u" }, task_ids: ["a", "b"] });
    expect((await client.sendTopicMessageV2("t", "screen", ["one", "two", "three"])).task_ids).toEqual(["a", "b"]);
    const fetchMock = vi.mocked(fetch);
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)).attachment_ids).toEqual(["one", "two", "three"]);
  });
});
