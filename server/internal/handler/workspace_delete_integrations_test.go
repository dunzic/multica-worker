package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// These tables were missing from the teardown manifest after integration.
// Prove the production deletion graph removes them without crossing tenants.
func TestDeleteWorkspace_CleansEmailAndAttestationData(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	deletedWS := dbfx.Workspace(t, "Delete integration data", "delete-integration-data")
	retainedWS := dbfx.Workspace(t, "Retain integration data", "retain-integration-data")
	tables := []string{
		"agent_email_policy_version", "agent_email_policy_state", "agent_email_approval",
		"email_message", "agent_email_quota_reservation", "email_delivery_recipient",
		"role_source_runtime_attestation_observation",
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, wsID := range []string{deletedWS, retainedWS} {
		// Register before seeding so a partially failed fixture is cleaned too.
		// Append-only email records require a workspace teardown transaction.
		t.Cleanup(func() {
			ctx := context.Background()
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Errorf("begin cleanup: %v", err)
				return
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, "SET LOCAL multica.workspace_teardown = 'on'"); err != nil {
				t.Errorf("enable cleanup: %v", err)
				return
			}
			q := db.New(tx)
			if err := q.DeleteWorkspaceLeafData(ctx, parseUUID(wsID)); err != nil {
				t.Errorf("cleanup email data: %v", err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Errorf("commit cleanup: %v", err)
			}
		})
		dbfx.Member(t, wsID, testUserID, "owner")
		runtimeID := dbfx.Runtime(t, "Teardown runtime", testutil.Cols{"workspace_id": wsID})
		agentID := dbfx.Agent(t, "Teardown agent", runtimeID, testutil.Cols{"workspace_id": wsID})
		taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "completed"})
		policyID := dbfx.Insert(t, "agent_email_policy_version", testutil.Cols{
			"workspace_id": wsID, "agent_id": agentID, "version": 1,
			"config_digest": digest, "agent_prompt_digest": digest,
			"plugin_release_id":      testutil.Raw("gen_random_uuid()"),
			"plugin_artifact_digest": digest, "plugin_entry_digest": digest,
			"interface_version": "v1", "provider_route_id": testutil.Raw("gen_random_uuid()"),
			"provider_route_digest": digest, "sender_identity_id": testutil.Raw("gen_random_uuid()"),
			"from_address": "fixture@example.test", "recipient_policy": "{}", "rate_policy": "{}",
			"created_by": testUserID,
		})
		dbfx.InsertNoID(t, "agent_email_policy_state", testutil.Cols{
			"workspace_id": wsID, "agent_id": agentID, "current_policy_version_id": policyID,
			"current_config_digest": digest, "state": "draft",
		}, "workspace_id = $1", wsID)
		dbfx.Insert(t, "agent_email_approval", testutil.Cols{
			"workspace_id": wsID, "agent_id": agentID, "policy_version_id": policyID,
			"config_digest": digest, "approval_authority_digest": digest,
			"decision": "requested", "request_key_digest": digest,
		})
		messageID := dbfx.Insert(t, "email_message", testutil.Cols{
			"workspace_id": wsID, "agent_id": agentID, "task_id": taskID,
			"mode": "sandbox", "policy_version_id": policyID,
			"idempotency_key_digest": digest, "request_digest": digest,
			"provider_route_id":  testutil.Raw("gen_random_uuid()"),
			"sender_identity_id": testutil.Raw("gen_random_uuid()"),
			"recipient_count":    1, "intended_to": "[]", "effective_to": "[]",
			"subject_digest": digest, "body_digest": digest, "encrypted_payload_ref": "fixture-payload",
		})
		dbfx.InsertNoID(t, "agent_email_quota_reservation", testutil.Cols{
			"workspace_id": wsID, "message_id": messageID, "agent_id": agentID,
			"provider_route_id":  testutil.Raw("gen_random_uuid()"),
			"sender_identity_id": testutil.Raw("gen_random_uuid()"), "recipient_count": 1,
		}, "workspace_id = $1", wsID)
		dbfx.Insert(t, "email_delivery_recipient", testutil.Cols{
			"workspace_id": wsID, "message_id": messageID,
			"recipient_digest": digest, "encrypted_recipient_ref": "fixture-recipient",
		})
		dbfx.InsertNoID(t, "role_source_runtime_attestation_observation", testutil.Cols{
			"workspace_id": wsID, "runtime_id": runtimeID,
			"contract_version": "role-source-config-attestation-v1",
			"loaded":           false, "attestation_id": digest, "sources": "[]",
		}, "workspace_id = $1", wsID)
		for _, table := range tables {
			if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", wsID); got != 1 {
				t.Fatalf("fixture %s in %s = %d, want 1", table, wsID, got)
			}
		}
	}

	req := withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+deletedWS, nil), "id", deletedWS)
	testutil.Call(t, testHandler.DeleteWorkspace, req).Want(http.StatusNoContent)
	for _, table := range tables {
		if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", deletedWS); got != 0 {
			t.Errorf("%s retained %d rows in deleted workspace", table, got)
		}
		if got := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", retainedWS); got != 1 {
			t.Errorf("%s in other workspace = %d, want 1", table, got)
		}
	}
}
