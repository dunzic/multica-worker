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
	"github.com/multica-ai/multica/server/internal/storage"
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
	DRGuard RoleSourceDestructiveGuard
	Logger  *slog.Logger
	Metrics *metrics.RoleSourceArtifactGCMetrics

	deleteTimeout time.Duration
}

type RoleSourceDestructiveGuard interface {
	WithDestructive(context.Context, func(context.Context) error) error
}

type roleSourceArtifactObjectPurger interface {
	PurgeObjectWithResult(context.Context, string) (storage.PermanentPurgeResult, error)
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
	if r.DRGuard == nil {
		r.releaseDeleteIntent(ctx, intent, token, errors.New("role-source DR guard is not configured"))
		return
	}
	var result storage.PermanentPurgeResult
	err := r.DRGuard.WithDestructive(ctx, func(guarded context.Context) error {
		var purgeErr error
		result, purgeErr = r.purge(guarded, intent)
		return purgeErr
	})
	if err != nil {
		r.releaseDeleteIntent(ctx, intent, token, err)
		return
	}
	r.advanceDeleteIntent(ctx, intent, token, result)
}

func (r *RoleSourceArtifactReconciler) purge(ctx context.Context, intent db.RoleSourceArtifactDeleteIntent) (storage.PermanentPurgeResult, error) {
	timeout := r.deleteTimeout
	if timeout <= 0 {
		timeout = roleSourceArtifactGCDelete
	}
	deleteCtx, cancel := context.WithTimeout(ctx, timeout)
	result, err := purgeRoleSourceArtifactObject(deleteCtx, r.Storage, intent.StorageKey)
	cancel()
	return result, err
}

func (r *RoleSourceArtifactReconciler) releaseDeleteIntent(ctx context.Context, intent db.RoleSourceArtifactDeleteIntent, token pgtype.UUID, cause error) {
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
	if ctx.Err() == nil {
		r.logger().Warn("role source artifact GC purge failed", "error", cause)
	}
}

func (r *RoleSourceArtifactReconciler) advanceDeleteIntent(ctx context.Context, intent db.RoleSourceArtifactDeleteIntent, token pgtype.UUID, result storage.PermanentPurgeResult) {
	if r.Metrics != nil {
		r.Metrics.ObjectsDeleted.Inc()
	}
	pass := int(intent.TombstonePass)
	if pass >= len(roleSourceArtifactGCTombstoneSchedule) {
		writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer writeCancel()
		params, err := buildRoleSourceArtifactPurgeReceiptParams(intent, token, result, time.Now())
		if err != nil {
			r.releaseDeleteIntent(ctx, intent, token, err)
			return
		}
		receipt, err := r.Queries.CompleteRoleSourceArtifactDeleteIntent(writeCtx, params)
		if err != nil && ctx.Err() == nil {
			r.logger().Warn("role source artifact GC completion failed", "storage_key", intent.StorageKey, "error", err)
			return
		}
		if err == nil && r.Metrics != nil {
			r.Metrics.ReceiptsCompleted.Inc()
			r.Metrics.LogicalBytesConfirmedAbsent.Add(float64(receipt.LogicalBytesConfirmedAbsent))
		}
		return
	}
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer writeCancel()
	tombstoned, err := r.Queries.TombstoneRoleSourceArtifactDeleteIntent(writeCtx, db.TombstoneRoleSourceArtifactDeleteIntentParams{
		TombstonePass: int32(pass + 1), PurgeBackend: pgtype.Text{String: result.Backend, Valid: true},
		PurgeMode: pgtype.Text{String: result.Mode, Valid: true}, PurgedVersionCount: result.VersionsDeleted,
		PurgedDeleteMarkerCount: result.DeleteMarkersDeleted, PurgedObservedBytes: result.ObservedBytesDeleted,
		AbsenceVerified: result.VerifiedAbsent, PurgedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		NextDelay:  pgInterval(roleSourceArtifactGCTombstoneSchedule[pass]),
		StorageKey: intent.StorageKey, LeaseToken: token,
	})
	if (err != nil || tombstoned != 1) && ctx.Err() == nil {
		r.logger().Warn("role source artifact GC tombstone failed", "storage_key", intent.StorageKey, "error", err)
	}
}

func purgeRoleSourceArtifactObject(ctx context.Context, objectStorage MediaObjectDeleter, key string) (storage.PermanentPurgeResult, error) {
	if purger, ok := objectStorage.(roleSourceArtifactObjectPurger); ok {
		result, err := purger.PurgeObjectWithResult(ctx, key)
		if err != nil {
			return storage.PermanentPurgeResult{}, err
		}
		if err := validatePermanentPurgeResult(result); err != nil {
			return storage.PermanentPurgeResult{}, err
		}
		return result, nil
	}
	return storage.PermanentPurgeResult{}, errors.New("role source artifact storage does not support receipt-bearing permanent object purge")
}
