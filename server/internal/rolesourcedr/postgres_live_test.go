package rolesourcedr

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/drlock"
)

// This is an opt-in restore-drill gate. It is intentionally read-only and
// expects DATABASE_URL to reference a disposable PostgreSQL 17 database that
// was restored from the manifest under test. Object/key verification remains
// in cmd/role_source_dr verify, where the operator supplies the restored
// storage and key environment.
func TestRoleSourceDRLiveManifestRoundTripAndLockExclusion(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_DR_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_DR_TEST=1")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := BuildManifest(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := BuildManifest(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if !SameDatabaseState(manifest, actual) {
		t.Fatal("manifest did not reproduce in one repeatable-read snapshot")
	}

	exclusive, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive.Release()
	if _, err := exclusive.Exec(ctx, "SELECT pg_advisory_lock($1)", drlock.AdvisoryLockKey); err != nil {
		t.Fatal(err)
	}
	defer exclusive.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", drlock.AdvisoryLockKey) //nolint:errcheck
	contender, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Release()
	lockCtx, lockCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer lockCancel()
	var acquired bool
	err = contender.QueryRow(lockCtx, "SELECT pg_try_advisory_lock_shared($1)", drlock.AdvisoryLockKey).Scan(&acquired)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		_, _ = contender.Exec(ctx, "SELECT pg_advisory_unlock_shared($1)", drlock.AdvisoryLockKey)
		t.Fatal("destructive shared lock bypassed active backup exclusive lock")
	}
}
