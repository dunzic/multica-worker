package delivery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const processProbePayload = "content-free process-kill gate"

// TestChannelDeliveryReconciliationProcessKillPostgres proves the two lease
// boundaries with distinct OS processes, not only goroutines:
//
//  1. replica A dies after claiming the retry-publication lease;
//  2. replica B reclaims it, consumes the signed authorization and dies after
//     claiming the provider-send lease;
//  3. replica C freezes that abandoned pending send as the next ambiguity.
//
// The test advances expired timestamps directly instead of sleeping through
// two production 30-second leases. No provider call is made.
func TestChannelDeliveryReconciliationProcessKillPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_CHANNEL_DELIVERY_PROCESS_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_CHANNEL_DELIVERY_PROCESS_TEST=1")
	}
	if runtime.GOOS == "windows" {
		t.Skip("process-kill lease probe currently requires Unix process signals")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	input := postgresClaimInput()
	input.Payload = processProbePayload
	ledger := NewLedger(db.New(pool))
	claim, err := ledger.Claim(ctx, input)
	if err != nil || !claim.ShouldSend {
		t.Fatalf("initial claim=%+v err=%v", claim, err)
	}
	row, err := ledger.MarkAmbiguous(ctx, claim, AmbiguityResponseUnknown, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteReconciledPostgresDelivery(t, pool, row.ID) })
	authorizeProcessProbeRetry(t, ctx, pool, row.ID)

	signalDir := t.TempDir()
	replicaA := startProcessProbe(t, "claim_publish_and_block", row.ID, filepath.Join(signalDir, "replica-a"))
	waitProcessSignal(t, replicaA, "publish_claimed")
	assertProcessProbeStatus(t, ctx, pool, row.ID, "retry_authorized", 1, true, false)
	killProcessProbe(t, replicaA)
	if _, err := pool.Exec(ctx, `UPDATE channel_delivery SET retry_publish_expires_at=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}

	replicaB := startProcessProbe(t, "reclaim_consume_and_block", row.ID, filepath.Join(signalDir, "replica-b"))
	waitProcessSignal(t, replicaB, "delivery_claimed")
	assertProcessProbeStatus(t, ctx, pool, row.ID, "pending", 2, false, true)
	killProcessProbe(t, replicaB)
	if _, err := pool.Exec(ctx, `UPDATE channel_delivery SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}

	replicaC := startProcessProbe(t, "freeze_expired_and_exit", row.ID, filepath.Join(signalDir, "replica-c"))
	waitProcessSignal(t, replicaC, "frozen")
	waitProcessProbeExit(t, replicaC)
	current, err := db.New(pool).GetChannelDeliveryByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "ambiguous" || current.AttemptCount != 2 || current.ReconciliationCount != 1 ||
		current.LastErrorCode.String != AmbiguityLeaseExpired || current.LeaseToken.Valid || current.RetryPublishToken.Valid {
		t.Fatalf("post-kill row=%+v", current)
	}
	if _, err := ValidateRow(current); err != nil {
		t.Fatalf("post-kill ambiguity evidence=%v", err)
	}
	summary, err := InspectReconciliation(ctx, pool, uuidText(row.ID))
	if err != nil || summary.Status != "ambiguous" || summary.NextGeneration != 2 || summary.LatestOutcome != ReconciliationConfirmedNotDelivered {
		t.Fatalf("post-kill reconciliation summary=%+v err=%v", summary, err)
	}
	if _, err := ledger.Claim(ctx, input); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("abandoned controlled send was event-reclaimed: %v", err)
	}
}

// TestChannelDeliveryReconciliationProcessHelper is invoked as a child test
// binary by the process-kill gate above. It is never active in an ordinary
// package test run.
func TestChannelDeliveryReconciliationProcessHelper(t *testing.T) {
	role := os.Getenv("MULTICA_CHANNEL_DELIVERY_PROCESS_ROLE")
	if role == "" {
		t.Skip("process helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	deliveryID, err := pgUUIDFromText(os.Getenv("MULTICA_CHANNEL_DELIVERY_PROCESS_ID"))
	if err != nil {
		t.Fatal(err)
	}
	signalPath := os.Getenv("MULTICA_CHANNEL_DELIVERY_PROCESS_SIGNAL")
	if signalPath == "" {
		t.Fatal("process signal path is required")
	}
	queries := db.New(pool)

	switch role {
	case "claim_publish_and_block":
		rows, err := queries.ClaimAuthorizedChannelDeliveryRetries(ctx, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != deliveryID {
			t.Fatalf("claim retry publish lease rows=%+v err=%v", rows, err)
		}
		writeProcessSignal(t, signalPath, "publish_claimed")
		select {}
	case "reclaim_consume_and_block":
		rows, err := queries.ClaimAuthorizedChannelDeliveryRetries(ctx, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != deliveryID {
			t.Fatalf("reclaim retry publish lease rows=%+v err=%v", rows, err)
		}
		row := rows[0]
		claim, err := NewLedger(queries).Claim(ctx, ClaimInput{
			WorkspaceID: row.WorkspaceID, InstallationID: row.InstallationID, TaskID: row.TaskID,
			ChatSessionID: row.ChatSessionID, ChannelType: channel.Type(row.ChannelType),
			ChannelChatID: row.ChannelChatID, OperationKind: row.OperationKind, Payload: processProbePayload,
		})
		if err != nil || !claim.ShouldSend {
			t.Fatalf("consume authorized retry claim=%+v err=%v", claim, err)
		}
		writeProcessSignal(t, signalPath, "delivery_claimed")
		select {}
	case "freeze_expired_and_exit":
		ledger := NewLedger(queries)
		(&Reconciler{Ledger: ledger}).RunOnce(ctx)
		row, err := queries.GetChannelDeliveryByID(ctx, deliveryID)
		if err != nil || row.Status != "ambiguous" || row.LastErrorCode.String != AmbiguityLeaseExpired {
			t.Fatalf("freeze expired row=%+v err=%v", row, err)
		}
		writeProcessSignal(t, signalPath, "frozen")
	default:
		t.Fatalf("unknown process helper role %q", role)
	}
}

func authorizeProcessProbeRetry(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID pgtype.UUID) {
	t.Helper()
	requesterPublic, requesterPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	approverPublic, approverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectReconciliation(ctx, pool, uuidText(deliveryID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	auth, err := NewReconciliationAuthorization(summary, ReconciliationConfirmedNotDelivered,
		ReconciliationProviderNonDeliveryConfirmed, "sha256:"+repeatHex("d"), now)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalReconciliationAuthorization(auth)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteReconciliation(ctx, pool, ReconciliationExecuteInput{
		Authorization: auth, Now: now.Add(time.Second),
		PublicKeys: map[string]ed25519.PublicKey{"requester_1": requesterPublic, "approver_1": approverPublic},
		Requester:  ReconciliationSignature{KeyID: "requester_1", Value: ed25519.Sign(requesterPrivate, canonical)},
		Approver:   ReconciliationSignature{KeyID: "approver_1", Value: ed25519.Sign(approverPrivate, canonical)},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type processProbe struct {
	cmd        *exec.Cmd
	signalPath string
	output     *bytes.Buffer
	done       chan error
}

func startProcessProbe(t *testing.T, role string, deliveryID pgtype.UUID, signalPath string) *processProbe {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := &processProbe{signalPath: signalPath, output: &bytes.Buffer{}, done: make(chan error, 1)}
	probe.cmd = exec.Command(executable, "-test.run=^TestChannelDeliveryReconciliationProcessHelper$")
	probe.cmd.Env = append(os.Environ(),
		"MULTICA_CHANNEL_DELIVERY_PROCESS_ROLE="+role,
		"MULTICA_CHANNEL_DELIVERY_PROCESS_ID="+uuidText(deliveryID),
		"MULTICA_CHANNEL_DELIVERY_PROCESS_SIGNAL="+signalPath,
	)
	probe.cmd.Stdout = probe.output
	probe.cmd.Stderr = probe.output
	if err := probe.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { probe.done <- probe.cmd.Wait() }()
	t.Cleanup(func() {
		if probe.cmd.Process != nil {
			_ = probe.cmd.Process.Kill()
		}
	})
	return probe
}

func waitProcessSignal(t *testing.T, probe *processProbe, expected string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-probe.done:
			body, readErr := os.ReadFile(probe.signalPath)
			if readErr == nil && strings.TrimSpace(string(body)) == expected {
				probe.done <- err
				return
			}
			t.Fatalf("process probe exited before %q: %v\n%s", expected, err, probe.output.String())
		case <-timer.C:
			t.Fatalf("process probe timed out waiting for %q\n%s", expected, probe.output.String())
		case <-ticker.C:
			body, err := os.ReadFile(probe.signalPath)
			if err == nil && strings.TrimSpace(string(body)) == expected {
				return
			}
		}
	}
}

func killProcessProbe(t *testing.T, probe *processProbe) {
	t.Helper()
	if err := probe.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-probe.done:
		if err == nil {
			t.Fatal("killed process probe exited successfully")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("killed process probe did not exit")
	}
}

func waitProcessProbeExit(t *testing.T, probe *processProbe) {
	t.Helper()
	select {
	case err := <-probe.done:
		if err != nil {
			t.Fatalf("process probe failed: %v\n%s", err, probe.output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process probe did not exit")
	}
}

func writeProcessSignal(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertProcessProbeStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, status string, attempts int32, publishLease, deliveryLease bool) {
	t.Helper()
	row, err := db.New(pool).GetChannelDeliveryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != status || row.AttemptCount != attempts || row.RetryPublishToken.Valid != publishLease || row.LeaseToken.Valid != deliveryLease {
		t.Fatalf("process probe row=%+v", row)
	}
}

func pgUUIDFromText(value string) (pgtype.UUID, error) {
	var result pgtype.UUID
	if err := result.Scan(value); err != nil || !result.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID %q", value)
	}
	return result, nil
}
