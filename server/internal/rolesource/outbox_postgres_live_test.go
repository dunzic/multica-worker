package rolesource

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This opt-in test requires a disposable fully migrated PostgreSQL 17 DB. It
// proves concurrent workers cannot claim the same event, expired leases are
// recoverable, stale tokens cannot acknowledge, and the retry ceiling becomes
// terminal instead of leaving an immortal publishing row.
func TestRoleSourceOutboxPostgresLeaseAndRetryStateMachine(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_OUTBOX_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_OUTBOX_TEST=1")
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

	workspaceID, sourceID, actorID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id, name, slug) VALUES ($1, 'Outbox test', $2)`, workspaceID, "outbox-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM role_source_outbox WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspaceID)
	})
	queries := db.New(pool)
	invalid := db.InsertRoleSourceOutboxEventParams{
		ID: pgUUID(uuid.New()), WorkspaceID: pgUUID(workspaceID), SourceID: pgUUID(sourceID), EventType: "role_source:applied",
		ActorType: "user", ActorID: pgUUID(actorID), ApplyID: pgUUID(uuid.New()), Mode: "unsafe",
		SnapshotDigest: "not-a-digest", PlanDigest: "sha256:" + strings.Repeat("1", 64),
		ReceiptDigest: "sha256:" + strings.Repeat("2", 64),
	}
	if _, err := queries.InsertRoleSourceOutboxEvent(ctx, invalid); err == nil {
		t.Fatal("typed outbox accepted an invalid mode and digest")
	}
	insert := func(id uuid.UUID) {
		_, err := queries.InsertRoleSourceOutboxEvent(ctx, db.InsertRoleSourceOutboxEventParams{
			ID: pgUUID(id), WorkspaceID: pgUUID(workspaceID), SourceID: pgUUID(sourceID), EventType: "role_source:applied",
			ActorType: "user", ActorID: pgUUID(actorID), ApplyID: pgUUID(uuid.New()), Mode: "apply",
			SnapshotDigest: "sha256:" + strings.Repeat("0", 64), PlanDigest: "sha256:" + strings.Repeat("1", 64),
			ReceiptDigest: "sha256:" + strings.Repeat("2", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	firstID := uuid.New()
	insert(firstID)
	lease := pgtype.Interval{Microseconds: int64((50 * time.Millisecond) / time.Microsecond), Valid: true}
	tokenA, tokenB := pgUUID(uuid.New()), pgUUID(uuid.New())
	first, err := queries.ClaimNextRoleSourceOutboxEvent(ctx, db.ClaimNextRoleSourceOutboxEventParams{LeaseToken: tokenA, LeaseDuration: lease})
	if err != nil || first.ID != pgUUID(firstID) {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	if _, err := queries.ClaimNextRoleSourceOutboxEvent(ctx, db.ClaimNextRoleSourceOutboxEventParams{LeaseToken: tokenB, LeaseDuration: lease}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("duplicate live-lease claim err=%v, want no rows", err)
	}
	if _, err := queries.MarkRoleSourceOutboxPublished(ctx, db.MarkRoleSourceOutboxPublishedParams{ID: first.ID, LeaseToken: tokenB}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale token ack err=%v, want no rows", err)
	}
	time.Sleep(75 * time.Millisecond)
	reclaimed, err := queries.ClaimNextRoleSourceOutboxEvent(ctx, db.ClaimNextRoleSourceOutboxEventParams{LeaseToken: tokenB, LeaseDuration: lease})
	if err != nil || reclaimed.Attempt != 2 {
		t.Fatalf("expired lease reclaim=%+v err=%v", reclaimed, err)
	}
	if _, err := queries.MarkRoleSourceOutboxPublished(ctx, db.MarkRoleSourceOutboxPublishedParams{ID: reclaimed.ID, LeaseToken: tokenB}); err != nil {
		t.Fatal(err)
	}

	deadID := uuid.New()
	insert(deadID)
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET status='publishing', attempt=20, lease_token=$2, lease_expires_at=now()-interval '1 second' WHERE id=$1`, deadID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if affected, err := queries.MarkExhaustedRoleSourceOutboxEventsDead(ctx); err != nil || affected != 1 {
		t.Fatalf("terminal reconciliation affected=%d err=%v", affected, err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM role_source_outbox WHERE id=$1`, deadID).Scan(&status); err != nil || status != "dead" {
		t.Fatalf("terminal status=%q err=%v", status, err)
	}
}

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }
