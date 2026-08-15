package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This live-Postgres test applies the real role-source migration sequence in a
// private schema. It catches DDL and CREATE INDEX CONCURRENTLY failures that
// sqlc parsing and static policy tests cannot detect. Environments without
// Postgres skip through openTestPool, matching the migration suite convention.
func TestRoleSourceMigrationsRoundTripInIsolatedSchema(t *testing.T) {
	pool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	schema := fmt.Sprintf("role_source_migration_test_%d_%d", time.Now().UnixNano(), rand.Uint32())
	schemaIdentifier := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, "SET search_path TO public")
		_, _ = conn.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaIdentifier+" CASCADE")
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+schemaIdentifier+", public"); err != nil {
		t.Fatal(err)
	}
	// The role-source migrations deliberately avoid foreign keys, but the task
	// pin trigger attaches to the real queue and reads the real agent table.
	// Provide only the columns that trigger compilation needs; behavior is
	// exercised by the handler/role-source integration suite against the full
	// schema.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE agent (
			id UUID NOT NULL, workspace_id UUID NOT NULL, name TEXT,
			description TEXT, runtime_mode TEXT, runtime_id UUID,
			instructions TEXT, custom_env JSONB, mcp_config JSONB
		);
		CREATE TABLE agent_task_queue (
			id UUID NOT NULL, agent_id UUID NOT NULL, parent_task_id UUID,
			status TEXT NOT NULL DEFAULT 'queued', completed_at TIMESTAMPTZ,
			error TEXT, failure_reason TEXT, prepare_lease_expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE skill (
			id UUID NOT NULL, workspace_id UUID NOT NULL, name TEXT,
			description TEXT, content TEXT, config JSONB
		);
		CREATE TABLE skill_file (skill_id UUID NOT NULL, path TEXT, content TEXT);
		CREATE TABLE agent_skill (agent_id UUID NOT NULL, skill_id UUID NOT NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE);
	`); err != nil {
		t.Fatal(err)
	}

	upFiles, err := filepath.Glob(filepath.Join("..", "..", "migrations", "[0-9]*_role_source_*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(upFiles)
	expectedTables := 0
	expectedIndexes := map[string]struct{}{}
	indexPattern := regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+([a-z0-9_]+)\b`)
	dropIndexPattern := regexp.MustCompile(`(?i)\bDROP\s+INDEX\s+CONCURRENTLY\s+(?:IF\s+EXISTS\s+)?([a-z0-9_]+)\b`)
	for _, name := range upFiles {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(name), err)
		}
		expectedTables += strings.Count(strings.ToUpper(string(body)), "CREATE TABLE ")
		for _, match := range dropIndexPattern.FindAllSubmatch(body, -1) {
			delete(expectedIndexes, string(match[1]))
		}
		if match := indexPattern.FindSubmatch(body); len(match) == 2 {
			expectedIndexes[string(match[1])] = struct{}{}
		}
	}

	var tableCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = $1 AND table_name LIKE 'role_source%'
	`, schema).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != expectedTables {
		t.Fatalf("role-source table count = %d, want %d", tableCount, expectedTables)
	}
	var indexCount int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname LIKE 'role_source%'`, schema).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != len(expectedIndexes) {
		t.Fatalf("role-source index count = %d, want %d unique final names", indexCount, len(expectedIndexes))
	}

	verifyRoleSourceTaskPinTriggers(t, ctx, conn)

	downFiles := make([]string, len(upFiles))
	for index, up := range upFiles {
		downFiles[index] = strings.TrimSuffix(up, ".up.sql") + ".down.sql"
	}
	sort.Sort(sort.Reverse(sort.StringSlice(downFiles)))
	for _, name := range downFiles {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("revert %s: %v", filepath.Base(name), err)
		}
	}
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = $1 AND table_name LIKE 'role_source%'
	`, schema).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatalf("role-source tables remain after down migrations: %d", tableCount)
	}
}

func verifyRoleSourceTaskPinTriggers(t *testing.T, ctx context.Context, conn *pgxpool.Conn) {
	t.Helper()
	const (
		workspaceID = "00000000-0000-4000-8000-000000000001"
		userID      = "00000000-0000-4000-8000-000000000002"
		runtimeID   = "00000000-0000-4000-8000-000000000003"
		sourceID    = "00000000-0000-4000-8000-000000000004"
		agentID     = "00000000-0000-4000-8000-000000000005"
		taskID      = "00000000-0000-4000-8000-000000000006"
		retryID     = "00000000-0000-4000-8000-000000000007"
		digestA     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		digestB     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		digestC     = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		digestD     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	manifest := `{"contract_version":"1.0","roles":[{"id":"writer","display_name":"Writer","instructions":{"path":"roles/writer.md","digest":"` + digestC + `","size_bytes":1,"media_type":"text/markdown"},"skills":[],"capability_bindings":[],"environment":[],"mcp":[],"automations":[]}],"capabilities":[]}`
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent (id, workspace_id, name, description, runtime_mode, runtime_id, instructions, custom_env, mcp_config)
		VALUES ($1, $2, 'Writer', '', 'local', $3, 'write', '{}'::jsonb, NULL)
	`, agentID, workspaceID, runtimeID); err != nil {
		t.Fatalf("seed task-pin agent: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO role_source (
			id, workspace_id, runtime_id, name, kind, adapter_version,
			daemon_config_id, config_redacted, created_by, updated_by
		) VALUES ($1, $2, $3, 'Roles', 'agentwaker_directory', '1.0.0', 'cfg', '{}'::jsonb, $4, $4)
	`, sourceID, workspaceID, runtimeID, userID); err != nil {
		t.Fatalf("seed task-pin source: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO role_source_snapshot (
			source_id, workspace_id, snapshot_digest, manifest_digest, kind,
			adapter_version, contract_version, manifest, diagnostics,
			source_evidence, reported_by_runtime_id
		) VALUES ($1, $2, $3, $4, 'agentwaker_directory', '1.0.0', '1.0', $5::jsonb, '[]'::jsonb, $6::jsonb, $7)
	`, sourceID, workspaceID, digestA, digestB, manifest, `{"tree_digest":"`+digestD+`"}`, runtimeID); err != nil {
		t.Fatalf("seed task-pin snapshot: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO role_source_object_mapping (
			source_id, workspace_id, source_kind, source_parent_id,
			source_object_id, target_kind, target_id, ownership_mask,
			last_applied_digest, last_snapshot_digest
		) VALUES ($1, $2, 'role', '', 'writer', 'agent', $3,
			'["instructions"]'::jsonb, $4, $5)
	`, sourceID, workspaceID, agentID, digestC, digestA); err != nil {
		t.Fatalf("seed task-pin mapping: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id) VALUES ($1, $2)
	`, taskID, agentID); err != nil {
		t.Fatalf("capture root task pin: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, parent_task_id) VALUES ($1, $2, $3)
	`, retryID, agentID, taskID); err != nil {
		t.Fatalf("capture inherited task pin: %v", err)
	}
	var rootSnapshot, retrySnapshot, inheritedFrom string
	if err := conn.QueryRow(ctx, `
		SELECT root.snapshot_digest, retry.snapshot_digest, retry.inherited_from_task_id::text
		FROM role_source_task_pin root
		JOIN role_source_task_pin retry ON retry.task_id = $2
		WHERE root.task_id = $1
	`, taskID, retryID).Scan(&rootSnapshot, &retrySnapshot, &inheritedFrom); err != nil {
		t.Fatalf("read captured task pins: %v", err)
	}
	if rootSnapshot != digestA || retrySnapshot != digestA || inheritedFrom != taskID {
		t.Fatalf("pin inheritance = root %s retry %s parent %s", rootSnapshot, retrySnapshot, inheritedFrom)
	}
	if _, err := conn.Exec(ctx, `
		UPDATE role_source_object_mapping
		SET last_snapshot_digest = $1, last_applied_digest = $2
		WHERE source_id = $3 AND source_kind = 'role' AND source_object_id = 'writer'
	`, digestB, digestD, sourceID); err != nil {
		t.Fatalf("advance mapping and invalidate queued tasks: %v", err)
	}
	var cancelled int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE id IN ($1, $2) AND status = 'cancelled'
		  AND failure_reason = 'role_source_version_stale'
	`, taskID, retryID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Fatalf("invalidated task count = %d, want 2", cancelled)
	}
}
