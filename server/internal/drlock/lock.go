// Package drlock contains the source-neutral advisory-lock protocol shared by
// role-source mutation paths and disaster-recovery tooling.
package drlock

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const AdvisoryLockKey int64 = 7244554146635925606

type Guard struct{ pool *pgxpool.Pool }

func NewGuard(pool *pgxpool.Pool) *Guard {
	if pool == nil {
		return nil
	}
	return &Guard{pool: pool}
}

func (g *Guard) WithDestructive(ctx context.Context, fn func(context.Context) error) error {
	if g == nil || g.pool == nil {
		return fmt.Errorf("role-source DR guard is not configured")
	}
	if fn == nil {
		return fmt.Errorf("role-source DR guard requires destructive work")
	}
	conn, err := g.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire role-source DR guard connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock_shared($1)", AdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire role-source DR shared lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock_shared($1)", AdvisoryLockKey)
	}()
	return fn(ctx)
}
