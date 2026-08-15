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
	roleSourceRetentionSweep = 15 * time.Minute
	roleSourceRetentionLease = 5 * time.Minute
	roleSourceRetentionLimit = 50
)

type RoleSourceRetentionControlPlane interface {
	QueueRetentionCandidates(context.Context, int32) ([]db.RoleSourceRetentionCandidate, error)
	PruneRetentionCandidate(context.Context, pgtype.UUID, pgtype.UUID) (string, error)
}

// RoleSourceRetentionReconciler is deliberately separate from artifact GC.
// It removes old snapshot content only after policy/hold/provenance checks;
// artifact GC may then reclaim bodies whose last reachability edge disappeared.
type RoleSourceRetentionReconciler struct {
	Queries  *db.Queries
	Control  RoleSourceRetentionControlPlane
	Logger   *slog.Logger
	Metrics  *metrics.RoleSourceRetentionMetrics
	Interval time.Duration
}

func (r *RoleSourceRetentionReconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *RoleSourceRetentionReconciler) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = roleSourceRetentionSweep
	}
	ticker := time.NewTicker(interval)
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

func (r *RoleSourceRetentionReconciler) RunOnce(ctx context.Context) {
	if r == nil || r.Queries == nil || r.Control == nil {
		return
	}
	queued, err := r.Control.QueueRetentionCandidates(ctx, int32(roleSourceRetentionLimit))
	if err != nil && ctx.Err() == nil {
		r.logger().Warn("role source retention candidate queue failed", "error", err)
	}
	if r.Metrics != nil {
		r.Metrics.Queued.Add(float64(len(queued)))
	}
	for range roleSourceRetentionLimit {
		token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		candidate, err := r.Queries.ClaimNextRoleSourceRetentionCandidate(ctx, db.ClaimNextRoleSourceRetentionCandidateParams{
			LeaseToken: token, LeaseDuration: pgInterval(roleSourceRetentionLease),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				r.logger().Warn("role source retention candidate claim failed", "error", err)
			}
			break
		}
		outcome, err := r.Control.PruneRetentionCandidate(ctx, candidate.ID, token)
		if err == nil {
			if r.Metrics != nil {
				r.Metrics.RecordOutcome(outcome)
			}
			if outcome == "pruned" {
				r.logger().Info("role source historical snapshot pruned", "estimated_bytes", candidate.EstimatedBytes)
			}
			continue
		}
		if r.Metrics != nil {
			r.Metrics.Failures.Inc()
			r.Metrics.RecordOutcome("internal_failure")
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		delay := time.Minute << min(int(candidate.Attempt), 8)
		updated, releaseErr := r.Queries.ReleaseRoleSourceRetentionCandidate(writeCtx, db.ReleaseRoleSourceRetentionCandidateParams{
			RetryDelay: pgInterval(delay), ResultCode: pgtype.Text{String: "internal_failure", Valid: true},
			ID: candidate.ID, LeaseToken: token,
		})
		cancel()
		if ctx.Err() == nil {
			r.logger().Warn("role source retention prune failed", "error", err, "release_error", releaseErr, "released", updated)
		}
	}
	if r.Metrics != nil {
		if counts, err := r.Queries.CountRoleSourceRetentionCandidates(ctx); err == nil {
			r.Metrics.Backlog.Set(float64(counts.ActiveCount))
			r.Metrics.OldestActiveSeconds.Set(float64(counts.OldestActiveSeconds))
		}
	}
}
