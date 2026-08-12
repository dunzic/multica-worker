package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	RoleSourceArtifactGCSettleDelay = 24 * time.Hour
	roleSourceArtifactGCSweep       = 5 * time.Minute
	roleSourceArtifactGCLease       = 2 * time.Minute
	roleSourceArtifactGCDelete      = 30 * time.Second
	roleSourceArtifactGCLimit       = 50
)

var roleSourceArtifactGCTombstoneSchedule = []time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// RoleSourceArtifactReconciler moves old unreachable readiness rows into a
// durable deletion intent, then performs leased idempotent deletes with a
// widening tombstone tail. Workspace teardown writes the same intent before
// removing tenant rows, so storage cleanup survives deletion of the workspace.
type RoleSourceArtifactReconciler struct {
	Queries *db.Queries
	Storage MediaObjectDeleter
	Logger  *slog.Logger
	Metrics *metrics.RoleSourceArtifactGCMetrics

	deleteTimeout time.Duration
}

func (r *RoleSourceArtifactReconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *RoleSourceArtifactReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(roleSourceArtifactGCSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *RoleSourceArtifactReconciler) RunOnce(ctx context.Context) {
	if r == nil || r.Queries == nil || r.Storage == nil {
		return
	}
	for range roleSourceArtifactGCLimit {
		_, err := r.Queries.QueueNextUnreachableRoleSourceArtifact(ctx, pgInterval(RoleSourceArtifactGCSettleDelay))
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				r.logger().Warn("role source artifact GC queue failed", "error", err)
			}
			break
		}
		if r.Metrics != nil {
			r.Metrics.Queued.Inc()
		}
	}
	for range roleSourceArtifactGCLimit {
		token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		intent, err := r.Queries.ClaimNextRoleSourceArtifactDeleteIntent(ctx, db.ClaimNextRoleSourceArtifactDeleteIntentParams{
			LeaseToken: token, LeaseDuration: pgInterval(roleSourceArtifactGCLease),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				r.logger().Warn("role source artifact GC claim failed", "error", err)
			}
			break
		}
		r.delete(ctx, intent, token)
	}
	if r.Metrics != nil {
		if counts, err := r.Queries.CountRoleSourceArtifactDeleteIntents(ctx); err == nil {
			r.Metrics.Backlog.Set(float64(counts.ActiveCount))
			r.Metrics.Tombstones.Set(float64(counts.TombstoneCount))
		}
	}
}

func (r *RoleSourceArtifactReconciler) delete(ctx context.Context, intent db.RoleSourceArtifactDeleteIntent, token pgtype.UUID) {
	timeout := r.deleteTimeout
	if timeout <= 0 {
		timeout = roleSourceArtifactGCDelete
	}
	deleteCtx, cancel := context.WithTimeout(ctx, timeout)
	err := r.Storage.DeleteObject(deleteCtx, intent.StorageKey)
	cancel()
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.DeleteFailures.Inc()
		}
		delay := time.Minute << min(int(intent.Attempt-1), 6)
		writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer writeCancel()
		released, releaseErr := r.Queries.ReleaseRoleSourceArtifactDeleteIntent(writeCtx, db.ReleaseRoleSourceArtifactDeleteIntentParams{
			RetryDelay: pgInterval(delay), StorageKey: intent.StorageKey, LeaseToken: token,
		})
		if (releaseErr != nil || released != 1) && ctx.Err() == nil {
			r.logger().Warn("role source artifact GC release failed", "storage_key", intent.StorageKey, "error", releaseErr)
		}
		return
	}
	if r.Metrics != nil {
		r.Metrics.ObjectsDeleted.Inc()
	}
	pass := int(intent.TombstonePass)
	if pass >= len(roleSourceArtifactGCTombstoneSchedule) {
		writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer writeCancel()
		completed, err := r.Queries.CompleteRoleSourceArtifactDeleteIntent(writeCtx, db.CompleteRoleSourceArtifactDeleteIntentParams{
			StorageKey: intent.StorageKey, LeaseToken: token,
		})
		if (err != nil || completed != 1) && ctx.Err() == nil {
			r.logger().Warn("role source artifact GC completion failed", "storage_key", intent.StorageKey, "error", err)
		}
		return
	}
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer writeCancel()
	tombstoned, err := r.Queries.TombstoneRoleSourceArtifactDeleteIntent(writeCtx, db.TombstoneRoleSourceArtifactDeleteIntentParams{
		TombstonePass: int32(pass + 1), NextDelay: pgInterval(roleSourceArtifactGCTombstoneSchedule[pass]),
		StorageKey: intent.StorageKey, LeaseToken: token,
	})
	if (err != nil || tombstoned != 1) && ctx.Err() == nil {
		r.logger().Warn("role source artifact GC tombstone failed", "storage_key", intent.StorageKey, "error", err)
	}
}
