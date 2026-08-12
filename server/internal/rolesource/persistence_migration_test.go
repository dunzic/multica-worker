package rolesource

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestRoleSourceMigrationsRespectRepositorySafetyRules(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "[0-9]*_role_source_*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	if len(matches) < 20 {
		t.Fatalf("found %d role-source up migrations, want control-plane migration plus isolated indexes", len(matches))
	}
	indexPattern := regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b.*;\s*$`)
	containsIndexPattern := regexp.MustCompile(`(?i)\bCREATE\s+(UNIQUE\s+)?INDEX\b`)
	for _, name := range matches {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		statements := []string{}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				statements = append(statements, line)
			}
		}
		statementBody := strings.Join(statements, "\n")
		upper := strings.ToUpper(statementBody)
		if strings.Contains(upper, "REFERENCES ") || strings.Contains(upper, "FOREIGN KEY") || strings.Contains(upper, " ON DELETE ") {
			t.Fatalf("%s introduces a forbidden database relationship", name)
		}
		if !containsIndexPattern.MatchString(statementBody) {
			if strings.Contains(upper, "PRIMARY KEY") || regexp.MustCompile(`(?i)\bUNIQUE\b`).MatchString(statementBody) {
				t.Fatalf("%s creates an inline/non-concurrent index", name)
			}
			continue
		}
		if !indexPattern.MatchString(statementBody) || strings.Count(statementBody, ";") != 1 {
			t.Fatalf("%s must contain exactly one CREATE INDEX CONCURRENTLY statement", name)
		}
	}
}

func TestRoleSourceSecretTransferPersistenceIsCiphertextOnlyAndSelfClearing(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "304_role_source_secret_transfer.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"environment_value", "secret_value", "mcp_definition", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("secret transfer schema contains plaintext field %q", forbidden)
		}
	}
	for _, required := range []string{"private_key_ciphertext", "key_id", "envelope", "envelope_digest", "expires_at", "consumed_at"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("secret transfer schema is missing %q", required)
		}
	}
	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queryText := string(queries)
	for _, query := range []string{"ConsumeRoleSourceSecretTransfer", "ExpireRoleSourceSecretTransfers"} {
		start := strings.Index(queryText, "-- name: "+query+" ")
		if start < 0 {
			t.Fatalf("query %s is missing", query)
		}
		section := queryText[start:]
		if next := strings.Index(section[1:], "\n-- name: "); next >= 0 {
			section = section[:next+1]
		}
		if !strings.Contains(section, "envelope = NULL") || !strings.Contains(section, "private_key_ciphertext = decode(repeat('00', 60), 'hex')") {
			t.Fatalf("query %s does not clear recoverable ciphertext: %s", query, section)
		}
	}
}

func TestRoleSourcePersistenceNeverStoresRawSourceConfig(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "273_role_source_control_plane.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"root_path", "config_raw", "config_plaintext", "secret_value", "credential_value"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("control-plane schema contains forbidden raw source field %q", forbidden)
		}
	}
	for _, required := range []string{"daemon_config_id", "config_redacted", "snapshot_digest", "plan_digest", "event_digest", "lease_token"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("control-plane schema is missing safety field %q", required)
		}
	}
}

func TestRoleSourceTaskPinsAreContentFreeAndRetryStable(t *testing.T) {
	schemaBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "316_role_source_task_pin.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(schemaBody))
	for _, forbidden := range []string{"instructions", "custom_env", "mcp_config", "secret_value", "artifact_body", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("task pin schema contains forbidden runtime content %q", forbidden)
		}
	}
	for _, required := range []string{"snapshot_digest", "role_object_digest", "target_state_digest", "capability_pins", "inherited_from_task_id"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("task pin schema is missing %q", required)
		}
	}

	triggerBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "319_role_source_task_pin_trigger.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	trigger := string(triggerBody)
	for _, required := range []string{
		"WHERE parent.task_id = NEW.parent_task_id",
		"parent.snapshot_digest",
		"parent.capability_pins",
		"mapping.last_snapshot_digest",
		"role_source_agent_state_digest",
		"role_source_capability_version",
		"'target_skill_id', skill_mapping.target_id",
		"skill_mapping.last_snapshot_digest = snapshot.snapshot_digest",
		"unresolved capability provenance",
		"role source task pins are immutable",
		"role_source_version_stale",
		"task.status IN ('queued', 'deferred', 'dispatched')",
	} {
		if !strings.Contains(trigger, required) {
			t.Fatalf("task pin trigger is missing contract %q", required)
		}
	}
}

func TestRoleSourceClaimFinalizationUsesConsistentLockOrder(t *testing.T) {
	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	roleSourceQuery := string(queries)
	start := strings.Index(roleSourceQuery, "-- name: IsRoleSourceTaskPinCurrent ")
	if start < 0 {
		t.Fatal("role source claim validation query is missing")
	}
	section := roleSourceQuery[start:]
	if next := strings.Index(section[1:], "\n-- name: "); next >= 0 {
		section = section[:next+1]
	}
	for _, required := range []string{"WITH locked_mapping AS MATERIALIZED", "FOR UPDATE OF mapping", "role_source_agent_state_digest"} {
		if !strings.Contains(section, required) {
			t.Fatalf("role source claim validation does not contain %q", required)
		}
	}

	serviceBody, err := os.ReadFile(filepath.Join("..", "service", "task.go"))
	if err != nil {
		t.Fatal(err)
	}
	service := string(serviceBody)
	validateAt := strings.Index(service, "qtx.IsRoleSourceTaskPinCurrent")
	lockTaskAt := strings.Index(service, "qtx.LockAgentTaskClaim")
	createTokenAt := strings.Index(service, "qtx.CreateTaskToken")
	if validateAt < 0 || lockTaskAt < 0 || createTokenAt < 0 || !(validateAt < lockTaskAt && lockTaskAt < createTokenAt) {
		t.Fatal("claim finalization must lock role mapping, then exact task generation, before creating a token")
	}
}
