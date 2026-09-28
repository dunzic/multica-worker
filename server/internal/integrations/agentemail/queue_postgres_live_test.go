package agentemail

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentEmailQueueLeaseFencingPostgres(t *testing.T) {
	if os.Getenv("MULTICA_LIVE_AGENT_EMAIL_TEST") != "1" {
		t.Skip("set MULTICA_LIVE_AGENT_EMAIL_TEST=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	queries := db.New(pool)

	t.Run("task created before approval cannot borrow later approval", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixtureWithApproval(t, ctx, pool, 0, false)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)

		snapshot, err := queries.GetAgentEmailTaskAuthorizationSnapshot(ctx, db.GetAgentEmailTaskAuthorizationSnapshotParams{
			TaskID: fixture.taskID, AgentID: fixture.agentID, WorkspaceID: fixture.workspaceID,
		})
		if err != nil {
			t.Fatalf("get pending snapshot: %v", err)
		}
		if !snapshot.EmailAgentConfigDigest.Valid || !snapshot.EmailAgentPolicyVersionID.Valid || snapshot.EmailAgentApprovalID.Valid {
			t.Fatalf("pending task snapshot = %#v", snapshot)
		}

		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_email_approval (
				id, workspace_id, agent_id, policy_version_id, config_digest,
				approval_authority_digest, decision, request_key_digest,
				actor_user_id, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,'approved',$7,$8,now())`,
			fixture.approvalID, fixture.workspaceID, fixture.agentID, fixture.policyVersionID,
			fixture.configDigest, fixture.authorityDigest, agentEmailTestDigest("9"), fixture.ownerID,
		); err != nil {
			t.Fatalf("approve after enqueue: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE agent_email_policy_state
			SET state='approved', updated_at=now()
			WHERE workspace_id=$1 AND agent_id=$2`, fixture.workspaceID, fixture.agentID); err != nil {
			t.Fatalf("activate approval after enqueue: %v", err)
		}

		snapshot, err = queries.GetAgentEmailTaskAuthorizationSnapshot(ctx, db.GetAgentEmailTaskAuthorizationSnapshotParams{
			TaskID: fixture.taskID, AgentID: fixture.agentID, WorkspaceID: fixture.workspaceID,
		})
		if err != nil {
			t.Fatalf("get snapshot after approval: %v", err)
		}
		if snapshot.EmailAgentApprovalID.Valid {
			t.Fatalf("old task borrowed later approval: %#v", snapshot)
		}
	})

	t.Run("task without approved plugin snapshot cannot pin email policy", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixtureWithAuthorization(t, ctx, pool, 0, true, false)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)

		snapshot, err := queries.GetAgentEmailTaskAuthorizationSnapshot(ctx, db.GetAgentEmailTaskAuthorizationSnapshotParams{
			TaskID: fixture.taskID, AgentID: fixture.agentID, WorkspaceID: fixture.workspaceID,
		})
		if err != nil {
			t.Fatalf("get mismatched plugin snapshot: %v", err)
		}
		if snapshot.EmailAgentConfigDigest.Valid || snapshot.EmailAgentPolicyVersionID.Valid || snapshot.EmailAgentApprovalID.Valid {
			t.Fatalf("task pinned policy for mismatched plugin: %#v", snapshot)
		}
	})

	t.Run("concurrent claim has one lease owner", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)

		const workers = 24
		start := make(chan struct{})
		claimed := make(chan db.EmailMessage, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rows, claimErr := queries.ClaimAgentEmailMessages(ctx, db.ClaimAgentEmailMessagesParams{
					LeaseToken:    newAgentEmailPGUUID(),
					LeaseDuration: agentEmailTestInterval(time.Minute),
					WorkspaceID:   fixture.workspaceID,
					ClaimLimit:    1,
				})
				if claimErr != nil {
					errs <- claimErr
					return
				}
				for _, row := range rows {
					claimed <- row
				}
			}()
		}
		close(start)
		wg.Wait()
		close(claimed)
		close(errs)
		for claimErr := range errs {
			t.Errorf("claim: %v", claimErr)
		}
		var claims []db.EmailMessage
		for row := range claimed {
			claims = append(claims, row)
		}
		if len(claims) != 1 || claims[0].LeaseGeneration != 1 || claims[0].AttemptCount != 1 {
			t.Fatalf("claims = %#v, want one generation-1 claim", claims)
		}
	})

	t.Run("terminal task cannot authorize sandbox", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled', completed_at=now() WHERE id=$1`, fixture.taskID); err != nil {
			t.Fatalf("cancel task: %v", err)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("terminal task authorization error = %v, want no rows", err)
		}
	})

	t.Run("owner email change blocks sandbox", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if _, err := pool.Exec(ctx, `UPDATE "user" SET email=$2 WHERE id=$1`, fixture.ownerID, "changed-"+fixture.ownerEmail); err != nil {
			t.Fatalf("change owner email: %v", err)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("changed owner authorization error = %v, want no rows", err)
		}
	})

	t.Run("production requires server feature and cohort gates", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)

		for _, gates := range []struct {
			productionEnabled bool
			cohortAllowed     bool
		}{{false, true}, {true, false}} {
			_, err := queries.GetApprovedAgentEmailPolicyForAdmission(ctx, db.GetApprovedAgentEmailPolicyForAdmissionParams{
				WorkspaceID: fixture.workspaceID, AgentID: fixture.agentID, TaskID: fixture.taskID,
				ApprovalAuthorityDigest: fixture.authorityDigest,
				ProductionEnabled:       gates.productionEnabled, CohortAllowed: gates.cohortAllowed,
			})
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("production admission gates %#v error = %v, want no rows", gates, err)
			}
		}
		if _, err := queries.GetApprovedAgentEmailPolicyForAdmission(ctx, db.GetApprovedAgentEmailPolicyForAdmissionParams{
			WorkspaceID: fixture.workspaceID, AgentID: fixture.agentID, TaskID: fixture.taskID,
			ApprovalAuthorityDigest: fixture.authorityDigest,
			ProductionEnabled:       true, CohortAllowed: true,
		}); err != nil {
			t.Fatalf("production admission with both gates: %v", err)
		}

		makeAgentEmailFixtureProduction(t, ctx, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		for _, gates := range []struct {
			productionEnabled bool
			cohortAllowed     bool
		}{{false, true}, {true, false}} {
			_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
				WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
				LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
				SandboxOwnerEmail: fixture.ownerEmail, ProductionEnabled: gates.productionEnabled,
				CohortAllowed: gates.cohortAllowed, ApprovalAuthorityDigest: fixture.authorityDigest,
			})
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("production gates %#v authorization error = %v, want no rows", gates, err)
			}
		}

		authorized, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ProductionEnabled: true,
			CohortAllowed: true, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if err != nil {
			t.Fatalf("authorize production with both gates: %v", err)
		}
		if !authorized.AuthorizedAt.Valid {
			t.Fatalf("production message was not authorized: %#v", authorized)
		}
	})

	t.Run("revocation before provider call blocks production", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		makeAgentEmailFixtureProduction(t, ctx, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_email_approval (
				id,workspace_id,agent_id,policy_version_id,config_digest,approval_authority_digest,
				decision,request_key_digest,actor_user_id,reason_code,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,'revoked',$7,$8,'test_revocation',now())`,
			newAgentEmailPGUUID(), fixture.workspaceID, fixture.agentID, fixture.policyVersionID,
			fixture.configDigest, fixture.authorityDigest, agentEmailTestDigest("e"), fixture.ownerID,
		); err != nil {
			t.Fatalf("revoke production approval: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE agent_email_policy_state SET state='revoked',updated_at=now()
			WHERE workspace_id=$1 AND agent_id=$2`, fixture.workspaceID, fixture.agentID); err != nil {
			t.Fatalf("activate production revocation: %v", err)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ProductionEnabled: true,
			CohortAllowed: true, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("revoked production authorization error = %v, want no rows", err)
		}
	})

	t.Run("approver role downgrade blocks production", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		makeAgentEmailFixtureProduction(t, ctx, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if _, err := pool.Exec(ctx, `UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2`, fixture.workspaceID, fixture.ownerID); err != nil {
			t.Fatalf("downgrade approver: %v", err)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ProductionEnabled: true,
			CohortAllowed: true, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("downgraded approver authorization error = %v, want no rows", err)
		}
	})

	t.Run("agent prompt change invalidates production", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		makeAgentEmailFixtureProduction(t, ctx, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if _, err := pool.Exec(ctx, `UPDATE agent SET instructions='changed after approval' WHERE id=$1`, fixture.agentID); err != nil {
			t.Fatalf("change agent prompt: %v", err)
		}
		var state string
		if err := pool.QueryRow(ctx, `
			SELECT state FROM agent_email_policy_state
			WHERE workspace_id=$1 AND agent_id=$2`, fixture.workspaceID, fixture.agentID).Scan(&state); err != nil {
			t.Fatalf("read invalidated email policy: %v", err)
		}
		if state != "draft" {
			t.Fatalf("email policy state after prompt change = %q, want draft", state)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ProductionEnabled: true,
			CohortAllowed: true, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("prompt-changed production authorization error = %v, want no rows", err)
		}
	})

	t.Run("authorized expired lease freezes ambiguous", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		authorized, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: claim.LeaseToken, LeaseGeneration: claim.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
		if !authorized.AuthorizedAt.Valid || !authorized.AuthorizedLeaseGeneration.Valid ||
			authorized.AuthorizedLeaseGeneration.Int64 != claim.LeaseGeneration {
			t.Fatalf("authorization was not bound to generation: %#v", authorized)
		}
		expireAgentEmailLease(t, ctx, pool, fixture.messageID)
		if rows, err := queries.ReconcileExpiredAgentEmailMessageLeases(ctx, fixture.workspaceID); err != nil || rows != 1 {
			t.Fatalf("reconcile rows=%d err=%v", rows, err)
		}
		row := getAgentEmailFixture(t, ctx, queries, fixture)
		if row.Status != "ambiguous" || row.LastErrorCode.String != "lease_expired_after_authorization" ||
			!row.CompletedAt.Valid || row.LeaseToken.Valid || !row.AuthorizedAt.Valid {
			t.Fatalf("reconciled row = %#v", row)
		}
		if rows, err := queries.ClaimAgentEmailMessages(ctx, db.ClaimAgentEmailMessagesParams{
			LeaseToken: newAgentEmailPGUUID(), LeaseDuration: agentEmailTestInterval(time.Minute),
			WorkspaceID: fixture.workspaceID, ClaimLimit: 1,
		}); err != nil || len(rows) != 0 {
			t.Fatalf("ambiguous message was reclaimed: rows=%d err=%v", len(rows), err)
		}
	})

	t.Run("unauthorized lease recovers with strict generation fencing", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 0)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		first := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		expireAgentEmailLease(t, ctx, pool, fixture.messageID)
		if rows, err := queries.ReconcileExpiredAgentEmailMessageLeases(ctx, fixture.workspaceID); err != nil || rows != 1 {
			t.Fatalf("reconcile rows=%d err=%v", rows, err)
		}
		recovered := getAgentEmailFixture(t, ctx, queries, fixture)
		if recovered.Status != "queued" || recovered.LeaseToken.Valid || recovered.AuthorizedAt.Valid || recovered.CompletedAt.Valid {
			t.Fatalf("recovered row = %#v", recovered)
		}

		second := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if second.LeaseGeneration != first.LeaseGeneration+1 || second.AttemptCount != first.AttemptCount+1 {
			t.Fatalf("second claim = %#v, first = %#v", second, first)
		}
		_, err := queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("stale authorization error = %v, want no rows", err)
		}
		_, err = queries.AuthorizeClaimedAgentEmailMessage(ctx, db.AuthorizeClaimedAgentEmailMessageParams{
			WorkspaceID: fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: second.LeaseToken, LeaseGeneration: second.LeaseGeneration,
			SandboxOwnerEmail: fixture.ownerEmail, ApprovalAuthorityDigest: fixture.authorityDigest,
		})
		if err != nil {
			t.Fatalf("current authorization: %v", err)
		}

		_, err = queries.UpdateEmailDeliveryRecipientStatus(ctx, db.UpdateEmailDeliveryRecipientStatusParams{
			Status: "accepted", WorkspaceID: fixture.workspaceID,
			MessageID: fixture.messageID, ID: fixture.recipientID,
			LeaseToken: first.LeaseToken, LeaseGeneration: first.LeaseGeneration,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("stale recipient update error = %v, want no rows", err)
		}
		_, err = queries.UpdateEmailDeliveryRecipientStatus(ctx, db.UpdateEmailDeliveryRecipientStatusParams{
			Status: "accepted", WorkspaceID: fixture.workspaceID,
			MessageID: fixture.messageID, ID: fixture.recipientID,
			LeaseToken: second.LeaseToken, LeaseGeneration: second.LeaseGeneration,
		})
		if err != nil {
			t.Fatalf("current recipient update: %v", err)
		}

		retried, err := queries.RetryClaimedAgentEmailMessage(ctx, db.RetryClaimedAgentEmailMessageParams{
			NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true},
			LastErrorCode: pgtype.Text{String: "provider_retryable", Valid: true},
			WorkspaceID:   fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: second.LeaseToken, LeaseGeneration: second.LeaseGeneration,
		})
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if retried.Status != "queued" || retried.AuthorizedAt.Valid || retried.AuthorizedLeaseGeneration.Valid {
			t.Fatalf("retry retained authorization: %#v", retried)
		}

		third := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		_, err = queries.AcceptClaimedAgentEmailMessage(ctx, db.AcceptClaimedAgentEmailMessageParams{
			ProviderMessageID: pgtype.Text{String: "provider-message", Valid: true},
			ProviderStatus:    pgtype.Text{String: "accepted", Valid: true},
			WorkspaceID:       fixture.workspaceID, ID: fixture.messageID,
			LeaseToken: third.LeaseToken, LeaseGeneration: third.LeaseGeneration,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("accept without current authorization error = %v, want no rows", err)
		}
	})

	t.Run("unauthorized max attempt lease becomes dead", func(t *testing.T) {
		fixture := insertAgentEmailQueueFixture(t, ctx, pool, 19)
		defer deleteAgentEmailQueueFixture(t, pool, fixture)
		claim := claimAgentEmailFixture(t, ctx, queries, fixture, newAgentEmailPGUUID())
		if claim.AttemptCount != 20 {
			t.Fatalf("attempt count = %d, want 20", claim.AttemptCount)
		}
		expireAgentEmailLease(t, ctx, pool, fixture.messageID)
		if rows, err := queries.ReconcileExpiredAgentEmailMessageLeases(ctx, fixture.workspaceID); err != nil || rows != 1 {
			t.Fatalf("reconcile rows=%d err=%v", rows, err)
		}
		row := getAgentEmailFixture(t, ctx, queries, fixture)
		if row.Status != "dead" || row.LastErrorCode.String != "lease_expired_after_max_attempts" || !row.CompletedAt.Valid {
			t.Fatalf("dead row = %#v", row)
		}
	})
}

type agentEmailQueueFixture struct {
	workspaceID      pgtype.UUID
	ownerID          pgtype.UUID
	runtimeID        pgtype.UUID
	agentID          pgtype.UUID
	taskID           pgtype.UUID
	policyVersionID  pgtype.UUID
	approvalID       pgtype.UUID
	pluginSnapshotID pgtype.UUID
	pluginReleaseID  pgtype.UUID
	messageID        pgtype.UUID
	recipientID      pgtype.UUID
	providerRouteID  pgtype.UUID
	senderIdentityID pgtype.UUID
	ownerEmail       string
	configDigest     string
	authorityDigest  string
	artifactDigest   string
	entryDigest      string
}

func insertAgentEmailQueueFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptCount int16) agentEmailQueueFixture {
	return insertAgentEmailQueueFixtureWithApproval(t, ctx, pool, attemptCount, true)
}

func insertAgentEmailQueueFixtureWithApproval(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptCount int16, approved bool) agentEmailQueueFixture {
	return insertAgentEmailQueueFixtureWithAuthorization(t, ctx, pool, attemptCount, approved, true)
}

func insertAgentEmailQueueFixtureWithAuthorization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attemptCount int16, approved, pluginMatches bool) agentEmailQueueFixture {
	t.Helper()
	fixture := agentEmailQueueFixture{
		workspaceID: newAgentEmailPGUUID(), ownerID: newAgentEmailPGUUID(), runtimeID: newAgentEmailPGUUID(),
		agentID: newAgentEmailPGUUID(), taskID: newAgentEmailPGUUID(), policyVersionID: newAgentEmailPGUUID(),
		approvalID: newAgentEmailPGUUID(), pluginSnapshotID: newAgentEmailPGUUID(), pluginReleaseID: newAgentEmailPGUUID(),
		messageID: newAgentEmailPGUUID(), recipientID: newAgentEmailPGUUID(),
		providerRouteID: newAgentEmailPGUUID(), senderIdentityID: newAgentEmailPGUUID(),
		ownerEmail: "agent-email-" + uuid.NewString() + "@example.test", configDigest: agentEmailTestDigest("6"),
		authorityDigest: agentEmailTestDigest("a"), artifactDigest: agentEmailTestDigest("8"),
		entryDigest: agentEmailTestDigest("b"),
	}
	state := "pending"
	if approved {
		state = "approved"
	}
	manifestReleaseID := fixture.pluginReleaseID
	if !pluginMatches {
		manifestReleaseID = newAgentEmailPGUUID()
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin email fixture: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Agent email owner',$2)`, fixture.ownerID, fixture.ownerEmail); err != nil {
		t.Fatalf("insert email fixture owner: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workspace (id,name,slug) VALUES ($1,'Agent email test',$2)`, fixture.workspaceID, "agent-email-"+uuid.NewString()); err != nil {
		t.Fatalf("insert email fixture workspace: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO member (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, fixture.workspaceID, fixture.ownerID); err != nil {
		t.Fatalf("insert email fixture member: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_runtime (id,workspace_id,name,runtime_mode,provider,status,owner_id)
		VALUES ($1,$2,'Agent email runtime','local','codex','online',$3)`, fixture.runtimeID, fixture.workspaceID, fixture.ownerID); err != nil {
		t.Fatalf("insert email fixture runtime: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent (id,workspace_id,name,runtime_mode,runtime_id,owner_id)
		VALUES ($1,$2,'Agent email test','local',$3,$4)`, fixture.agentID, fixture.workspaceID, fixture.runtimeID, fixture.ownerID); err != nil {
		t.Fatalf("insert email fixture agent: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_capability_snapshot (
			id,workspace_id,revision,source_generations,compiler_version,schema_version,
			snapshot_digest,compiled_entries
		) VALUES (
			$1,$2,1,'{}'::jsonb,'plugin-compiler-v1',1,$3,
			jsonb_build_array(jsonb_build_object(
				'release_id',$4::uuid::text,
				'artifact_digest',$5::text,
				'entry_digest',$6::text,
				'scope_type','agent',
				'scope_id',$7::uuid::text
			))
		)`, fixture.pluginSnapshotID, fixture.workspaceID, agentEmailTestDigest("f"), manifestReleaseID,
		fixture.artifactDigest, fixture.entryDigest, fixture.agentID); err != nil {
		t.Fatalf("insert email fixture plugin snapshot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_workspace_capability_state (
			workspace_id,next_revision,active_snapshot_id,active_revision
		) VALUES ($1,2,$2,1)`, fixture.workspaceID, fixture.pluginSnapshotID); err != nil {
		t.Fatalf("insert email fixture plugin state: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_email_policy_version (
			id,workspace_id,agent_id,version,config_digest,agent_prompt_digest,
			plugin_release_id,plugin_artifact_digest,plugin_entry_digest,interface_version,
			provider_route_id,provider_route_digest,sender_identity_id,from_address,
			recipient_policy,rate_policy,created_by,created_at
		) VALUES (
			$1,$2,$3,1,$4,$5,$6,$7,$8,'notification-email/v1',
			$9,$10,$11,$12,jsonb_build_object('allowlist',jsonb_build_array($12::text)),'{}'::jsonb,$13,now()
		)`,
		fixture.policyVersionID, fixture.workspaceID, fixture.agentID, fixture.configDigest,
		agentEmailTestDigest("7"), fixture.pluginReleaseID, fixture.artifactDigest, fixture.entryDigest,
		fixture.providerRouteID, agentEmailTestDigest("c"), fixture.senderIdentityID, fixture.ownerEmail, fixture.ownerID,
	); err != nil {
		t.Fatalf("insert email fixture policy: %v", err)
	}
	if approved {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent_email_approval (
				id,workspace_id,agent_id,policy_version_id,config_digest,approval_authority_digest,
				decision,request_key_digest,actor_user_id,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,'approved',$7,$8,now())`,
			fixture.approvalID, fixture.workspaceID, fixture.agentID, fixture.policyVersionID,
			fixture.configDigest, fixture.authorityDigest, agentEmailTestDigest("d"), fixture.ownerID,
		); err != nil {
			t.Fatalf("insert email fixture approval: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_email_policy_state (
			workspace_id,agent_id,current_policy_version_id,current_config_digest,
			assigned_approver_user_id,approval_authority_digest,state,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,now())`,
		fixture.workspaceID, fixture.agentID, fixture.policyVersionID, fixture.configDigest,
		fixture.ownerID, fixture.authorityDigest, state,
	); err != nil {
		t.Fatalf("insert email fixture policy state: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_task_queue (
			id,agent_id,runtime_id,status,priority,dispatched_at,started_at
		) VALUES ($1,$2,$3,'running',0,now(),now())`, fixture.taskID, fixture.agentID, fixture.runtimeID); err != nil {
		t.Fatalf("insert email fixture task: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_message (
			id, workspace_id, agent_id, task_id, mode, policy_version_id,
			idempotency_key_digest, request_digest, provider_route_id,
			sender_identity_id, recipient_count, intended_to, effective_to,
			subject_digest, body_digest, encrypted_payload_ref, attempt_count,
			next_attempt_at, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, 'sandbox', $5,
				$6, $7, $8, $9, 1, jsonb_build_array($10::text),
				jsonb_build_array($10::text), $11, $12, 'opaque-payload-ref', $13,
				now() - interval '1 second', now(), now()
		)`,
		fixture.messageID, fixture.workspaceID, fixture.agentID, fixture.taskID,
		fixture.policyVersionID, agentEmailTestDigest("1"), agentEmailTestDigest("2"),
		fixture.providerRouteID, fixture.senderIdentityID, fixture.ownerEmail,
		agentEmailTestDigest("3"), agentEmailTestDigest("4"), attemptCount,
	); err != nil {
		t.Fatalf("insert email message: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO email_delivery_recipient (
			id, workspace_id, message_id, recipient_digest,
			encrypted_recipient_ref, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'opaque-recipient-ref', now(), now())`,
		fixture.recipientID, fixture.workspaceID, fixture.messageID, agentEmailTestDigest("5"),
	); err != nil {
		t.Fatalf("insert email recipient: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit email fixture: %v", err)
	}
	return fixture
}

func claimAgentEmailFixture(t *testing.T, ctx context.Context, queries *db.Queries, fixture agentEmailQueueFixture, token pgtype.UUID) db.EmailMessage {
	t.Helper()
	rows, err := queries.ClaimAgentEmailMessages(ctx, db.ClaimAgentEmailMessagesParams{
		LeaseToken: token, LeaseDuration: agentEmailTestInterval(time.Minute),
		WorkspaceID: fixture.workspaceID, ClaimLimit: 1,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("claim rows=%d err=%v", len(rows), err)
	}
	return rows[0]
}

func makeAgentEmailFixtureProduction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture agentEmailQueueFixture) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE email_message
		SET mode='production', approval_id=$2
		WHERE workspace_id=$1 AND id=$3`, fixture.workspaceID, fixture.approvalID, fixture.messageID); err != nil {
		t.Fatalf("make email fixture production: %v", err)
	}
}

func getAgentEmailFixture(t *testing.T, ctx context.Context, queries *db.Queries, fixture agentEmailQueueFixture) db.EmailMessage {
	t.Helper()
	row, err := queries.GetAgentEmailMessageForTask(ctx, db.GetAgentEmailMessageForTaskParams{
		WorkspaceID: fixture.workspaceID, AgentID: fixture.agentID,
		TaskID: fixture.taskID, ID: fixture.messageID,
	})
	if err != nil {
		t.Fatalf("get email message: %v", err)
	}
	return row
}

func expireAgentEmailLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, messageID pgtype.UUID) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE email_message SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, messageID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

func deleteAgentEmailQueueFixture(t *testing.T, pool *pgxpool.Pool, fixture agentEmailQueueFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin email fixture cleanup: %v", err)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('multica.workspace_teardown','on',true)`); err != nil {
		t.Errorf("enable email fixture cleanup: %v", err)
		return
	}
	cleanupStatements := []struct {
		query string
		arg   pgtype.UUID
	}{
		{`DELETE FROM email_delivery_recipient WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM agent_email_quota_reservation WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM email_message WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM agent_email_approval WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM agent_email_policy_state WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM agent_email_policy_version WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM plugin_execution_manifest WHERE task_id=$1`, fixture.taskID},
		{`DELETE FROM plugin_workspace_capability_state WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM plugin_capability_snapshot WHERE id=$1`, fixture.pluginSnapshotID},
		{`DELETE FROM agent_task_queue WHERE id=$1`, fixture.taskID},
		{`DELETE FROM agent WHERE id=$1`, fixture.agentID},
		{`DELETE FROM agent_runtime WHERE id=$1`, fixture.runtimeID},
		{`DELETE FROM member WHERE workspace_id=$1`, fixture.workspaceID},
		{`DELETE FROM workspace WHERE id=$1`, fixture.workspaceID},
		{`DELETE FROM "user" WHERE id=$1`, fixture.ownerID},
	}
	for _, statement := range cleanupStatements {
		if _, err := tx.Exec(ctx, statement.query, statement.arg); err != nil {
			t.Errorf("delete email fixture: %v", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit email fixture cleanup: %v", err)
	}
}

func newAgentEmailPGUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

func agentEmailTestInterval(value time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: value.Microseconds(), Valid: true}
}

func agentEmailTestDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
