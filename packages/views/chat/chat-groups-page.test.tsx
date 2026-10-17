import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import i18next from "i18next";
import { initReactI18next } from "react-i18next";
import enChat from "../locales/en/chat.json";
import { ChatGroupsPage } from "./chat-groups-page";

const mocks = vi.hoisted(() => ({
  commit: vi.fn(),
  send: vi.fn(),
  list: vi.fn(),
  replace: vi.fn(),
  inputProps: null as null | Record<string, unknown>,
  attachmentIds: ["one", "two", "three"],
  query: "conversation=group&topic=topic",
  topics: vi.fn(),
  messages: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: {
  listConversationsV2: () => mocks.list(),
  listTopicsV2: () => mocks.topics(),
  listTopicMessagesV2: (...args: unknown[]) => mocks.messages(...args),
  listTopicRunsV2: async () => [],
  createConversationV2: vi.fn(),
  createTopicV2: vi.fn(),
  sendTopicMessageV2: (...args: unknown[]) => mocks.send(...args),
} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign((select: (s: { user: { id: string } }) => unknown) => select({ user: { id: "user" } }), { getState: () => ({ user: { id: "user" } }) }),
}));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ chat: () => "/test/chat", chatGroups: () => "/test/chat/groups" }) }));
vi.mock("@multica/core/workspace/hooks", () => ({ useActorName: () => ({ getActorName: (_type: string, id: string) => id }) }));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: async () => [] }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: async () => [] }),
}));
vi.mock("../navigation", () => ({
  useNavigation: () => ({ searchParams: new URLSearchParams(mocks.query), replace: mocks.replace }),
  AppLink: ({ children, ...props }: React.ComponentProps<"a">) => <a {...props}>{children}</a>,
}));
vi.mock("../layout/page-header", () => ({ PageHeader: ({ children }: { children: React.ReactNode }) => <header>{children}</header> }));
vi.mock("../editor", () => ({ ReadonlyContent: () => null }));
vi.mock("../editor/attachment", () => ({ Attachment: () => null }));
vi.mock("./components/chat-input", () => ({ ChatInput: (props: Record<string, unknown>) => {
  mocks.inputProps = props;
  const onSend = props.onSend as (content: string, ids: string[], commit: () => void) => Promise<boolean>;
  return <button onClick={() => onSend("screen", mocks.attachmentIds, mocks.commit)}>Send batch</button>;
} }));

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const i18n = i18next.createInstance();
  i18n.use(initReactI18next).init({ lng: "en", resources: { en: { chat: enChat } }, initAsync: false });
  return render(<I18nextProvider i18n={i18n}><QueryClientProvider client={client}><ChatGroupsPage /></QueryClientProvider></I18nextProvider>);
}
beforeEach(() => {
  vi.clearAllMocks();
  mocks.inputProps = null;
  mocks.attachmentIds = ["one", "two", "three"];
  mocks.query = "conversation=group&topic=topic";
  mocks.topics.mockResolvedValue([{ id: "topic", conversation_id: "group", title: "HR batch" }]);
  mocks.messages.mockResolvedValue([]);
  mocks.list.mockResolvedValue([{ id: "group", workspace_id: "ws", kind: "group", title: "HR", participants: [{ principal_type: "member", principal_id: "user", role: "admin" }, { principal_type: "agent", principal_id: "HR reviewer", role: "member" }] }]);
  mocks.send.mockResolvedValue({ message: { id: "m" }, task_ids: ["run"] });
});
describe("group chat wiring", () => {
  it("keeps the composer generic and directly in the height-constrained conversation", async () => {
    mount();
    const input = await screen.findByRole("button", { name: "Send batch" });
    expect(input.parentElement?.tagName).toBe("MAIN");
    expect(mocks.inputProps?.leftAdornment).toBeUndefined();
    expect(mocks.inputProps?.conversationStarterRequest).toBeUndefined();
    expect(screen.queryByText("Screen a batch of résumés")).not.toBeInTheDocument();
  });

  it("rejects a topic link from another group without reading, uploading or sending there", async () => {
    mocks.query = "conversation=group&topic=another-group-topic";
    mount();
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "Send batch" })).not.toBeInTheDocument();
    expect(mocks.messages).not.toHaveBeenCalled();
    expect(mocks.inputProps).toBeNull();
    expect(mocks.send).not.toHaveBeenCalled();
  });
  it("does not enable a composer before the topic membership lookup succeeds", async () => {
    mocks.topics.mockReturnValue(new Promise(() => {}));
    mount();
    await screen.findByRole("heading", { name: "HR" });
    expect(mocks.inputProps).toBeNull();
    expect(mocks.messages).not.toHaveBeenCalled();
  });
  it("pins the initial topic instead of following subsequent activity ordering", async () => {
    mocks.query = "conversation=group";
    mount();
    await screen.findByRole("button", { name: "Send batch" });
    expect(mocks.replace).toHaveBeenCalledWith("/test/chat/groups?conversation=group&topic=topic");
  });
  it("keeps oversized batches in the draft and explains how to split them", async () => {
    mocks.attachmentIds = Array.from({ length: 101 }, (_, i) => String(i));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Send batch" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("100 files per message");
    expect(mocks.send).not.toHaveBeenCalled();
    expect(mocks.commit).not.toHaveBeenCalled();
  });
  it.each([21, 100])("sends %i attachments as exactly one message", async (count) => {
    mocks.attachmentIds = Array.from({ length: count }, (_, i) => String(i));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Send batch" }));
    await waitFor(() => expect(mocks.commit).toHaveBeenCalledOnce());
    expect(mocks.send).toHaveBeenCalledExactlyOnceWith("topic", "screen", mocks.attachmentIds);
  });
  it("sends all batch ids and only clears the draft after success", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Send batch" }));
    await waitFor(() => expect(mocks.commit).toHaveBeenCalledOnce());
    expect(mocks.send).toHaveBeenCalledWith("topic", "screen", ["one", "two", "three"]);
    expect(mocks.inputProps?.uploadTopicId).toBe("topic");
    expect(mocks.inputProps?.draftKeyOverride).toBe("group:topic");
  });
  it("preserves the draft and exposes retry guidance when send fails", async () => {
    mocks.send.mockRejectedValue(new Error("offline"));
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Send batch" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not send");
    expect(mocks.commit).not.toHaveBeenCalled();
  });
});
