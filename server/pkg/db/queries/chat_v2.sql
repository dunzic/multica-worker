-- Workspace collaboration queries. Existing single-chat handlers retain
-- their separate ownership and completion contract.
--
-- DM signature is computed by the handler (Go side) before passing in;
-- keeping the canonical sort here would duplicate logic and risk drift
-- with the backfill migration's signature. See chat_v2.go.

-- ═══════════════════════════════════════════════════════════════════
-- Conversation
-- ═══════════════════════════════════════════════════════════════════

-- name: CreateGroupConversation :one
INSERT INTO conversation (workspace_id, kind, title, created_by)
VALUES ($1, 'group', $2, $3)
RETURNING *;

-- name: CreateDMConversation :one
-- Idempotent-on-conflict insert for direct conversations. The unique
-- partial index (workspace_id, dm_signature WHERE active) handles the
-- race; on conflict we return the existing row by re-selecting.
-- Callers MUST use GetDMConversation first if they want the existing
-- conversation; this is for the "create if absent" path. The handler
-- enforces dedup via the surrounding transaction + GetDMConversation
-- → fall back to insert.
INSERT INTO conversation (workspace_id, kind, dm_signature, created_by)
VALUES ($1, 'dm', $2, $3)
RETURNING *;

-- name: GetDMConversation :one
-- Looks up an active DM conversation by canonical signature.
SELECT * FROM conversation
WHERE workspace_id = $1
  AND dm_signature = $2
  AND archived_at IS NULL;

-- name: GetConversation :one
SELECT * FROM conversation
WHERE id = $1 AND workspace_id = $2;

-- name: ListConversationsByMember :many
-- Sidebar driver: all conversations where (member, user_id) is an active
-- participant. Ordered by latest activity (conversation.updated_at, which
-- is bumped by the touch_topic_on_message trigger via topic → updated_at
-- propagation — see follow-up: also bump conversation.updated_at).
-- Filters: archived inclusion, kind.
SELECT c.*
FROM   conversation c
JOIN   conversation_participant p
       ON p.conversation_id = c.id
WHERE  c.workspace_id    = $1
  AND  p.principal_type  = 'member'
  AND  p.principal_id    = $2
  AND  p.left_at         IS NULL
  AND  (sqlc.narg('include_archived')::bool IS TRUE OR c.archived_at IS NULL)
  AND  (sqlc.narg('kind')::text IS NULL OR c.kind = sqlc.narg('kind')::text)
ORDER BY c.updated_at DESC;

-- name: UpdateConversationTitle :one
UPDATE conversation SET title = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ArchiveConversation :exec
UPDATE conversation SET archived_at = now(), updated_at = now()
WHERE id = $1;

-- name: UnarchiveConversation :exec
UPDATE conversation SET archived_at = NULL, updated_at = now()
WHERE id = $1;

-- name: DeleteConversation :exec
DELETE FROM conversation WHERE id = $1;

-- name: TouchConversation :exec
-- Called after a new message lands in any of the conversation's topics so
-- the sidebar's "latest activity" sort is stable. We can't put this on
-- the topic trigger because triggers can't cheaply update a parent row
-- without a recursive lookup; the handler calls this explicitly after
-- the message insert.
UPDATE conversation SET updated_at = now()
WHERE id = $1;

-- ═══════════════════════════════════════════════════════════════════
-- Participants
-- ═══════════════════════════════════════════════════════════════════

-- name: AddParticipant :one
INSERT INTO conversation_participant
    (conversation_id, principal_type, principal_id, role)
VALUES ($1, $2, $3, COALESCE(sqlc.narg('role')::text, 'member'))
ON CONFLICT (conversation_id, principal_type, principal_id) DO UPDATE
    SET left_at = NULL, role=EXCLUDED.role
RETURNING *;

-- name: LockConversation :one
SELECT * FROM conversation WHERE id=$1 FOR UPDATE;

-- name: CancelTopicParticipantRuns :many
UPDATE agent_task_queue q SET status='cancelled',completed_at=now(),cancelled_by_type='member',cancelled_by_id= @caller_id
WHERE q.topic_id IN(SELECT id FROM topic WHERE conversation_id= @conversation_id)
 AND q.status IN('queued','dispatched','running','waiting_local_directory')
 AND ((@principal_type::text='agent' AND q.agent_id= @principal_id)
   OR (@principal_type::text='member' AND q.initiator_user_id= @principal_id))
RETURNING q.*;

-- name: DeleteWorkspaceChatCollaborations :exec
WITH convs AS (SELECT c.id FROM conversation c WHERE c.workspace_id=$1),
 topics AS (SELECT t.id FROM topic t WHERE t.conversation_id IN(SELECT id FROM convs)),
 removed_tasks AS (DELETE FROM agent_task_queue q WHERE q.topic_id IN(SELECT id FROM topics)),
 removed_messages AS (DELETE FROM chat_message m WHERE m.topic_id IN(SELECT id FROM topics)),
 removed_participants AS (DELETE FROM conversation_participant p WHERE p.conversation_id IN(SELECT id FROM convs)),
 removed_topics AS (DELETE FROM topic t WHERE t.id IN(SELECT id FROM topics))
DELETE FROM conversation c WHERE c.id IN(SELECT id FROM convs);

-- name: RemoveParticipant :exec
UPDATE conversation_participant
SET    left_at = now()
WHERE  conversation_id = $1
  AND  principal_type  = $2
  AND  principal_id    = $3
  AND  left_at IS NULL;

-- name: ListConversationParticipants :many
SELECT * FROM conversation_participant
WHERE  conversation_id = $1
  AND  (sqlc.narg('include_removed')::bool IS TRUE OR left_at IS NULL)
ORDER BY joined_at ASC;

-- name: IsActiveParticipant :one
-- Access-control check: is this (principal_type, principal_id) currently
-- an active participant of this conversation?
SELECT EXISTS (
    SELECT 1
    FROM   conversation_participant
    WHERE  conversation_id = $1
      AND  principal_type  = $2
      AND  principal_id    = $3
      AND  left_at IS NULL
) AS is_member;

-- name: UpdateParticipantReadState :exec
UPDATE conversation_participant
SET    last_read_topic_id = $4,
       last_read_at       = now()
WHERE  conversation_id = $1
  AND  principal_type  = $2
  AND  principal_id    = $3;

-- ═══════════════════════════════════════════════════════════════════
-- Topics
-- ═══════════════════════════════════════════════════════════════════

-- name: CreateTopic :one
INSERT INTO topic (conversation_id, title, created_by_type, created_by_id)
VALUES ($1, sqlc.narg('title'), $2, $3)
RETURNING *;

-- name: GetTopic :one
SELECT * FROM topic WHERE id = $1;

-- name: ListTopicsInConversation :many
-- Topic list inside a conversation, latest activity first. Archived
-- topics included only when caller asks (sidebar hides them by default).
SELECT *
FROM   topic
WHERE  conversation_id = $1
  AND  (sqlc.narg('include_archived')::bool IS TRUE OR archived_at IS NULL)
ORDER BY last_message_at DESC;

-- name: UpdateTopicTitle :one
UPDATE topic SET title = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ArchiveTopic :exec
UPDATE topic SET archived_at = now(), updated_at = now()
WHERE id = $1;

-- ═══════════════════════════════════════════════════════════════════
-- Messages (v2 columns on chat_message)
-- ═══════════════════════════════════════════════════════════════════
--
-- Group messages derive role from sender_type and use a topic rather than
-- chat_session_id. Existing single-chat messages retain their original session.

-- name: CreateMessageV2 :one
-- Derives legacy chat_message.role from sender_type so the column stays
-- NOT NULL (keeps upstream callers that read msg.Role as a non-null
-- string compatible). 'member' → 'user', 'agent' → 'assistant'.
-- chat_session_id is left NULL — v2 messages live under a topic, not a
-- session. Migration 564 makes that column nullable.
INSERT INTO chat_message
    (topic_id, sender_type, sender_id, content, task_id, role)
VALUES ($1, $2, $3, $4, sqlc.narg('task_id'),
        CASE $2::text WHEN 'agent' THEN 'assistant' ELSE 'user' END)
RETURNING *;

-- name: ListMessagesByTopic :many
SELECT *
FROM   chat_message
WHERE  topic_id = $1
ORDER  BY created_at ASC;

-- name: GetMessageV2 :one
SELECT * FROM chat_message WHERE id = $1 AND topic_id IS NOT NULL;

-- name: GetConversationWorkspaceID :one
-- ACL-less workspace lookup. Used by the task-completion path in
-- service/task.go, which has no caller workspace context — write-time
-- ACL is already enforced upstream by GetConversation (workspace-scoped)
-- when handlers serve user requests.
SELECT workspace_id FROM conversation WHERE id = $1;

-- name: CreateTopicTask :one
-- v2 sibling of CreateChatTask (chat.sql): enqueues an agent task pointing
-- at a topic instead of a chat_session. The dispatcher polls regardless of
-- which source pointer is set; only the completion handler needs to branch.
INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, topic_id,
  initiator_user_id, originator_user_id, accountable_user_id, originator_source,
  trigger_evidence_kind, trigger_evidence_ref_id)
VALUES (@agent_id, @runtime_id, NULL, 'queued', @priority, @topic_id,
  @originator_user_id, @originator_user_id, @originator_user_id, 'direct_human',
  'chat_message', @trigger_message_id)
RETURNING *;

-- name: LockConversationDM :exec
SELECT pg_advisory_xact_lock(hashtextextended(@signature::text, 0));

-- name: ListConversationRecipientUsers :many
SELECT p.principal_id
FROM conversation_participant p
JOIN conversation c ON c.id=p.conversation_id
JOIN member m ON m.user_id=p.principal_id AND m.workspace_id=c.workspace_id
WHERE p.conversation_id=$1 AND p.principal_type='member' AND p.left_at IS NULL;

-- name: ListTopicRecipientUsers :many
SELECT p.principal_id
FROM topic t
JOIN conversation c ON c.id=t.conversation_id
JOIN conversation_participant p ON p.conversation_id=c.id
JOIN member m ON m.user_id=p.principal_id AND m.workspace_id=c.workspace_id
WHERE t.id=$1 AND p.principal_type='member' AND p.left_at IS NULL;

-- name: GetTopicAttachmentByStorageURL :one
SELECT a.* FROM attachment a
LEFT JOIN chat_message m ON m.id=a.chat_message_id
-- Compare the storage path, not the deployment origin. Case-fold it because
-- local storage may be hosted on a case-insensitive filesystem (macOS).
WHERE lower(regexp_replace(a.url, '^https?://[^/]+', ''))=lower(regexp_replace(@storage_url::text, '^https?://[^/]+', ''))
  AND (a.topic_id IS NOT NULL OR m.topic_id IS NOT NULL)
LIMIT 1;

-- name: LinkTopicAttachmentsToMessage :many
UPDATE attachment
SET chat_message_id= @chat_message_id, topic_id= @topic_id
WHERE workspace_id= @workspace_id
  AND uploader_type='member' AND uploader_id= @uploader_id
  AND id=ANY(@attachment_ids::uuid[])
  AND issue_id IS NULL AND comment_id IS NULL AND chat_session_id IS NULL
  AND chat_message_id IS NULL AND source_context_id IS NULL AND task_id IS NULL
  AND (topic_id IS NULL OR topic_id= @topic_id)
RETURNING id;

-- name: ListTopicMessagesForRun :many
SELECT m.* FROM chat_message m
WHERE m.topic_id= @topic_id
  AND m.created_at <= (SELECT trigger.created_at FROM chat_message trigger WHERE trigger.id= @trigger_message_id)
ORDER BY m.created_at DESC,m.id DESC
LIMIT 20;

-- name: ListTopicRuns :many
SELECT id,agent_id,status,error,trigger_evidence_ref_id,created_at FROM agent_task_queue
WHERE topic_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100;

-- name: CountUnreadInConversation :one
-- For inbox / sidebar badges: how many messages in this conversation
-- arrived after the viewer's last_read_at, excluding messages sent by
-- the viewer themself? The participant's last_read_at is the watermark.
SELECT COUNT(*)::bigint AS unread_count
FROM   chat_message m
JOIN   topic t ON t.id = m.topic_id
JOIN   conversation_participant p
       ON p.conversation_id = t.conversation_id
WHERE  t.conversation_id = $1
  AND  p.principal_type  = 'member'
  AND  p.principal_id    = $2
  AND  p.left_at IS NULL
  AND  (p.last_read_at IS NULL OR m.created_at > p.last_read_at)
  AND  NOT (m.sender_type = 'member' AND m.sender_id = $2);

-- name: ListPendingTopicTaskByTopic :one
-- v2 sibling of GetPendingChatTask (chat.sql): the most recent in-flight
-- task (queued/dispatched/running) for a topic. Used by the FAB / chat
-- page to restore pending state after refresh or reopen.
SELECT id, status FROM agent_task_queue
WHERE topic_id = $1 AND status IN ('queued', 'dispatched', 'running')
ORDER BY created_at DESC
LIMIT 1;

-- name: ListPendingTopicTasksByCreator :many
-- v2 sibling of ListPendingChatTasksByCreator (chat.sql): every in-flight
-- topic task owned by a given member in this workspace. Drives the FAB's
-- "running" indicator when the popover is closed.
SELECT atq.id AS task_id, atq.status, atq.topic_id
FROM agent_task_queue atq
JOIN topic t ON t.id = atq.topic_id
JOIN conversation c ON c.id = t.conversation_id
JOIN conversation_participant p
  ON p.conversation_id = c.id
 WHERE c.workspace_id = $1
   AND p.principal_type = 'member'
   AND p.principal_id = $2
   AND p.left_at IS NULL
   AND atq.status IN ('queued', 'dispatched', 'running')
ORDER BY atq.created_at DESC;

-- name: ListUnreadCountsByTopicForMember :many
-- Read markers: for each non-archived topic the caller participates in
-- (across every active membership in this workspace), count how many
-- messages were sent by someone else AFTER the caller's last_read_at.
-- Drives the FAB aggregate badge AND the per-topic dot in SessionDropdown.
--
-- Only returns rows where unread_count > 0 (HAVING) — frontend defaults
-- missing topics to 0 so the cardinality stays tiny.
--
-- Index notes: WHERE filter hits idx_message_topic_created on
-- (topic_id, created_at) and idx_participant_active_lookup on
-- (principal_type, principal_id) WHERE left_at IS NULL — both already
-- created by the collaboration index migrations.
SELECT t.id AS topic_id,
       t.conversation_id,
       COUNT(m.id)::bigint AS unread_count
FROM   topic t
JOIN   conversation c ON c.id = t.conversation_id
JOIN   conversation_participant p
       ON p.conversation_id = c.id
JOIN   chat_message m ON m.topic_id = t.id
WHERE  c.workspace_id    = $1
  AND  p.principal_type  = 'member'
  AND  p.principal_id    = $2
  AND  p.left_at IS NULL
  AND  t.archived_at IS NULL
  AND  (p.last_read_at IS NULL OR m.created_at > p.last_read_at)
  AND  NOT (m.sender_type = 'member' AND m.sender_id = $2)
GROUP BY t.id, t.conversation_id
HAVING COUNT(m.id) > 0;

-- name: GetLastTopicTaskSession :one
-- v2 sibling of GetLastTaskSession (agent.sql, which keys on issue_id):
-- the session_id + work_dir from the most recent completed topic task,
-- used to resume the Claude session so multi-turn conversations within
-- the same topic don't restart from cold. Returns ErrNoRows when no
-- prior completed task with a session exists (handler degrades to a
-- fresh session, same as v1 first-message behavior).
SELECT session_id, work_dir FROM agent_task_queue
WHERE topic_id = @topic_id AND agent_id = @agent_id AND runtime_id = @runtime_id
  AND status = 'completed'
  AND session_id IS NOT NULL
ORDER BY completed_at DESC
LIMIT 1;
-- name: CanReadTopic :one
SELECT EXISTS (
  SELECT 1 FROM topic t
  JOIN conversation c ON c.id = t.conversation_id
  JOIN conversation_participant p ON p.conversation_id = c.id
  WHERE t.id = @topic_id::uuid AND c.workspace_id = @workspace_id::uuid
    AND p.left_at IS NULL AND p.principal_type = @principal_type::text
    AND p.principal_id = @principal_id::uuid
    AND (p.principal_type <> 'member' OR EXISTS (
      SELECT 1 FROM member m WHERE m.workspace_id = c.workspace_id AND m.user_id = p.principal_id
    ))
)::boolean;
