package migrations

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestChatCollaborationHistoryBackfillPreservesLegacyOwnership(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Temporary fixtures shadow every table the real backfill touches.
	_, err = tx.Exec(ctx, `
 CREATE TEMP TABLE chat_session(id UUID PRIMARY KEY, workspace_id UUID, creator_id UUID, agent_id UUID, title TEXT, status TEXT, unread_since TIMESTAMPTZ, created_at TIMESTAMPTZ DEFAULT now(), updated_at TIMESTAMPTZ DEFAULT now());
 CREATE TEMP TABLE conversation(id UUID PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id UUID,kind TEXT,dm_signature TEXT,title TEXT,created_by UUID,archived_at TIMESTAMPTZ,created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ);
 CREATE TEMP TABLE conversation_participant(conversation_id UUID,principal_type TEXT,principal_id UUID,joined_at TIMESTAMPTZ,last_read_at TIMESTAMPTZ);
 CREATE TEMP TABLE topic(id UUID PRIMARY KEY DEFAULT gen_random_uuid(),legacy_chat_session_id UUID UNIQUE,conversation_id UUID,title TEXT,created_by_type TEXT,created_by_id UUID,archived_at TIMESTAMPTZ,last_message_at TIMESTAMPTZ,created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ);
 CREATE TEMP TABLE chat_message(id UUID PRIMARY KEY,chat_session_id UUID,topic_id UUID,sender_type TEXT,sender_id UUID,role TEXT,content TEXT);
 CREATE TEMP TABLE agent_task_queue(id UUID PRIMARY KEY,chat_session_id UUID,topic_id UUID,status TEXT);
 INSERT INTO chat_session(id,workspace_id,creator_id,agent_id,title,status) VALUES
 ('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003','Empty active','active'),
 ('00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003','Archived','archived');
 INSERT INTO chat_message(id,chat_session_id,role,content) VALUES('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000012','user','Original résumé request');
 INSERT INTO agent_task_queue(id,chat_session_id,status) VALUES('00000000-0000-0000-0000-000000000031','00000000-0000-0000-0000-000000000011','running');
 `)
	if err != nil {
		t.Fatal(err)
	}
	sql := readMigrationFile(t, "573_chat_history_backfill.up.sql")
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var convs, topics, archived, messages, legacyTasks int
	err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM conversation),
 (SELECT count(*) FROM topic),
 (SELECT count(*) FROM topic WHERE archived_at IS NOT NULL),
 (SELECT count(*) FROM chat_message WHERE topic_id IS NOT NULL AND sender_type='member' AND content='Original résumé request' AND chat_session_id IS NOT NULL),
 (SELECT count(*) FROM agent_task_queue WHERE topic_id IS NULL AND chat_session_id IS NOT NULL AND status='running')`).Scan(&convs, &topics, &archived, &messages, &legacyTasks)
	if err != nil {
		t.Fatal(err)
	}
	if convs != 1 || topics != 2 || archived != 1 || messages != 1 || legacyTasks != 1 {
		t.Fatalf("backfill counts: conversations=%d topics=%d archived=%d messages=%d unchanged running tasks=%d", convs, topics, archived, messages, legacyTasks)
	}
}
