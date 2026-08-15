package delivery

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestChannelDeliveryAmbiguityPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_CHANNEL_DELIVERY_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_CHANNEL_DELIVERY_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	t.Run("concurrent claim and unknown response freeze", func(t *testing.T) {
		input := postgresClaimInput()
		t.Cleanup(func() { deletePostgresDelivery(t, pool, input.InstallationID) })
		ledger := NewLedger(db.New(pool))

		const workers = 32
		start := make(chan struct{})
		claims := make(chan Claim, workers)
		errs := make(chan error, workers)
		var senders atomic.Int32
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				claim, claimErr := ledger.Claim(ctx, input)
				if claimErr != nil {
					errs <- claimErr
					return
				}
				if claim.ShouldSend {
					senders.Add(1)
					claims <- claim
				}
			}()
		}
		close(start)
		wg.Wait()
		close(claims)
		close(errs)
		for claimErr := range errs {
			t.Errorf("concurrent claim: %v", claimErr)
		}
		if senders.Load() != 1 {
			t.Fatalf("provider senders=%d, want 1", senders.Load())
		}
		winner := <-claims
		row, err := ledger.MarkAmbiguous(ctx, winner, AmbiguityResponseUnknown, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateRow(row); err != nil {
			t.Fatalf("stored ambiguity evidence: %v", err)
		}

		for range workers {
			if _, err := ledger.Claim(ctx, input); !errors.Is(err, ErrAmbiguous) {
				t.Fatalf("frozen replay error=%v", err)
			}
		}
		var count int64
		var attempts int32
		var status string
		if err := pool.QueryRow(ctx, `SELECT count(*), min(status), min(attempt_count) FROM channel_delivery WHERE installation_id=$1`, input.InstallationID).Scan(&count, &status, &attempts); err != nil {
			t.Fatal(err)
		}
		if count != 1 || status != "ambiguous" || attempts != 1 {
			t.Fatalf("count=%d status=%s attempts=%d", count, status, attempts)
		}
	})

	t.Run("expired pending lease cannot be reclaimed by event", func(t *testing.T) {
		input := postgresClaimInput()
		t.Cleanup(func() { deletePostgresDelivery(t, pool, input.InstallationID) })
		ledger := NewLedger(db.New(pool))
		claim, err := ledger.Claim(ctx, input)
		if err != nil || !claim.ShouldSend {
			t.Fatalf("initial claim=%+v err=%v", claim, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE channel_delivery SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, claim.Row.ID); err != nil {
			t.Fatal(err)
		}
		for range 16 {
			replay, err := ledger.Claim(ctx, input)
			if err != nil || replay.ShouldSend || replay.Row.AttemptCount != 1 {
				t.Fatalf("expired replay=%+v err=%v", replay, err)
			}
		}
		(&Reconciler{Ledger: ledger}).RunOnce(ctx)
		row, err := db.New(pool).GetChannelDeliveryByIdentity(ctx, db.GetChannelDeliveryByIdentityParams{
			InstallationID: input.InstallationID, TaskID: input.TaskID, OperationKind: input.OperationKind,
		})
		if err != nil || row.Status != "ambiguous" || row.LastErrorCode.String != AmbiguityLeaseExpired {
			t.Fatalf("reconciled row=%+v err=%v", row, err)
		}
		if _, err := ValidateRow(row); err != nil {
			t.Fatalf("lease ambiguity evidence: %v", err)
		}
	})

	t.Run("database rejects incomplete ambiguity row", func(t *testing.T) {
		input := postgresClaimInput()
		_, err := pool.Exec(ctx, `
			INSERT INTO channel_delivery (
				workspace_id,installation_id,task_id,chat_session_id,channel_type,channel_chat_id,
				operation_kind,payload_digest,status,last_error_code,ambiguous_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'ambiguous',$9,now())`,
			input.WorkspaceID, input.InstallationID, input.TaskID, input.ChatSessionID, input.ChannelType,
			input.ChannelChatID, input.OperationKind, PayloadDigest(input.Payload), AmbiguityResponseUnknown)
		if err == nil {
			deletePostgresDelivery(t, pool, input.InstallationID)
			t.Fatal("incomplete ambiguous row was accepted")
		}
	})
}

func postgresClaimInput() ClaimInput {
	return ClaimInput{
		WorkspaceID: postgresUUID(), InstallationID: postgresUUID(), TaskID: postgresUUID(), ChatSessionID: postgresUUID(),
		ChannelType: channel.Type("slack"), ChannelChatID: "C-live-gate", OperationKind: OperationChatReply, Payload: "content-free live gate",
	}
}

func postgresUUID() pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(uuid.NewString()); err != nil {
		panic(err)
	}
	return id
}

func deletePostgresDelivery(t *testing.T, pool *pgxpool.Pool, installationID pgtype.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM channel_delivery WHERE installation_id=$1`, installationID); err != nil {
		t.Errorf("cleanup channel delivery: %v", err)
	}
}
