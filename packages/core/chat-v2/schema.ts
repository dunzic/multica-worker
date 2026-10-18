import { z } from "zod";
import { AttachmentResponseSchema } from "../api/schemas";

export const PrincipalSchema = z.object({ type: z.string(), id: z.string() });
export const ParticipantSchema = z.object({
  principal_type: z.string(), principal_id: z.string(), role: z.string().default("member"),
  joined_at: z.string().default(""), left_at: z.string().nullable().optional(),
});
export const ConversationSchema = z.object({
  id: z.string().min(1), workspace_id: z.string(), kind: z.string(),
  title: z.string().nullable().optional(), participants: z.array(ParticipantSchema).default([]),
  archived_at: z.string().nullable().optional(), created_at: z.string().default(""), updated_at: z.string().default(""),
  last_message: z.object({ content: z.string(), created_at: z.string() }).nullish().catch(null),
});
export const TopicSchema = z.object({
  id: z.string().min(1), conversation_id: z.string(), title: z.string().nullable().optional(),
  created_by_type: z.string().default("member"), created_by_id: z.string().default(""),
  archived_at: z.string().nullable().optional(), last_message_at: z.string().default(""), created_at: z.string().default(""),
});
export const MessageSchema = z.object({
  id: z.string().min(1), topic_id: z.string(), sender_type: z.string(), sender_id: z.string(),
  content: z.string().default(""), task_id: z.string().nullable().optional(), created_at: z.string().default(""),
  attachments: z.array(AttachmentResponseSchema).nullish().transform((v) => v ?? []),
});
export const SendMessageSchema = z.object({
  message: MessageSchema, task_id: z.string().nullable().optional(), task_ids: z.array(z.string()).default([]),
});
export const ConversationListSchema = z.array(ConversationSchema);
export const TopicListSchema = z.array(TopicSchema);
export const MessageListSchema = z.array(MessageSchema);
export const TopicRunSchema = z.object({
  id: z.string(), agent_id: z.string(), status: z.string(),
  error: z.string().nullable().default(null), message_id: z.string().default(""), created_at: z.string().default(""),
});
export const TopicRunListSchema = z.array(TopicRunSchema);
export type TopicRun = z.infer<typeof TopicRunSchema>;
