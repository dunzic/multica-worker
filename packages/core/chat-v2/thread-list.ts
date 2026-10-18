import type { ChatSession } from "../types";
import type { Conversation } from "../types/chat-v2";

export type ChatThread = { kind: "direct"; session: ChatSession } | { kind: "group"; conversation: Conversation };

/** Merge projections, not ownership or message models. Group unread/pin state is not invented. */
export function mergeChatThreads(sessions: ChatSession[], groups: Conversation[]): ChatThread[] {
  const rows: ChatThread[] = [
    ...sessions.filter((session) => session.status !== "archived").map((session): ChatThread => ({ kind: "direct", session })),
    ...groups.filter((conversation) => conversation.kind === "group" && !conversation.archived_at).map((conversation): ChatThread => ({ kind: "group", conversation })),
  ];
  const time = (row: ChatThread) => {
    if (row.kind === "group") return Math.max(Date.parse(row.conversation.updated_at) || 0, Date.parse(row.conversation.last_message?.created_at ?? "") || 0);
    return Date.parse(row.session.last_message?.created_at ?? row.session.updated_at) || 0;
  };
  const pinned = (row: ChatThread) => row.kind === "direct" && row.session.pinned ? 1 : 0;
  return rows.sort((a, b) => pinned(b) - pinned(a) || time(b) - time(a));
}
