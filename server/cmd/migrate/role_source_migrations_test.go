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
	if _, err := conn.Exec(ctx, "SET search_path TO "+schemaIdentifier); err != nil {
		t.Fatal(err)
	}

	upFiles, err := filepath.Glob(filepath.Join("..", "..", "migrations", "2*_role_source_*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(upFiles)
	expectedTables, expectedIndexes := 0, 0
	indexPattern := regexp.MustCompile(`(?i)\bCREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b`)
	for _, name := range upFiles {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(name), err)
		}
		expectedTables += strings.Count(strings.ToUpper(string(body)), "CREATE TABLE ")
		if indexPattern.Match(body) {
			expectedIndexes++
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
	if indexCount != expectedIndexes {
		t.Fatalf("role-source index count = %d, want %d", indexCount, expectedIndexes)
	}

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
