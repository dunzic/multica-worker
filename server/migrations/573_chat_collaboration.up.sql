-- Group collaboration: conversation + participant + topic tables, plus columns on
-- chat_message and agent_task_queue that point at the new topic-scoped model.
--
-- Additive schema: existing single-chat sessions and tasks retain their original
-- ownership. Migration 573 creates an idempotent history projection; handlers do
-- not dual-write between the two chat surfaces. Rollback disables the new surface
-- without removing its schema or collaboration data.

-- ──────────────────────────────────────────────────────────────────────
-- Conversation: the stable "who". DM (2 participants) or group (N).
-- dm_signature is the canonical sorted "type:id|type:id" string for the
-- two participants of a dm; NULL for groups. A partial UNIQUE index on
-- it enforces "one dm per (workspace, pair)" without needing a trigger.
-- ──────────────────────────────────────────────────────────────────────
CREATE TABLE conversation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('dm', 'group')),
    title           TEXT,
    dm_signature    TEXT,
    created_by      UUID NOT NULL,
    archived_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT conversation_dm_signature_shape
        CHECK ((kind = 'dm' AND dm_signature IS NOT NULL)
            OR (kind = 'group' AND dm_signature IS NULL))
);



-- One active DM per (workspace, sorted participant pair). Excludes archived
-- conversations so re-creating a DM after archiving the old one is allowed.


-- ──────────────────────────────────────────────────────────────────────
-- Participant: polymorphic (member│agent). Per-participant read state
-- lives here directly — saves a join on the hot inbox-list query.
-- left_at is soft-remove (preserves history attribution); active
-- membership is "left_at IS NULL".
-- ──────────────────────────────────────────────────────────────────────
CREATE TABLE conversation_participant (
    conversation_id     UUID NOT NULL,
    principal_type      TEXT NOT NULL CHECK (principal_type IN ('member', 'agent')),
    principal_id        UUID NOT NULL,
    role                TEXT NOT NULL DEFAULT 'member'
                            CHECK (role IN ('member', 'admin')),
    joined_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at             TIMESTAMPTZ,
    last_read_topic_id  UUID,                       -- Read marker validated by the application
    last_read_at        TIMESTAMPTZ,
    PRIMARY KEY (conversation_id, principal_type, principal_id)
);

-- "Which conversations am I (member│agent) actively part of?" — the
-- driver query for sidebar lists. WHERE left_at IS NULL keeps the
-- index lean (departed members aren't in active inbox queries).


-- ──────────────────────────────────────────────────────────────────────
-- Topic: a threaded subject inside a conversation. "New session" = new
-- topic. last_message_at is denormalized so the topic-list ORDER BY
-- doesn't have to scan messages. Updated by trigger on message insert
-- (added below).
-- ──────────────────────────────────────────────────────────────────────
CREATE TABLE topic (
	legacy_chat_session_id UUID,
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id     UUID NOT NULL,
    title               TEXT,
    created_by_type     TEXT NOT NULL CHECK (created_by_type IN ('member', 'agent')),
    created_by_id       UUID NOT NULL,
    archived_at         TIMESTAMPTZ,
    last_message_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);



-- ──────────────────────────────────────────────────────────────────────
-- chat_message topic columns. Keep role + chat_session_id alive
-- so existing handlers and any in-flight assistant replies don't break.
-- New columns are nullable; migration 573 projects old messages while new
-- group messages require a validated topic in the application.
-- ──────────────────────────────────────────────────────────────────────
ALTER TABLE chat_message
    ADD COLUMN topic_id     UUID,
    ADD COLUMN sender_type  TEXT CHECK (sender_type IN ('member', 'agent')),
    ADD COLUMN sender_id    UUID;





-- Bump topic.last_message_at on every new message landed under a topic.
-- This keeps the "recent activity" sort cheap (idx_topic_conv_last_msg).
-- Idempotent: only bumps if the incoming created_at is fresher (out-of-
-- order arrivals don't backdate the topic).
CREATE OR REPLACE FUNCTION chat_v2_touch_topic_on_message()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.topic_id IS NOT NULL THEN
        UPDATE topic
        SET last_message_at = NEW.created_at,
            updated_at      = now()
        WHERE id = NEW.topic_id
          AND last_message_at < NEW.created_at;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER chat_v2_touch_topic_trigger
    AFTER INSERT ON chat_message
    FOR EACH ROW
    EXECUTE FUNCTION chat_v2_touch_topic_on_message();

-- ──────────────────────────────────────────────────────────────────────
-- agent_task_queue: gain topic_id alongside the existing chat_session_id.
-- Group tasks use topic_id; existing tasks retain chat_session_id. Do not
-- repoint in-flight tasks or introduce dual writes.
-- ──────────────────────────────────────────────────────────────────────
ALTER TABLE agent_task_queue
    ADD COLUMN topic_id UUID;



ALTER TABLE chat_message ALTER COLUMN chat_session_id DROP NOT NULL;
