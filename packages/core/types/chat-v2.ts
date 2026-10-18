// Chat v2 wire types — mirror the Go handler structs in
// server/internal/handler/chat_v2.go. Field names are snake_case to match
// the server's JSON tags; renaming on the TS side would break the type
// contract since we don't run a code generator.
//
// Shared workspace collaboration contract.

import type { Attachment } from "./attachment";
export type PrincipalType = "member" | "agent";

export interface Principal {
  type: PrincipalType;
  id: string;
}

export interface Participant {
  principal_type: PrincipalType;
  principal_id: string;
  role: string;
  joined_at: string;
  left_at?: string | null;
}

export type ConversationKind = "dm" | "group";

export interface Conversation {
  id: string;
  workspace_id: string;
  kind: ConversationKind;
  /** null for DMs (derived from participants client-side), set for groups. */
  title?: string | null;
  participants: Participant[];
  archived_at?: string | null;
  created_at: string;
  updated_at: string;
  last_message?: { content: string; created_at: string } | null;
}

export interface Topic {
  id: string;
  conversation_id: string;
  title?: string | null;
  created_by_type: PrincipalType;
  created_by_id: string;
  archived_at?: string | null;
  last_message_at: string;
  created_at: string;
}

export interface MessageV2 {
  id: string;
  topic_id: string;
  sender_type: PrincipalType;
  sender_id: string;
  content: string;
  task_id?: string | null;
  created_at: string;
  /** File / image attachments linked to this message. Server bulk-loads
   *  these in ListTopicMessages so the chat surface can render them
   *  without a per-message N+1 fetch. Optional / nullable to keep
   *  cached MessageV2 rows from before this field landed compatible. */
  attachments?: Attachment[];
}

export interface SendMessageV2Response {
  task_ids?: string[];
  message: MessageV2;
  /** Set when the message dispatched to an agent (dm-with-agent or @mention). */
  task_id?: string | null;
}

// ─── WS event payload shapes (mirror pkg/protocol/messages.go) ──────────

export interface ConversationEventPayload {
  conversation_id: string;
  workspace_id: string;
  kind: ConversationKind;
  title?: string | null;
  archived_at?: string | null;
  updated_at: string;
  recipient_user_ids: string[];
}

export interface ParticipantChangePayload {
  conversation_id: string;
  principal_type: PrincipalType;
  principal_id: string;
  role?: string;
  recipient_user_ids: string[];
}

export interface TopicChangePayload {
  conversation_id: string;
  topic_id: string;
  title?: string | null;
  archived_at?: string | null;
  last_message_at?: string;
  recipient_user_ids: string[];
}

export interface MessageSentEventPayload {
  conversation_id: string;
  topic_id: string;
  message_id: string;
  sender_type: PrincipalType;
  sender_id: string;
  content: string;
  task_id?: string | null;
  created_at: string;
  recipient_user_ids: string[];
}

export interface MessageDoneEventPayload {
  conversation_id: string;
  topic_id: string;
  message_id: string;
  task_id: string;
  agent_id: string;
  content: string;
  created_at: string;
  recipient_user_ids: string[];
}

export interface ConversationReadEventPayload {
  conversation_id: string;
  user_id: string;
  last_read_topic_id?: string | null;
  recipient_user_ids: string[];
}
