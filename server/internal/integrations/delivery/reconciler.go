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

// Reconciler turns crash-abandoned pending claims into explicit retryable
// failures. It never resends content: a later event/manual task retry must earn
// a fresh lease, which avoids guessing whether a provider accepted an
// ambiguous request.
type Reconciler struct {
	Ledger *Ledger
	Logger *slog.Logger
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
		rows, err := r.Ledger.ExpireLeases(ctx, reconcileBatch)
		if err != nil {
			if ctx.Err() == nil {
				r.logger().WarnContext(ctx, "channel delivery reconciler: expiry failed", "error", err)
			}
			return
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
