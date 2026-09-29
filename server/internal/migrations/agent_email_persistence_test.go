package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentEmailPersistenceMigrationHasServerSideGates(t *testing.T) {
	root := realMigrationsDir(t)
	shapeBytes, err := os.ReadFile(filepath.Join(root, "442_agent_email_persistence.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	shape := string(shapeBytes)

	for _, forbidden := range []string{"REFERENCES ", "ON DELETE CASCADE", "ON UPDATE CASCADE"} {
		if strings.Contains(strings.ToUpper(shape), forbidden) {
			t.Fatalf("agent email migration must keep relationships application-owned; found %q", forbidden)
		}
	}
	for _, required := range []string{
		"agent_email_policy_version", "agent_email_policy_state", "agent_email_approval",
		"email_message", "agent_email_quota_reservation", "email_delivery_recipient",
		"email_agent_config_digest", "reserve_agent_email_quota",
		"pg_advisory_xact_lock", "reject_agent_email_append_only_update",
		"lease_generation", "multica.workspace_teardown",
		"BEFORE UPDATE OR DELETE ON agent_email_policy_version",
		"BEFORE UPDATE OR DELETE ON agent_email_approval",
	} {
		if !strings.Contains(shape, required) {
			t.Fatalf("agent email migration is missing %q", required)
		}
	}
	for _, table := range []string{
		"agent_email_policy_version", "agent_email_policy_state", "agent_email_approval",
		"email_message", "agent_email_quota_reservation", "email_delivery_recipient",
	} {
		start := strings.Index(shape, "CREATE TABLE "+table+" (")
		if start < 0 {
			t.Fatalf("missing table %s", table)
		}
		end := strings.Index(shape[start:], ");")
		if end < 0 || !strings.Contains(shape[start:start+end], "workspace_id UUID NOT NULL") {
			t.Fatalf("table %s must carry a non-null workspace_id", table)
		}
	}
}

func TestAgentEmailIndexesAreConcurrentSingleStatementMigrations(t *testing.T) {
	root := realMigrationsDir(t)
	indexMigrations := make([]int, 0, 22)
	for number := 443; number <= 460; number++ {
		indexMigrations = append(indexMigrations, number)
	}
	indexMigrations = append(indexMigrations, 462, 463, 464, 465)
	for _, number := range indexMigrations {
		matches, err := filepath.Glob(filepath.Join(root, migrationNumberPattern(number)+"*.up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("migration %d: got %d files, want 1", number, len(matches))
		}
		body, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.TrimSpace(string(body))
		if !strings.Contains(sql, "INDEX CONCURRENTLY") || strings.Count(sql, ";") != 1 {
			t.Fatalf("%s must contain exactly one concurrent index statement", filepath.Base(matches[0]))
		}
	}
}

func TestAgentEmailTaskAuthorizationSnapshotIsCapturedAtEnqueue(t *testing.T) {
	root := realMigrationsDir(t)
	body, err := os.ReadFile(filepath.Join(root, "461_agent_email_task_authorization_snapshot.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"email_agent_policy_version_id", "email_agent_approval_id",
		"NEW.retry_of_task_id",
		"parent.email_agent_approval_id", "latest_decision.decision = 'approved'",
		"authorization snapshot is immutable once pinned", "plugin_execution_manifest",
		"contribution.entry->>'release_id' = policy.plugin_release_id::text",
		"invalidate_agent_email_policy_on_prompt_change", "NOT VALID",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("task authorization snapshot migration is missing %q", required)
		}
	}
	validation, err := os.ReadFile(filepath.Join(root, "466_validate_agent_email_task_snapshot_shape.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(validation), "VALIDATE CONSTRAINT agent_task_email_authorization_snapshot_shape") {
		t.Fatal("task authorization snapshot shape must be validated in its own low-lock migration")
	}
	disable, err := os.ReadFile(filepath.Join(root, "467_disable_legacy_agent_email_task_snapshot_trigger.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(disable), "DROP TRIGGER IF EXISTS trg_agent_email_task_authorization_snapshot") {
		t.Fatal("Plugin V2 compatibility migration must disable the legacy task snapshot trigger")
	}
}

func TestAgentEmailQueriesPinTasksAndScopeTenantData(t *testing.T) {
	_, self, _, ok := runtimeCaller()
	if !ok {
		t.Fatal("resolve test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(self), "..", ".."))
	queryBytes, err := os.ReadFile(filepath.Join(root, "pkg", "db", "queries", "agent_email.sql"))
	if err != nil {
		t.Fatal(err)
	}
	query := string(queryBytes)
	for _, required := range []string{
		"task.email_agent_config_digest = policy.config_digest",
		"task.email_agent_policy_version_id = policy.id",
		"task.email_agent_approval_id = approval.id",
		"task.status = 'running'",
		"state.current_config_digest = policy.config_digest",
		"state.approval_authority_digest = @approval_authority_digest",
		"message.lease_token = @lease_token",
		"message.lease_generation = @lease_generation",
		"message.authorized_lease_generation = message.lease_generation",
		"status = 'queued'",
		"WHEN authorized_at IS NOT NULL THEN 'ambiguous'",
		"FOR UPDATE OF state",
		"approver_member.role IN ('owner', 'admin')",
		"owner_user.email = @sandbox_owner_email",
		"message.effective_to = jsonb_build_array(@sandbox_owner_email::text)",
		"@production_enabled::boolean",
		"@cohort_allowed::boolean",
		"approval.actor_user_id = state.assigned_approver_user_id",
		"FOR UPDATE SKIP LOCKED",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("agent email query contract is missing %q", required)
		}
		if strings.Contains(query, "PinAgentEmailTaskConfigDigest") {
			t.Fatal("task email authorization must be captured at enqueue, not lazily pinned by a caller")
		}
	}
	for _, name := range []string{
		"GetAgentEmailPolicyVersion", "GetAgentEmailApproval", "GetAgentEmailMessageForTask",
		"ClaimAgentEmailMessages", "AuthorizeClaimedAgentEmailMessage",
	} {
		section := namedQuerySection(query, name)
		if !strings.Contains(section, "workspace_id") || !strings.Contains(section, "@workspace_id") {
			t.Fatalf("query %s is not explicitly workspace-scoped", name)
		}
	}

	cleanupBytes, err := os.ReadFile(filepath.Join(root, "pkg", "db", "queries", "workspace_delete.sql"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := string(cleanupBytes)
	for _, table := range []string{
		"email_delivery_recipient", "agent_email_quota_reservation", "email_message",
		"agent_email_approval", "agent_email_policy_state", "agent_email_policy_version",
	} {
		if !strings.Contains(cleanup, "DELETE FROM "+table+" WHERE workspace_id = $1") {
			t.Fatalf("workspace deletion does not explicitly clean %s", table)
		}
	}
}

func migrationNumberPattern(number int) string {
	return fmt.Sprintf("%03d_", number)
}

func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}

func namedQuerySection(sql, name string) string {
	marker := "-- name: " + name + " "
	start := strings.Index(sql, marker)
	if start < 0 {
		return ""
	}
	rest := sql[start+len(marker):]
	if end := strings.Index(rest, "-- name: "); end >= 0 {
		return rest[:end]
	}
	return rest
}
