package delivery

import (
	"context"
	"log/slog"
	"time"
)

const (
	reconcileInterval = time.Minute
	reconcileBatch    = int32(200)
)

// Reconciler freezes crash-abandoned pending claims as ambiguous. It never
// resends content and never makes an expired lease retryable: process death may
// have happened after provider acceptance but before the receipt was stored.
type Reconciler struct {
	Ledger  *Ledger
	Logger  *slog.Logger
	Metrics ReconcileMetrics
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

func (r *Reconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
