-- Preserve every existing session, including empty and archived sessions.
-- Never delete historical messages, attachments, or repoint in-flight runs.
DO $$
DECLARE
    s            RECORD;
    new_conv_id  UUID;
    new_topic_id UUID;
    sig          TEXT;
BEGIN
    FOR s IN
        SELECT cs.*
        FROM chat_session cs
        ORDER BY cs.created_at  -- stable order so the test of "first creator wins" is deterministic
    LOOP
        -- Canonical DM signature: sort the two sides lexicographically so
        -- the order in which a session was created can't yield two
        -- different signatures for the same pair.
        sig := CASE
            WHEN ('member:' || s.creator_id::text) < ('agent:' || s.agent_id::text)
            THEN 'member:' || s.creator_id::text || '|' || 'agent:' || s.agent_id::text
            ELSE 'agent:'  || s.agent_id::text   || '|' || 'member:' || s.creator_id::text
        END;

        -- Reuse a conversation for this pair if one already exists
        -- (idempotency for re-runs / partial failures).
        SELECT id INTO new_conv_id
        FROM conversation
        WHERE workspace_id  = s.workspace_id
          AND dm_signature  = sig
          ORDER BY created_at
        LIMIT 1;

        IF new_conv_id IS NULL THEN
            INSERT INTO conversation (
                workspace_id, kind, dm_signature, title, created_by,
                archived_at, created_at, updated_at
            ) VALUES (
                s.workspace_id, 'dm', sig, NULL, s.creator_id,
                CASE WHEN s.status = 'archived' THEN s.updated_at ELSE NULL END,
                s.created_at, s.updated_at
            )
            RETURNING id INTO new_conv_id;

            -- Two participants: the creator (member) and the bound agent.
            -- Creator's last_read_at mirrors the old unread_since logic:
            --   - unread_since NULL  → user has caught up → last_read_at = updated_at
            --   - unread_since set   → user has unread    → last_read_at = unread_since - 1µs
            INSERT INTO conversation_participant (
                conversation_id, principal_type, principal_id,
                joined_at, last_read_at
            ) VALUES
                (new_conv_id, 'member', s.creator_id, s.created_at,
                 CASE WHEN s.unread_since IS NULL
                      THEN s.updated_at
                      ELSE s.unread_since - INTERVAL '1 microsecond'
                 END),
                (new_conv_id, 'agent', s.agent_id, s.created_at, NULL);
        END IF;

        -- One topic per chat_session. If we re-run after a partial
        -- migration, detect its topic by the original session ID, even when empty.
        SELECT id INTO new_topic_id FROM topic WHERE legacy_chat_session_id=s.id LIMIT 1;

        IF new_topic_id IS NULL THEN
            INSERT INTO topic (
                legacy_chat_session_id, conversation_id, title, created_by_type, created_by_id,
                archived_at, last_message_at, created_at, updated_at
            ) VALUES (
                s.id, new_conv_id, s.title, 'member', s.creator_id,
                CASE WHEN s.status = 'archived' THEN s.updated_at ELSE NULL END,
                s.updated_at, s.created_at, s.updated_at
            )
            RETURNING id INTO new_topic_id;
        END IF;

        -- Point this session's messages at the new topic and attribute
        -- sender polymorphically. role='user' → creator (member),
        -- role='assistant' → bound agent.
        UPDATE chat_message
        SET topic_id    = new_topic_id,
            sender_type = CASE WHEN role = 'user' THEN 'member' ELSE 'agent' END,
            sender_id   = CASE WHEN role = 'user' THEN s.creator_id ELSE s.agent_id END
        WHERE chat_session_id = s.id
          AND topic_id IS NULL;

        -- In-flight tasks keep their original source and completion contract.
    END LOOP;
END
$$;
