package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	roleSourceOutboxSweepInterval = time.Second
	roleSourceOutboxLease         = 30 * time.Second
	roleSourceOutboxBatch         = 100
	roleSourceOutboxCleanupBatch  = 500
	roleSourceOutboxCleanupPeriod = time.Hour
	roleSourceOutboxObservePeriod = 15 * time.Second
)

// RoleSourceOutboxDispatcher turns transactional apply events into durable
// realtime invalidations. Multiple API replicas may run it concurrently:
// SKIP LOCKED and a per-attempt lease distribute work while a stable event ID
// makes ambiguous publish retries harmless to connected clients.
type RoleSourceOutboxDispatcher struct {
	Queries     *db.Queries
	Broadcaster realtime.DurableBroadcaster
	Logger      *slog.Logger
	Metrics     *metrics.RoleSourceMetrics
	Interval    time.Duration
}

func (d *RoleSourceOutboxDispatcher) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

func (d *RoleSourceOutboxDispatcher) Run(ctx context.Context) {
	if d == nil || d.Queries == nil || d.Broadcaster == nil || ctx.Err() != nil {
		return
	}
	interval := d.Interval
	if interval <= 0 {
		interval = roleSourceOutboxSweepInterval
	}
	d.RunOnce(ctx)
	d.observe(ctx)
	d.cleanup(ctx)
	dispatchTicker := time.NewTicker(interval)
	observeTicker := time.NewTicker(roleSourceOutboxObservePeriod)
	cleanupTicker := time.NewTicker(roleSourceOutboxCleanupPeriod)
	defer dispatchTicker.Stop()
	defer observeTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-dispatchTicker.C:
			d.RunOnce(ctx)
		case <-observeTicker.C:
			d.observe(ctx)
		case <-cleanupTicker.C:
			d.cleanup(ctx)
		}
	}
}

func (d *RoleSourceOutboxDispatcher) RunOnce(ctx context.Context) {
	if d == nil || d.Queries == nil || d.Broadcaster == nil {
		return
	}
	for range roleSourceOutboxBatch {
		if ctx.Err() != nil {
			return
		}
		token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		row, err := d.Queries.ClaimNextRoleSourceOutboxEvent(ctx, db.ClaimNextRoleSourceOutboxEventParams{
			LeaseToken: token, LeaseDuration: pgInterval(roleSourceOutboxLease),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			d.recordOutbox("claim_failed")
			if ctx.Err() == nil {
				d.logger().Warn("role source outbox claim failed", "error", err)
			}
			break
		}
		if err := d.publish(row); err != nil {
			d.release(ctx, row, token, err)
			// A relay outage normally affects every row. Stop this pass after
			// the first failure so one 100-row batch cannot turn a two-second
			// publish timeout into minutes of dependency pressure.
			break
		}
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, err = d.Queries.MarkRoleSourceOutboxPublished(writeCtx, db.MarkRoleSourceOutboxPublishedParams{ID: row.ID, LeaseToken: token})
		cancel()
		if err != nil {
			// The relay may already have accepted the event. Leave the lease in
			// place; expiry retries with the same event ID and clients dedup it.
			d.recordOutbox("ack_failed")
			d.logger().Warn("role source outbox publish ack failed", "error", err, "attempt", row.Attempt)
			// PostgreSQL acknowledgement failures are likewise systemic. The
			// live lease preserves the ambiguous result for a stable-ID retry.
			break
		}
		d.recordOutbox("published")
	}

}

func (d *RoleSourceOutboxDispatcher) observe(ctx context.Context) {
	if _, err := d.Queries.MarkExhaustedRoleSourceOutboxEventsDead(ctx); err != nil {
		d.recordOutbox("release_failed")
		if ctx.Err() == nil {
			d.logger().Warn("role source outbox exhausted-lease reconciliation failed", "error", err)
		}
	}
	if counts, err := d.Queries.CountRoleSourceOutboxState(ctx); err == nil && d.Metrics != nil {
		d.Metrics.SetOutboxState(counts.ActiveCount, counts.DeadCount, counts.OldestActiveSeconds)
	}
}

func (d *RoleSourceOutboxDispatcher) cleanup(parent context.Context) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cleanupCancel()
	_, publishedErr := d.Queries.DeletePublishedRoleSourceOutboxEvents(cleanupCtx, roleSourceOutboxCleanupBatch)
	_, deadErr := d.Queries.DeleteDeadRoleSourceOutboxEvents(cleanupCtx, roleSourceOutboxCleanupBatch)
	if err := errors.Join(publishedErr, deadErr); err != nil && parent.Err() == nil {
		d.recordOutbox("cleanup_failed")
		d.logger().Warn("role source outbox cleanup failed", "error", err)
	}
}

func (d *RoleSourceOutboxDispatcher) publish(row db.RoleSourceOutbox) error {
	if row.EventType != protocol.EventRoleSourceApplied || !row.ActorID.Valid || row.ActorType != "user" {
		return errors.New("invalid durable role source event")
	}
	payload := map[string]any{
		"event_id":        util.UUIDToString(row.ID),
		"source_id":       util.UUIDToString(row.SourceID),
		"apply_id":        util.UUIDToString(row.ApplyID),
		"mode":            row.Mode,
		"snapshot_digest": row.SnapshotDigest,
		"plan_digest":     row.PlanDigest,
		"receipt_digest":  row.ReceiptDigest,
	}
	frame, err := json.Marshal(map[string]any{
		"type":       row.EventType,
		"payload":    payload,
		"actor_id":   util.UUIDToString(row.ActorID),
		"actor_type": row.ActorType,
	})
	if err != nil {
		return err
	}
	return d.Broadcaster.PublishDurable(
		realtime.ScopeWorkspace,
		util.UUIDToString(row.WorkspaceID),
		"",
		frame,
		util.UUIDToString(row.ID),
	)
}

func (d *RoleSourceOutboxDispatcher) release(parent context.Context, row db.RoleSourceOutbox, token pgtype.UUID, publishErr error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	delay := roleSourceOutboxRetryDelay(row.Attempt)
	released, err := d.Queries.ReleaseRoleSourceOutboxEvent(writeCtx, db.ReleaseRoleSourceOutboxEventParams{
		RetryDelay: pgInterval(delay), LastErrorCode: pgtype.Text{String: "publish_failed", Valid: true},
		ID: row.ID, LeaseToken: token,
	})
	if err != nil {
		d.recordOutbox("release_failed")
		d.logger().Warn("role source outbox publish release failed", "error", publishErr, "release_error", err, "attempt", row.Attempt)
		return
	}
	if released.Status == "dead" {
		d.recordOutbox("dead")
		d.logger().Error("role source outbox event exhausted retries", "attempt", released.Attempt, "event_type", released.EventType)
		return
	}
	d.recordOutbox("publish_failed")
	d.logger().Warn("role source outbox publish failed", "error", publishErr, "attempt", row.Attempt, "retry_in", delay)
}

func roleSourceOutboxRetryDelay(attempt int16) time.Duration {
	shift := max(int(attempt)-1, 0)
	shift = min(shift, 8)
	return 5 * time.Second * time.Duration(1<<shift)
}

func (d *RoleSourceOutboxDispatcher) recordOutbox(outcome string) {
	if d.Metrics != nil {
		d.Metrics.RecordOutboxDispatch(outcome)
	}
}
