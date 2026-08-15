package delivery

import (
	"context"
	"log/slog"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	reconcileInterval = time.Minute
	reconcileBatch    = int32(200)
)

// Reconciler freezes crash-abandoned pending claims as ambiguous. A retry is
// published only after a two-person receipt proves provider non-delivery; the
// normal expired-lease path itself never makes an unknown outcome retryable.
type Reconciler struct {
	Ledger         *Ledger
	RetryPublisher AuthorizedRetryPublisher
	Logger         *slog.Logger
	Metrics        ReconcileMetrics
}

type AuthorizedRetryPublisher interface {
	PublishAuthorizedRetry(context.Context, db.ChannelDelivery) error
}

type ReconcileMetrics interface {
	RecordChannelDeliveryReconcile(outcome string)
}

func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
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

func (r *Reconciler) RunOnce(ctx context.Context) {
	if r == nil || r.Ledger == nil {
		return
	}
	r.freezeExpired(ctx)
	if ctx.Err() == nil && r.RetryPublisher != nil {
		r.publishAuthorizedRetries(ctx)
	}
}

func (r *Reconciler) freezeExpired(ctx context.Context) {
	for {
		rows, err := r.Ledger.ClaimExpiredLeases(ctx, reconcileBatch)
		if err != nil {
			if r.Metrics != nil {
				r.Metrics.RecordChannelDeliveryReconcile("query_failed")
			}
			if ctx.Err() == nil {
				r.logger().WarnContext(ctx, "channel delivery reconciler: claim expired leases failed", "error", err)
			}
			return
		}
		writeFailed := false
		for _, row := range rows {
			recordCtx, cancel := detachedOutcomeContext(ctx)
			_, markErr := r.Ledger.MarkAmbiguous(recordCtx, Claim{Row: row, ShouldSend: true}, AmbiguityLeaseExpired, "")
			cancel()
			if markErr != nil {
				writeFailed = true
				if r.Metrics != nil {
					r.Metrics.RecordChannelDeliveryReconcile("write_failed")
				}
				if ctx.Err() == nil {
					r.logger().WarnContext(ctx, "channel delivery reconciler: freeze expired lease failed", "error", markErr)
				}
			}
		}
		if writeFailed {
			return
		}
		if r.Metrics != nil {
			r.Metrics.RecordChannelDeliveryReconcile("completed")
		}
		if len(rows) < int(reconcileBatch) {
			return
		}
	}
}

func (r *Reconciler) publishAuthorizedRetries(ctx context.Context) {
	for {
		rows, err := r.Ledger.ClaimAuthorizedRetries(ctx, reconcileBatch)
		if err != nil {
			r.record("retry_query_failed")
			if ctx.Err() == nil {
				r.logger().WarnContext(ctx, "channel delivery reconciler: claim authorized retries failed", "error", err)
			}
			return
		}
		for _, row := range rows {
			if err := r.RetryPublisher.PublishAuthorizedRetry(ctx, row); err != nil {
				r.record("retry_publish_failed")
				if ctx.Err() == nil {
					r.logger().WarnContext(ctx, "channel delivery reconciler: publish authorized retry failed", "delivery_id", row.ID, "error", err)
				}
				continue
			}
			current, err := r.Ledger.Get(ctx, row.ID)
			if err != nil || current.Status == "retry_authorized" {
				r.record("retry_unconsumed")
				if ctx.Err() == nil {
					r.logger().WarnContext(ctx, "channel delivery reconciler: authorized retry event was not consumed", "delivery_id", row.ID, "error", err)
				}
				continue
			}
			r.record("retry_published")
		}
		if len(rows) < int(reconcileBatch) {
			return
		}
	}
}

func (r *Reconciler) record(outcome string) {
	if r.Metrics != nil {
		r.Metrics.RecordChannelDeliveryReconcile(outcome)
	}
}

func (r *Reconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
