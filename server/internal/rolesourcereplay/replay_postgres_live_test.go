package rolesourcereplay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/rolesource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRoleSourceOutboxReplayPostgresStateMachine(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_ROLE_SOURCE_REPLAY_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_REPLAY_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fixture := createReplayFixture(t, ctx, pool)
	defer cleanupReplayFixture(t, pool, fixture.workspaceID)

	summary, err := Inspect(ctx, pool, fixture.outboxID.String())
	if err != nil || summary.Status != "dead" || summary.NextGeneration != 1 || summary.ExpectedReceiptDigest != fixture.receiptDigest {
		t.Fatalf("inspect=%+v err=%v", summary, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET attempt=19 WHERE id=$1`, fixture.outboxID); err == nil {
		t.Fatal("database accepted a dead outbox event before terminal attempt 20")
	}
	wrongActorID := uuid.New()
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET actor_id=$2 WHERE id=$1`, fixture.outboxID, wrongActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, pool, fixture.outboxID.String()); err == nil {
		t.Fatal("inspect accepted an outbox actor that mismatched the successful apply")
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET actor_id=$2 WHERE id=$1`, fixture.outboxID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	badDigest := "sha256:" + strings.Repeat("f", 64)
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET plan_digest=$2 WHERE id=$1`, fixture.outboxID, badDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, pool, fixture.outboxID.String()); err == nil {
		t.Fatal("inspect accepted an outbox commitment that mismatched the receipt")
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET plan_digest=$2 WHERE id=$1`, fixture.outboxID, fixture.planDigest); err != nil {
		t.Fatal(err)
	}

	publicA, privateA, _ := ed25519.GenerateKey(rand.Reader)
	publicB, privateB, _ := ed25519.GenerateKey(rand.Reader)
	keys := map[string]ed25519.PublicKey{"requester_1": publicA, "approver_1": publicB}
	for generation := int16(1); generation <= 3; generation++ {
		summary, err = Inspect(ctx, pool, fixture.outboxID.String())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		auth, err := NewAuthorization(summary, "dependency_recovered", "sha256:"+strings.Repeat("e", 64), now)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := CanonicalAuthorization(auth)
		input := ExecuteInput{Authorization: auth, PublicKeys: keys, Now: now,
			Requester: Signature{KeyID: "requester_1", Value: ed25519.Sign(privateA, body)},
			Approver:  Signature{KeyID: "approver_1", Value: ed25519.Sign(privateB, body)},
		}
		if generation == 1 {
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, executeErr := Execute(context.Background(), pool, input)
					results <- executeErr
				}()
			}
			wg.Wait()
			close(results)
			for executeErr := range results {
				if executeErr != nil {
					t.Fatalf("exact concurrent authorization did not reconcile idempotently: %v", executeErr)
				}
			}
		} else if _, err := Execute(ctx, pool, input); err != nil {
			t.Fatal(err)
		}
		if existing, err := Execute(ctx, pool, input); err != nil || existing.Generation != generation {
			t.Fatalf("idempotent authorization retry generation=%d receipt=%+v err=%v", generation, existing, err)
		}
		expiredRetry := input
		expiredRetry.Now = auth.ExpiresAt.Add(time.Hour)
		if existing, err := Execute(ctx, pool, expiredRetry); err != nil || existing.Generation != generation {
			t.Fatalf("expired idempotent retry generation=%d receipt=%+v err=%v", generation, existing, err)
		}
		conflict := input
		conflict.Requester.Value = append([]byte(nil), input.Requester.Value...)
		conflict.Requester.Value[0] ^= 0xff
		if _, err := Execute(ctx, pool, conflict); err == nil {
			t.Fatal("conflicting retry for consumed authorization was accepted")
		}
		var status string
		var replayCount int16
		if err := pool.QueryRow(ctx, `SELECT status, replay_count FROM role_source_outbox WHERE id=$1`, fixture.outboxID).Scan(&status, &replayCount); err != nil || status != "pending" || replayCount != generation {
			t.Fatalf("generation %d status=%s replay_count=%d err=%v", generation, status, replayCount, err)
		}
		if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET status='dead', attempt=20, last_error_code='publish_failed' WHERE id=$1`, fixture.outboxID); err != nil {
			t.Fatal(err)
		}
		persisted, err := db.New(pool).GetLatestRoleSourceOutboxReplay(ctx, pgUUID(fixture.outboxID))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodePersistedReplayReceipt(persisted)
		if err != nil || decoded.Generation != generation {
			t.Fatalf("generation %d persisted receipt=%+v err=%v", generation, decoded, err)
		}
	}
	if summary, err := Inspect(ctx, pool, fixture.outboxID.String()); err != nil || summary.NextGeneration != 4 {
		t.Fatalf("final inspect=%+v err=%v", summary, err)
	} else if _, err := NewAuthorization(summary, "dependency_recovered", "sha256:"+strings.Repeat("e", 64), time.Now()); !errors.Is(err, ErrNotReplayable) {
		t.Fatalf("fourth generation error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox_replay SET reason_code='incident_recovery' WHERE outbox_id=$1`, fixture.outboxID); err == nil {
		t.Fatal("immutable replay receipt update succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM role_source_outbox_replay WHERE outbox_id=$1`, fixture.outboxID); err == nil {
		t.Fatal("replay receipt delete succeeded outside teardown")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qtx := db.New(tx)
	if err := qtx.SetWorkspaceTeardownMode(ctx); err != nil {
		t.Fatal(err)
	}
	if err := qtx.DeleteWorkspaceRoleSources(ctx, pgUUID(fixture.workspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var remains int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM role_source_outbox_replay WHERE workspace_id=$1`, fixture.workspaceID).Scan(&remains); err != nil || remains != 0 {
		t.Fatalf("workspace teardown left replay receipts=%d err=%v", remains, err)
	}
}

type replayFixture struct {
	workspaceID, sourceID, applyID, outboxID, actorID uuid.UUID
	receiptDigest, planDigest                         string
}

func createReplayFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) replayFixture {
	t.Helper()
	fixture := replayFixture{workspaceID: uuid.New(), sourceID: uuid.New(), applyID: uuid.New(), outboxID: uuid.New(), planDigest: testDigest("plan")}
	fixture.actorID = uuid.New()
	runtimeID := uuid.New()
	snapshotDigest := testDigest("snapshot")
	if _, err := pool.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Replay test',$2)`, fixture.workspaceID, "replay-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO role_source (id,workspace_id,runtime_id,name,kind,adapter_version,daemon_config_id,config_redacted,policy,state,current_snapshot_digest,audit_sequence,created_by,updated_by) VALUES ($1,$2,$3,'Replay source','agentwaker','1.0','local', '{}'::jsonb, '{}'::jsonb, 'active',$4,1,$5,$5)`, fixture.sourceID, fixture.workspaceID, runtimeID, snapshotDigest, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	receipt := rolesource.ApplyReceipt{ContractVersion: rolesource.ApplyReceiptContractVersion, Mode: "apply", ApplyID: fixture.applyID.String(), SourceID: fixture.sourceID.String(), WorkspaceID: fixture.workspaceID.String(), SnapshotDigest: snapshotDigest, PlanDigest: fixture.planDigest, ApprovalID: uuid.NewString(), Mappings: []rolesource.ApplyMapping{}, SecretTransfers: []rolesource.SecretTransferReceipt{}}
	canonical := receipt
	canonical.ReceiptDigest = ""
	canonicalBody, _ := json.Marshal(canonical)
	fixture.receiptDigest = testDigestBytes(canonicalBody)
	receipt.ReceiptDigest = fixture.receiptDigest
	receiptBody, _ := json.Marshal(receipt)
	if _, err := pool.Exec(ctx, `INSERT INTO role_source_apply (id,source_id,workspace_id,request_key,mode,snapshot_digest,plan_digest,status,actor_user_id,receipt_digest,receipt,completed_at) VALUES ($1,$2,$3,$4,'apply',$5,$6,'succeeded',$7,$8,$9,now())`, fixture.applyID, fixture.sourceID, fixture.workspaceID, uuid.NewString(), snapshotDigest, fixture.planDigest, fixture.actorID, fixture.receiptDigest, receiptBody); err != nil {
		t.Fatal(err)
	}
	event, err := rolesource.BuildAuditEvent(fixture.sourceID.String(), fixture.workspaceID.String(), 1, "apply_succeeded", rolesource.AuditActor{Type: "user", ID: fixture.actorID.String()}, "", rolesource.AuditPayload{OperationID: fixture.applyID.String(), SnapshotDigest: snapshotDigest, PlanDigest: fixture.planDigest, ReceiptDigest: fixture.receiptDigest, Result: "succeeded"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(event.Payload)
	q := db.New(pool)
	if _, err := q.InsertRoleSourceAuditEvent(ctx, db.InsertRoleSourceAuditEventParams{ID: pgUUID(uuid.New()), SourceID: pgUUID(fixture.sourceID), WorkspaceID: pgUUID(fixture.workspaceID), Sequence: 1, EventType: event.EventType, ActorType: "user", ActorID: pgUUID(fixture.actorID), EventDigest: event.EventDigest, Payload: payload, OccurredAt: pgtype.Timestamptz{Time: event.OccurredAt, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertRoleSourceOutboxEvent(ctx, db.InsertRoleSourceOutboxEventParams{ID: pgUUID(fixture.outboxID), WorkspaceID: pgUUID(fixture.workspaceID), SourceID: pgUUID(fixture.sourceID), EventType: "role_source:applied", ActorType: "user", ActorID: pgUUID(fixture.actorID), ApplyID: pgUUID(fixture.applyID), Mode: "apply", SnapshotDigest: snapshotDigest, PlanDigest: fixture.planDigest, ReceiptDigest: fixture.receiptDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE role_source_outbox SET status='dead', attempt=20, last_error_code='publish_failed' WHERE id=$1`, fixture.outboxID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupReplayFixture(t *testing.T, pool *pgxpool.Pool, workspaceID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Log(err)
		return
	}
	_, _ = tx.Exec(ctx, `SELECT set_config('multica.workspace_teardown','on',true)`)
	for _, table := range []string{"role_source_outbox_replay", "role_source_outbox", "role_source_audit_event", "role_source_apply", "role_source"} {
		_, _ = tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1`, workspaceID)
	}
	_, _ = tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspaceID)
	_ = tx.Commit(ctx)
}

func pgUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func testDigest(value string) string { return testDigestBytes([]byte(value)) }
func testDigestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
