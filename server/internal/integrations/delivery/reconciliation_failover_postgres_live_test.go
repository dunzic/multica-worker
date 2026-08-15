package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const channelDeliveryFailoverWorkers = 8

// TestChannelDeliveryReconciliationPrimaryFailoverPostgres is coordinated by
// scripts/validation/rs07-postgres-failover.sh. The harness stops the primary
// before releasing the workers, waits until this process observes a real write
// failure, and only then promotes the physical standby. Every worker must
// recover through the stable database endpoint and converge on one receipt.
func TestChannelDeliveryReconciliationPrimaryFailoverPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_CHANNEL_DELIVERY_FAILOVER_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_CHANNEL_DELIVERY_FAILOVER_TEST=1")
	}
	signalDir := os.Getenv("MULTICA_CHANNEL_DELIVERY_FAILOVER_SIGNAL_DIR")
	if signalDir == "" {
		t.Fatal("MULTICA_CHANNEL_DELIVERY_FAILOVER_SIGNAL_DIR is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = channelDeliveryFailoverWorkers + 4
	poolConfig.HealthCheckPeriod = 250 * time.Millisecond
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	_, _, row := createAmbiguousDelivery(t, ctx, pool)
	t.Cleanup(func() { deleteReconciledPostgresDelivery(t, pool, row.ID) })
	executeInput := newFailoverReconciliationInput(t, ctx, pool, uuidText(row.ID))
	writeFailoverSignal(t, signalDir, "prepared", uuidText(row.ID))
	waitFailoverSignal(t, ctx, signalDir, "start")

	results := make(chan ReconciliationReceipt, channelDeliveryFailoverWorkers)
	workerErrors := make(chan error, channelDeliveryFailoverWorkers)
	signalErrors := make(chan error, 1)
	var attempts atomic.Int64
	var transientErrors atomic.Int64
	var firstErrorNanos atomic.Int64
	var firstSuccessNanos atomic.Int64
	var outageOnce sync.Once
	var workers sync.WaitGroup
	for range channelDeliveryFailoverWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			var lastErr error
			for ctx.Err() == nil {
				attempts.Add(1)
				attemptCtx, attemptCancel := context.WithTimeout(ctx, 5*time.Second)
				receipt, executeErr := ExecuteReconciliation(attemptCtx, pool, executeInput)
				attemptCancel()
				if executeErr == nil {
					firstSuccessNanos.CompareAndSwap(0, time.Now().UnixNano())
					results <- receipt
					return
				}
				lastErr = executeErr
				transientErrors.Add(1)
				firstErrorNanos.CompareAndSwap(0, time.Now().UnixNano())
				outageOnce.Do(func() {
					if writeErr := os.WriteFile(filepath.Join(signalDir, "outage_observed"), []byte("1"), 0o600); writeErr != nil {
						signalErrors <- writeErr
					}
				})
				select {
				case <-ctx.Done():
				case <-time.After(100 * time.Millisecond):
				}
			}
			workerErrors <- lastErr
		}()
	}
	workers.Wait()
	close(results)
	close(workerErrors)
	close(signalErrors)

	for signalErr := range signalErrors {
		if signalErr != nil {
			t.Fatalf("write outage signal: %v", signalErr)
		}
	}
	for workerErr := range workerErrors {
		if workerErr == nil {
			workerErr = ctx.Err()
		}
		t.Errorf("failover worker did not converge: %v", workerErr)
	}
	if transientErrors.Load() == 0 {
		t.Fatal("failover gate observed no database error before promotion")
	}

	var receiptDigest string
	successes := 0
	for receipt := range results {
		successes++
		if receiptDigest == "" {
			receiptDigest = receipt.ReconciliationDigest
		}
		if receipt.ReconciliationDigest != receiptDigest || receipt.Generation != 1 ||
			receipt.Outcome != ReconciliationConfirmedNotDelivered {
			t.Fatalf("non-convergent failover receipt=%+v", receipt)
		}
	}
	if successes != channelDeliveryFailoverWorkers {
		t.Fatalf("successful failover workers=%d, want %d", successes, channelDeliveryFailoverWorkers)
	}

	current, err := db.New(pool).GetChannelDeliveryByID(ctx, row.ID)
	if err != nil || current.Status != "retry_authorized" || current.ReconciliationCount != 1 {
		t.Fatalf("post-failover delivery=%+v err=%v", current, err)
	}
	var receiptCount, postgresMajor int
	var inRecovery bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_is_in_recovery(), current_setting('server_version_num')::int / 10000,
		       (SELECT count(*) FROM channel_delivery_reconciliation WHERE delivery_id=$1)
	`, row.ID).Scan(&inRecovery, &postgresMajor, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if inRecovery || postgresMajor != 17 || receiptCount != 1 {
		t.Fatalf("promoted topology in_recovery=%v postgres_major=%d receipts=%d", inRecovery, postgresMajor, receiptCount)
	}
	summary, err := InspectReconciliation(ctx, pool, uuidText(row.ID))
	if err != nil || summary.Status != "retry_authorized" || summary.ReconciliationCount != 1 ||
		summary.NextGeneration != 2 || summary.LatestOutcome != ReconciliationConfirmedNotDelivered {
		t.Fatalf("post-failover summary=%+v err=%v", summary, err)
	}

	firstError := time.Unix(0, firstErrorNanos.Load())
	firstSuccess := time.Unix(0, firstSuccessNanos.Load())
	recovery := firstSuccess.Sub(firstError)
	t.Logf("channel-delivery failover workers=%d attempts=%d transient_errors=%d recovery_to_first_success=%s receipt_digest=%s",
		channelDeliveryFailoverWorkers, attempts.Load(), transientErrors.Load(), recovery, receiptDigest)
	writeFailoverSignal(t, signalDir, "verified", receiptDigest)
	waitFailoverSignal(t, ctx, signalDir, "finish")
}

func newFailoverReconciliationInput(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID string) ReconciliationExecuteInput {
	t.Helper()
	requesterPublic, requesterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approverPublic, approverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectReconciliation(ctx, pool, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	authorization, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedNotDelivered,
		ReconciliationProviderNonDeliveryConfirmed, "sha256:"+repeatHex("f"), now)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalReconciliationAuthorization(authorization)
	if err != nil {
		t.Fatal(err)
	}
	return ReconciliationExecuteInput{
		Authorization: authorization, Now: now.Add(time.Second),
		PublicKeys: map[string]ed25519.PublicKey{"requester_failover": requesterPublic, "approver_failover": approverPublic},
		Requester:  ReconciliationSignature{KeyID: "requester_failover", Value: ed25519.Sign(requesterPrivate, canonical)},
		Approver:   ReconciliationSignature{KeyID: "approver_failover", Value: ed25519.Sign(approverPrivate, canonical)},
	}
}

func writeFailoverSignal(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFailoverSignal(t *testing.T, ctx context.Context, dir, name string) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for failover signal %q: %v", name, ctx.Err())
		case <-ticker.C:
		}
	}
}
