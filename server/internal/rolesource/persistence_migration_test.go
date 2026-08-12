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

func TestRoleSourceSnapshotArtifactReachabilityIsTransactionalAndBackfilled(t *testing.T) {
	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queryText := string(queries)
	lockStart := strings.Index(queryText, "-- name: ListRoleSourceArtifactsForSnapshotByDigests ")
	if lockStart < 0 {
		t.Fatal("snapshot artifact locking query is missing")
	}
	lockSection := queryText[lockStart:]
	if next := strings.Index(lockSection[1:], "\n-- name: "); next >= 0 {
		lockSection = lockSection[:next+1]
	}
	if !strings.Contains(lockSection, "FOR SHARE") {
		t.Fatalf("snapshot artifact query does not fence GC: %s", lockSection)
	}
	for _, required := range []string{"InsertRoleSourceSnapshotArtifacts", "ON CONFLICT (source_id, snapshot_digest, artifact_digest) DO NOTHING", "ListRoleSourceSnapshotArtifacts"} {
		if !strings.Contains(queryText, required) {
			t.Fatalf("reachability query contract is missing %q", required)
		}
	}

	controlBody, err := os.ReadFile("controlplane.go")
	if err != nil {
		t.Fatal(err)
	}
	control := string(controlBody)
	verifyAt := strings.Index(control, "verifySnapshotArtifacts(ctx")
	insertSnapshotAt := strings.Index(control, "qtx.InsertRoleSourceSnapshot(ctx")
	insertEdgesAt := strings.Index(control, "persistSnapshotArtifactEdges(ctx")
	completeAt := strings.Index(control, "qtx.CompleteRoleSourceScanSuccess(ctx")
	if verifyAt < 0 || insertSnapshotAt < 0 || insertEdgesAt < 0 || completeAt < 0 ||
		!(verifyAt < insertSnapshotAt && insertSnapshotAt < insertEdgesAt && insertEdgesAt < completeAt) {
		t.Fatal("scan success must lock bodies, insert snapshot, persist edges, then complete the request")
	}

	backfillBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "330_role_source_snapshot_artifact_backfill.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	backfill := string(backfillBody)
	for _, path := range []string{
		"$.capabilities[*].entrypoint", "$.capabilities[*].artifacts[*]", "$.roles[*].instructions",
		"$.roles[*].profile", "$.roles[*].skills[*].entrypoint", "$.roles[*].skills[*].artifacts[*]",
		"$.roles[*].automations[*].prompt",
	} {
		if !strings.Contains(backfill, path) {
			t.Fatalf("reachability backfill is missing manifest path %q", path)
		}
	}
}

func TestRoleSourceApplyFailurePersistenceIsContentFree(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "324_role_source_apply_failure.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"request_key text", "raw_error", "error_message", "payload", "manifest", "artifact_body", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("apply failure schema contains sensitive field %q", forbidden)
		}
	}
	for _, required := range []string{"request_key_digest", "failure_stage", "failure_code", "occurred_at"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("apply failure schema is missing %q", required)
		}
	}
}

func TestRoleSourceArtifactDeleteIntentSurvivesTenantTeardownWithoutContent(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "331_role_source_artifact_delete_intent.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"workspace_id uuid", "source_id uuid", "artifact_body", "manifest jsonb", "prompt", "credential", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("artifact delete intent retains forbidden tenant/content field %q", forbidden)
		}
	}
	for _, required := range []string{"storage_key", "artifact_digest", "lease_token", "lease_expires_at", "tombstone_pass", "next_attempt_at"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("artifact delete intent is missing lifecycle field %q", required)
		}
	}
}

func TestRoleSourceRuntimeAttestationIsBoundedRedactedAndExplicitlyDeleted(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "334_role_source_runtime_attestation.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"root_path", "allowed_roots jsonb", "config_raw", "config_plaintext", "digest_key", "secret_value", "credential_value"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("runtime attestation schema contains private field %q", forbidden)
		}
	}
	for _, required := range []string{"attestation_id", "config_revision", "sources jsonb", "observed_at", "changed_at", "observation_count", "jsonb_array_length(sources) between 1 and 512"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("runtime attestation schema is missing %q", required)
		}
	}

	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queryText := string(queries)
	for _, required := range []string{"RecordRoleSourceRuntimeAttestation", "ON CONFLICT (runtime_id)", "ON CONFLICT (runtime_id, attestation_id)", "observation_count + 1"} {
		if !strings.Contains(queryText, required) {
			t.Fatalf("runtime attestation persistence is missing %q", required)
		}
	}
	for _, name := range []string{"workspace.sql", "workspace_delete.sql"} {
		cleanup, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"role_source_runtime_attestation_observation", "role_source_runtime_attestation"} {
			if !strings.Contains(string(cleanup), "DELETE FROM "+table) {
				t.Fatalf("%s does not explicitly delete %s", name, table)
			}
		}
	}
	for _, name := range []string{"runtime.sql", "runtime_profile.sql"} {
		cleanup, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"role_source_runtime_attestation_observation", "role_source_runtime_attestation"} {
			if !strings.Contains(string(cleanup), "DELETE FROM "+table) {
				t.Fatalf("%s does not explicitly delete %s on runtime teardown", name, table)
			}
		}
	}
	runtimeQueries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "runtime.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtimeQueries), "role_source.runtime_id = agent_runtime.id") {
		t.Fatal("stale runtime GC does not preserve role-source-bound runtimes")
	}
	roleSourceQueries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"CountRoleSourcesByRuntime", "CountRoleSourcesByRuntimes", "ReassignRoleSourcesToRuntime", "LockRoleSourceRuntimeForRegistration", "FOR KEY SHARE"} {
		if !strings.Contains(string(roleSourceQueries), required) {
			t.Fatalf("runtime relationship guard is missing %q", required)
		}
	}
	controlBody, err := os.ReadFile("controlplane.go")
	if err != nil {
		t.Fatal(err)
	}
	control := string(controlBody)
	lockAt := strings.Index(control, "qtx.LockRoleSourceRuntimeForRegistration(ctx")
	createAt := strings.Index(control, "qtx.CreateRoleSource(ctx")
	if lockAt < 0 || createAt < 0 || lockAt > createAt {
		t.Fatal("role-source registration must lock its runtime before creating the source")
	}
	handlerBody, err := os.ReadFile(filepath.Join("..", "handler", "daemon.go"))
	if err != nil {
		t.Fatal(err)
	}
	handler := string(handlerBody)
	attestationStart := strings.Index(handler, "func (h *Handler) recordRoleSourceRuntimeAttestation(")
	if attestationStart < 0 {
		t.Fatal("runtime attestation persistence handler is missing")
	}
	attestation := handler[attestationStart:]
	workspaceLockAt := strings.Index(attestation, "qtx.LockWorkspaceForRoleSourceMutation(ctx")
	runtimeLockAt := strings.Index(attestation, "qtx.LockRoleSourceRuntimeForRegistration(ctx")
	recordAt := strings.Index(attestation, "qtx.RecordRoleSourceRuntimeAttestation(ctx")
	commitAt := strings.Index(attestation, "tx.Commit(ctx)")
	if workspaceLockAt < 0 || runtimeLockAt < 0 || recordAt < 0 || commitAt < 0 ||
		!(workspaceLockAt < runtimeLockAt && runtimeLockAt < recordAt && recordAt < commitAt) {
		t.Fatal("runtime attestation must lock workspace and runtime before writing and acknowledging durable evidence")
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
		"'fallback', COALESCE(binding->>'fallback', '')",
		"skill_mapping.last_snapshot_digest = snapshot.snapshot_digest",
		"binding_mapping.source_kind = 'capability_binding'",
		"binding_mapping.target_id = skill_mapping.target_id",
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
