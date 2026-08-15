package delivery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestChannelDeliveryReconciliationPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_CHANNEL_DELIVERY_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_CHANNEL_DELIVERY_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	requesterPublic, requesterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approverPublic, approverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"requester_1": requesterPublic, "approver_1": approverPublic}

	t.Run("two-person non-delivery authorization is idempotent and consumed once", func(t *testing.T) {
		input, ledger, row := createAmbiguousDelivery(t, ctx, pool)
		t.Cleanup(func() { deleteReconciledPostgresDelivery(t, pool, row.ID) })
		summary, err := InspectReconciliation(ctx, pool, uuidText(row.ID))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		auth, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedNotDelivered,
			ReconciliationProviderNonDeliveryConfirmed, "sha256:"+repeatHex("b"), now)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalReconciliationAuthorization(auth)
		if err != nil {
			t.Fatal(err)
		}
		executeInput := ReconciliationExecuteInput{
			Authorization: auth, PublicKeys: keys, Now: now.Add(time.Second),
			Requester: ReconciliationSignature{KeyID: "requester_1", Value: ed25519.Sign(requesterPrivate, canonical)},
			Approver:  ReconciliationSignature{KeyID: "approver_1", Value: ed25519.Sign(approverPrivate, canonical)},
		}

		const workers = 16
		start := make(chan struct{})
		results := make(chan ReconciliationReceipt, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				receipt, executeErr := ExecuteReconciliation(ctx, pool, executeInput)
				if executeErr != nil {
					errs <- executeErr
					return
				}
				results <- receipt
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for executeErr := range errs {
			t.Errorf("concurrent reconciliation: %v", executeErr)
		}
		var digestValue string
		resultCount := 0
		for receipt := range results {
			resultCount++
			if digestValue == "" {
				digestValue = receipt.ReconciliationDigest
			}
			if receipt.ReconciliationDigest != digestValue || receipt.Generation != 1 {
				t.Fatalf("non-idempotent receipt=%+v", receipt)
			}
		}
		if resultCount != workers {
			t.Fatalf("successful idempotent executions=%d, want %d", resultCount, workers)
		}

		current, err := db.New(pool).GetChannelDeliveryByID(ctx, row.ID)
		if err != nil || current.Status != "retry_authorized" || current.ReconciliationCount != 1 {
			t.Fatalf("authorized row=%+v err=%v", current, err)
		}
		var receiptCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_delivery_reconciliation WHERE delivery_id=$1`, row.ID).Scan(&receiptCount); err != nil || receiptCount != 1 {
			t.Fatalf("receipt count=%d err=%v", receiptCount, err)
		}
		claimed, err := db.New(pool).ClaimAuthorizedChannelDeliveryRetries(ctx, 10)
		if err != nil || len(claimed) != 1 || claimed[0].ID != row.ID {
			t.Fatalf("publish claims=%+v err=%v", claimed, err)
		}
		retry, err := ledger.Claim(ctx, input)
		if err != nil || !retry.ShouldSend || retry.Row.AttemptCount != 2 || retry.Row.Status != "pending" {
			t.Fatalf("controlled retry=%+v err=%v", retry, err)
		}
		duplicate, err := ledger.Claim(ctx, input)
		if err != nil || duplicate.ShouldSend {
			t.Fatalf("duplicate controlled retry=%+v err=%v", duplicate, err)
		}
		if _, err := ledger.MarkDelivered(ctx, retry, "provider-after-reconciliation"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("confirmed delivery is terminal and immutable", func(t *testing.T) {
		input, ledger, row := createAmbiguousDelivery(t, ctx, pool)
		t.Cleanup(func() { deleteReconciledPostgresDelivery(t, pool, row.ID) })
		summary, err := InspectReconciliation(ctx, pool, uuidText(row.ID))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		auth, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedDelivered,
			ReconciliationProviderDeliveryConfirmed, "sha256:"+repeatHex("c"), now)
		if err != nil {
			t.Fatal(err)
		}
		canonical, _ := CanonicalReconciliationAuthorization(auth)
		receipt, err := ExecuteReconciliation(ctx, pool, ReconciliationExecuteInput{
			Authorization: auth, PublicKeys: keys, Now: now.Add(time.Second),
			Requester: ReconciliationSignature{KeyID: "requester_1", Value: ed25519.Sign(requesterPrivate, canonical)},
			Approver:  ReconciliationSignature{KeyID: "approver_1", Value: ed25519.Sign(approverPrivate, canonical)},
		})
		if err != nil || receipt.Outcome != ReconciliationConfirmedDelivered {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
		replay, err := ledger.Claim(ctx, input)
		if err != nil || replay.ShouldSend || replay.Row.Status != "reconciled" {
			t.Fatalf("terminal replay=%+v err=%v", replay, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE channel_delivery_reconciliation SET outcome='closed_no_retry' WHERE delivery_id=$1`, row.ID); err == nil {
			t.Fatal("immutable reconciliation receipt was updated")
		}
		if _, err := pool.Exec(ctx, `DELETE FROM channel_delivery_reconciliation WHERE delivery_id=$1`, row.ID); err == nil {
			t.Fatal("immutable reconciliation receipt was deleted")
		}
	})
}

func createAmbiguousDelivery(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (ClaimInput, *Ledger, db.ChannelDelivery) {
	t.Helper()
	input := postgresClaimInput()
	ledger := NewLedger(db.New(pool))
	claim, err := ledger.Claim(ctx, input)
	if err != nil || !claim.ShouldSend {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}
	row, err := ledger.MarkAmbiguous(ctx, claim, AmbiguityResponseUnknown, "")
	if err != nil {
		t.Fatal(err)
	}
	return input, ledger, row
}

func deleteReconciledPostgresDelivery(t *testing.T, pool *pgxpool.Pool, deliveryID pgtype.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT set_config('multica.workspace_teardown','on',true)`); err != nil {
		t.Error(err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_delivery_reconciliation WHERE delivery_id=$1`, deliveryID); err != nil {
		t.Error(err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_delivery WHERE id=$1`, deliveryID); err != nil {
		t.Error(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Error(err)
	}
}

func repeatHex(value string) string { return strings.Repeat(value, 64) }
