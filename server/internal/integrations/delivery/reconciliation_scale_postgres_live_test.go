package delivery

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	channelDeliveryScaleRows        = 10_000
	channelDeliveryScaleAuditRuns   = 200
	channelDeliveryScaleConcurrency = 16
	channelDeliveryScaleClaimers    = 8
	channelDeliveryScaleClaimBatch  = 200
)

// TestChannelDeliveryReconciliationScalePostgres is an opt-in, destructive
// staging gate. It seeds 10,000 cryptographically self-consistent authorized
// retry receipts, exercises the production audit validator, and then has eight
// worker replicas drain publish leases through the exact SKIP LOCKED query.
// It records a local baseline; it is not a production-provider latency claim.
func TestChannelDeliveryReconciliationScalePostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_CHANNEL_DELIVERY_SCALE_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_CHANNEL_DELIVERY_SCALE_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if poolConfig.MaxConns < channelDeliveryScaleConcurrency+2 {
		poolConfig.MaxConns = channelDeliveryScaleConcurrency + 2
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var postgresMajor int
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&postgresMajor); err != nil || postgresMajor != 17 {
		t.Fatalf("PostgreSQL major=%d err=%v", postgresMajor, err)
	}

	var databaseBytesBefore int64
	var walBefore string
	if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()), pg_current_wal_lsn()::text`).Scan(&databaseBytesBefore, &walBefore); err != nil {
		t.Fatal(err)
	}
	seedStarted := time.Now()
	fixture := seedChannelDeliveryScaleFixture(t, ctx, pool, channelDeliveryScaleRows)
	seedDuration := time.Since(seedStarted)
	t.Cleanup(func() { cleanupChannelDeliveryScaleFixture(t, pool, fixture.workspaceID) })
	if _, err := pool.Exec(ctx, `ANALYZE channel_delivery; ANALYZE channel_delivery_reconciliation`); err != nil {
		t.Fatal(err)
	}
	var databaseBytesAfter, walBytes int64
	if err := pool.QueryRow(ctx, `SELECT pg_database_size(current_database()), pg_wal_lsn_diff(pg_current_wal_lsn(), $1::pg_lsn)::bigint`, walBefore).Scan(&databaseBytesAfter, &walBytes); err != nil {
		t.Fatal(err)
	}

	ledger := NewLedger(db.New(pool))
	auditDurations, auditFailures := measureChannelDeliveryScaleAudits(ctx, ledger, fixture.workspaceID)
	if auditFailures != 0 {
		t.Fatalf("audit failures=%d", auditFailures)
	}
	auditP50 := durationQuantile(auditDurations, .50)
	auditP95 := durationQuantile(auditDurations, .95)
	auditP99 := durationQuantile(auditDurations, .99)
	if auditP99 >= 500*time.Millisecond {
		t.Fatalf("local bounded-audit p99=%s, want <500ms", auditP99)
	}

	plans := collectChannelDeliveryScalePlans(t, ctx, pool, fixture)
	for name, required := range map[string]string{
		"audit":    "idx_channel_delivery_workspace_listing",
		"receipts": "channel_delivery_reconciliation_generation_unique",
		"retry":    "idx_channel_delivery_retry_publish_due",
	} {
		if !strings.Contains(plans[name], required) {
			t.Fatalf("%s plan does not use %s:\n%s", name, required, plans[name])
		}
	}

	claimStarted := time.Now()
	claimed, duplicates, batchDurations := claimChannelDeliveryScaleBacklog(ctx, ledger)
	claimDuration := time.Since(claimStarted)
	if claimed != channelDeliveryScaleRows || duplicates != 0 {
		t.Fatalf("claimed=%d duplicates=%d, want %d/0", claimed, duplicates, channelDeliveryScaleRows)
	}
	if claimDuration >= 30*time.Second {
		t.Fatalf("local 10k publish-lease drain=%s, want <30s", claimDuration)
	}
	var leased int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_delivery WHERE workspace_id=$1 AND status='retry_authorized' AND retry_publish_token IS NOT NULL`, fixture.workspaceID).Scan(&leased); err != nil || leased != channelDeliveryScaleRows {
		t.Fatalf("leased rows=%d err=%v", leased, err)
	}
	records, err := ledger.ListRecords(ctx, fixture.workspaceID, 100)
	if err != nil || len(records) != 100 {
		t.Fatalf("post-drain audit records=%d err=%v", len(records), err)
	}

	t.Logf("channel-delivery scale rows=%d seed=%s db_growth=%d wal=%d audit_samples=%d audit_p50=%s audit_p95=%s audit_p99=%s claim_workers=%d claim_batches=%d claim_p95=%s claim_total=%s rows_per_second=%.1f",
		channelDeliveryScaleRows, seedDuration, databaseBytesAfter-databaseBytesBefore, walBytes,
		len(auditDurations), auditP50, auditP95, auditP99, channelDeliveryScaleClaimers,
		len(batchDurations), durationQuantile(batchDurations, .95), claimDuration,
		float64(channelDeliveryScaleRows)/claimDuration.Seconds())
}

type channelDeliveryScaleFixture struct {
	workspaceID pgtype.UUID
	deliveryIDs []pgtype.UUID
}

func seedChannelDeliveryScaleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) channelDeliveryScaleFixture {
	t.Helper()
	fixture := channelDeliveryScaleFixture{workspaceID: postgresUUID(), deliveryIDs: make([]pgtype.UUID, 0, count)}
	installationID := postgresUUID()
	baseTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	payloadDigest := PayloadDigest("content-free scale payload")
	constantDigest := "sha256:" + repeatHex("e")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	const copyBatch = 1_000
	for start := 0; start < count; start += copyBatch {
		end := min(start+copyBatch, count)
		deliveryRows := make([][]any, 0, end-start)
		receiptRows := make([][]any, 0, end-start)
		for index := start; index < end; index++ {
			deliveryID := postgresUUID()
			taskID := postgresUUID()
			sessionID := postgresUUID()
			correlationID := postgresUUID()
			reconciliationID := postgresUUID()
			authorizationID := postgresUUID()
			createdAt := baseTime.Add(time.Duration(index) * time.Microsecond)
			evidence := Evidence{
				ContractVersion: AmbiguityEvidenceContractVersion,
				DeliveryID:      uuidText(deliveryID), CorrelationID: uuidText(correlationID),
				WorkspaceID: uuidText(fixture.workspaceID), TaskID: uuidText(taskID), ChatSessionID: uuidText(sessionID),
				ChannelType: "slack", ChannelChatID: "scale-channel", OperationKind: OperationChatReply,
				PayloadDigest: payloadDigest, Status: "ambiguous", AttemptCount: 1,
				AmbiguityReason: AmbiguityResponseUnknown, AmbiguousAt: createdAt.Format(time.RFC3339Nano),
			}
			evidenceBody, evidenceDigest, err := encodeEvidence(evidence)
			if err != nil {
				t.Fatal(err)
			}
			receipt := ReconciliationReceipt{
				ContractVersion:  ReconciliationReceiptContractVersion,
				ReconciliationID: uuidText(reconciliationID), DeliveryID: uuidText(deliveryID),
				WorkspaceID: uuidText(fixture.workspaceID), AuthorizationID: uuidText(authorizationID),
				Generation: 1, Outcome: ReconciliationConfirmedNotDelivered, ReasonCode: ReconciliationProviderNonDeliveryConfirmed,
				ExternalEvidenceDigest: constantDigest, ExpectedAmbiguityEvidenceDigest: evidenceDigest,
				AmbiguityEvidence: evidence, RequesterKeyID: "requester_scale", ApproverKeyID: "approver_scale",
				AuthorizationDigest: constantDigest, RequesterSignatureDigest: constantDigest,
				ApproverSignatureDigest: constantDigest, CreatedAt: createdAt,
			}
			receipt.ReconciliationDigest, err = digestReconciliationReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			deliveryRows = append(deliveryRows, []any{
				deliveryID, fixture.workspaceID, installationID, taskID, sessionID, "slack", "scale-channel",
				OperationChatReply, correlationID, payloadDigest, "retry_authorized", int32(1), evidenceBody,
				evidenceDigest, AmbiguityResponseUnknown, createdAt, int16(1), createdAt, createdAt, createdAt,
			})
			receiptRows = append(receiptRows, []any{
				reconciliationID, deliveryID, fixture.workspaceID, authorizationID, int16(1),
				receipt.Outcome, receipt.ReasonCode, receipt.ExternalEvidenceDigest,
				receipt.ExpectedAmbiguityEvidenceDigest, evidenceBody, receipt.RequesterKeyID, receipt.ApproverKeyID,
				receipt.AuthorizationDigest, receipt.RequesterSignatureDigest, receipt.ApproverSignatureDigest,
				nil, receipt.ReconciliationDigest, createdAt,
			})
			fixture.deliveryIDs = append(fixture.deliveryIDs, deliveryID)
		}
		if inserted, err := tx.CopyFrom(ctx, pgx.Identifier{"channel_delivery"}, []string{
			"id", "workspace_id", "installation_id", "task_id", "chat_session_id", "channel_type", "channel_chat_id",
			"operation_kind", "correlation_id", "payload_digest", "status", "attempt_count", "evidence",
			"evidence_digest", "last_error_code", "ambiguous_at", "reconciliation_count", "last_reconciled_at",
			"created_at", "updated_at",
		}, pgx.CopyFromRows(deliveryRows)); err != nil || inserted != int64(len(deliveryRows)) {
			t.Fatalf("copy delivery rows=%d want=%d err=%v", inserted, len(deliveryRows), err)
		}
		if inserted, err := tx.CopyFrom(ctx, pgx.Identifier{"channel_delivery_reconciliation"}, []string{
			"id", "delivery_id", "workspace_id", "authorization_id", "generation", "outcome", "reason_code",
			"external_evidence_digest", "expected_ambiguity_evidence_digest", "ambiguity_evidence",
			"requester_key_id", "approver_key_id", "authorization_digest", "requester_signature_digest",
			"approver_signature_digest", "previous_reconciliation_digest", "reconciliation_digest", "created_at",
		}, pgx.CopyFromRows(receiptRows)); err != nil || inserted != int64(len(receiptRows)) {
			t.Fatalf("copy reconciliation rows=%d want=%d err=%v", inserted, len(receiptRows), err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func measureChannelDeliveryScaleAudits(ctx context.Context, ledger *Ledger, workspaceID pgtype.UUID) ([]time.Duration, int) {
	durations := make([]time.Duration, channelDeliveryScaleAuditRuns)
	jobs := make(chan int)
	var failures atomic.Int32
	var workers sync.WaitGroup
	for range channelDeliveryScaleConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				started := time.Now()
				records, err := ledger.ListRecords(ctx, workspaceID, 100)
				durations[index] = time.Since(started)
				if err != nil || len(records) != 100 {
					failures.Add(1)
				}
			}
		}()
	}
	for index := range channelDeliveryScaleAuditRuns {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return durations, int(failures.Load())
}

func collectChannelDeliveryScalePlans(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture channelDeliveryScaleFixture) map[string]string {
	t.Helper()
	probes := map[string]struct {
		query string
		args  []any
	}{
		"audit": {
			query: `SELECT * FROM channel_delivery WHERE workspace_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100`,
			args:  []any{fixture.workspaceID},
		},
		"receipts": {
			query: `SELECT * FROM channel_delivery_reconciliation WHERE workspace_id=$1 AND delivery_id=ANY($2::uuid[]) ORDER BY delivery_id,generation`,
			args:  []any{fixture.workspaceID, fixture.deliveryIDs[:100]},
		},
		"retry": {
			query: `SELECT id FROM channel_delivery WHERE status='retry_authorized' AND (retry_publish_expires_at IS NULL OR retry_publish_expires_at < now()) ORDER BY last_reconciled_at,id FOR UPDATE SKIP LOCKED LIMIT 200`,
		},
	}
	result := make(map[string]string, len(probes))
	for name, probe := range probes {
		rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT TEXT) "+probe.query, probe.args...)
		if err != nil {
			t.Fatalf("explain %s: %v", name, err)
		}
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		result[name] = strings.Join(lines, "\n")
	}
	return result
}

func claimChannelDeliveryScaleBacklog(ctx context.Context, ledger *Ledger) (int, int, []time.Duration) {
	var claimed atomic.Int64
	var duplicates atomic.Int64
	seen := sync.Map{}
	var durationMu sync.Mutex
	var durations []time.Duration
	var workers sync.WaitGroup
	for range channelDeliveryScaleClaimers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				started := time.Now()
				rows, err := ledger.ClaimAuthorizedRetries(ctx, channelDeliveryScaleClaimBatch)
				duration := time.Since(started)
				durationMu.Lock()
				durations = append(durations, duration)
				durationMu.Unlock()
				if err != nil {
					duplicates.Add(channelDeliveryScaleRows)
					return
				}
				for _, row := range rows {
					if _, loaded := seen.LoadOrStore(uuidText(row.ID), struct{}{}); loaded {
						duplicates.Add(1)
					} else {
						claimed.Add(1)
					}
				}
				if len(rows) < channelDeliveryScaleClaimBatch {
					return
				}
			}
		}()
	}
	workers.Wait()
	return int(claimed.Load()), int(duplicates.Load()), durations
}

func durationQuantile(values []time.Duration, quantile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	index := int(float64(len(ordered)-1) * quantile)
	return ordered[index]
}

func cleanupChannelDeliveryScaleFixture(t *testing.T, pool *pgxpool.Pool, workspaceID pgtype.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	if _, err := tx.Exec(ctx, `DELETE FROM channel_delivery_reconciliation WHERE workspace_id=$1`, workspaceID); err != nil {
		t.Error(err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM channel_delivery WHERE workspace_id=$1`, workspaceID); err != nil {
		t.Error(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Error(err)
		return
	}
	var residue int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channel_delivery WHERE workspace_id=$1)+(SELECT count(*) FROM channel_delivery_reconciliation WHERE workspace_id=$1)`, workspaceID).Scan(&residue); err != nil || residue != 0 {
		t.Errorf("scale fixture residue=%d err=%v", residue, err)
	}
}
