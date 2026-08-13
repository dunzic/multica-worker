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

func TestRoleSourceArtifactIntegrityMigrationContract(t *testing.T) {
	root := filepath.Join("..", "..", "migrations")
	for _, required := range []struct {
		name string
		text []string
	}{
		{"354_role_source_artifact_integrity.up.sql", []string{"CREATE TABLE role_source_artifact_integrity", "quarantined", "lease_token", "CHECK"}},
		{"355_role_source_artifact_integrity_unique.up.sql", []string{"CREATE UNIQUE INDEX CONCURRENTLY", "workspace_id, artifact_digest"}},
		{"356_role_source_artifact_integrity_due_index.up.sql", []string{"CREATE INDEX CONCURRENTLY", "WHERE state IN"}},
		{"357_role_source_artifact_integrity_backfill.up.sql", []string{"FROM role_source_artifact", "ON CONFLICT"}},
	} {
		body, err := os.ReadFile(filepath.Join(root, required.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range required.text {
			if !strings.Contains(string(body), fragment) {
				t.Errorf("%s missing %q", required.name, fragment)
			}
		}
	}
	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(queries)
	for _, fragment := range []string{
		"MarkRoleSourceArtifactUploadedForIntegrity", "ClaimNextRoleSourceArtifactIntegrity",
		"QuarantineRoleSourceArtifactIntegrity", "ReleaseRoleSourceArtifactIntegrity",
		"integrity.state IN ('pending', 'healthy')", "FOR SHARE OF artifact, integrity", "removed_integrity",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("role-source SQL missing integrity contract %q", fragment)
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

func TestRoleSourceLegalHoldIsAppendOnlyContentFreeAndFencesWorkspaceDeletion(t *testing.T) {
	migrationBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "339_role_source_legal_hold.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(migrationBody))
	for _, forbidden := range []string{"request_key text", "case_number", "case_reference text", "reason text", "description", "notes", "payload", "manifest", "artifact_body", "credential", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("legal-hold schema contains sensitive/free-text field %q", forbidden)
		}
	}
	for _, required := range []string{
		"create table role_source_legal_hold", "create table role_source_legal_hold_release",
		"request_key_digest", "scope text", "snapshot_digest", "reason_code", "reference_digest", "released_by", "released_at",
	} {
		if !strings.Contains(schema, required) {
			t.Fatalf("legal-hold schema is missing %q", required)
		}
	}

	queriesBody, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queries := string(queriesBody)
	for _, required := range []string{
		"GetRoleSourceLegalHoldForUpdate", "FOR UPDATE", "CountActiveRoleSourceLegalHoldsInWorkspace",
		"NOT EXISTS", "role_source_legal_hold_release",
	} {
		if !strings.Contains(queries, required) {
			t.Fatalf("legal-hold persistence is missing %q", required)
		}
	}
	for _, name := range []string{"workspace.sql", "workspace_delete.sql"} {
		cleanup, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(cleanup)
		holdAt := strings.Index(text, "DELETE FROM role_source_legal_hold WHERE")
		releaseAt := strings.Index(text, "DELETE FROM role_source_legal_hold_release")
		sourceAt := strings.LastIndex(text, "DELETE FROM role_source ")
		if releaseAt < 0 || holdAt < 0 || sourceAt < 0 || !(holdAt < releaseAt && releaseAt < sourceAt) ||
			!strings.Contains(text[releaseAt:sourceAt], "SELECT id FROM") {
			t.Fatalf("%s must delete released holds before their releases and before sources", name)
		}
	}

	guardBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "344_role_source_legal_hold_mutation_guard.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	guard := strings.ToLower(string(guardBody))
	for _, required := range []string{
		"before update or delete on role_source_legal_hold",
		"active role source legal hold cannot be deleted",
		"before update on role_source_legal_hold_release",
		"role source legal hold releases are immutable",
	} {
		if !strings.Contains(guard, required) {
			t.Fatalf("legal-hold database mutation guard is missing %q", required)
		}
	}

	handlerBody, err := os.ReadFile(filepath.Join("..", "handler", "workspace.go"))
	if err != nil {
		t.Fatal(err)
	}
	handler := string(handlerBody)
	lockAt := strings.Index(handler, "qtx.LockWorkspaceForDelete")
	holdAt := strings.Index(handler, "qtx.CountActiveRoleSourceLegalHoldsInWorkspace")
	deleteAt := strings.Index(handler, `name: "delete leaf data"`)
	if lockAt < 0 || holdAt < 0 || deleteAt < 0 || !(lockAt < holdAt && holdAt < deleteAt) {
		t.Fatal("workspace deletion must check legal hold after its workspace lock and before any teardown mutation")
	}
}

func TestRoleSourceRetentionIsPolicyBoundHoldAwareAndRaceFenced(t *testing.T) {
	schemaBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "345_role_source_retention.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(schemaBody))
	for _, forbidden := range []string{"request_key text", "case_number", "description", "notes", "manifest jsonb", "artifact_body", "credential", "plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("retention schema contains content/free-text field %q", forbidden)
		}
	}
	for _, required := range []string{
		"create table role_source_retention_policy", "request_key_digest", "minimum_age_days",
		"keep_successful_snapshots", "create table role_source_retention_candidate", "lease_token",
		"estimated_bytes", "next_attempt_at", "result_code",
	} {
		if !strings.Contains(schema, required) {
			t.Fatalf("retention schema is missing %q", required)
		}
	}

	queryBody, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queries := string(queryBody)
	for _, required := range []string{
		"QueueEligibleRoleSourceRetentionCandidates", "@candidate_limit", "gen_random_uuid()", "ClaimNextRoleSourceRetentionCandidate",
		"FOR UPDATE SKIP LOCKED", "GetRoleSourceSnapshotForUpdate", "GetRoleSourceRetentionBlocker",
		"policy_age", "current_snapshot", "legal_hold", "task_pin", "object_mapping", "active_transfer", "active_apply",
		"recent_plan", "rollback_reserve", "DeleteRoleSourceSnapshotArtifacts",
		"DeleteRoleSourceSnapshotForRetention", "DeleteUnreachableRoleSourceCapabilityVersions",
	} {
		if !strings.Contains(queries, required) {
			t.Fatalf("retention query contract is missing %q", required)
		}
	}

	guardBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "352_role_source_snapshot_retention_guard.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	guard := strings.ToLower(string(guardBody))
	for _, required := range []string{
		"before insert on role_source_task_pin", "for key share of snapshot",
		"before update or delete on role_source_snapshot", "multica.role_source_retention_prune",
		"multica.workspace_teardown", "role_source_legal_hold_release",
	} {
		if !strings.Contains(guard, required) {
			t.Fatalf("snapshot retention guard is missing %q", required)
		}
	}
	policyGuardBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "353_role_source_retention_policy_mutation_guard.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	policyGuard := strings.ToLower(string(policyGuardBody))
	for _, required := range []string{
		"before update or delete on role_source_retention_policy",
		"multica.workspace_teardown", "append-only",
	} {
		if !strings.Contains(policyGuard, required) {
			t.Fatalf("retention policy mutation guard is missing %q", required)
		}
	}

	routerBody, err := os.ReadFile(filepath.Join("..", "..", "cmd", "server", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	router := string(routerBody)
	gcAt := strings.Index(router, "MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED")
	retentionAt := strings.Index(router, "MULTICA_ROLE_SOURCE_RETENTION_ENABLED")
	if gcAt < 0 || retentionAt < 0 || retentionAt < gcAt {
		t.Fatal("historical retention must be independently default-off and nested behind permanent artifact GC")
	}
	controlBody, err := os.ReadFile("retention.go")
	if err != nil {
		t.Fatal(err)
	}
	control := string(controlBody)
	pruneStart := strings.Index(control, "func (c *ControlPlane) PruneRetentionCandidate(")
	if pruneStart < 0 {
		t.Fatal("retention prune control-plane method is missing")
	}
	control = control[pruneStart:]
	if next := strings.Index(control[1:], "\nfunc "); next >= 0 {
		control = control[:next+1]
	}
	workspaceLockAt := strings.Index(control, "qtx.LockWorkspaceForRoleSourceMutation")
	sourceLockAt := strings.Index(control, "qtx.GetRoleSourceForUpdate")
	candidateLockAt := strings.Index(control, "qtx.GetRoleSourceRetentionCandidateForUpdate")
	snapshotLockAt := strings.Index(control, "qtx.GetRoleSourceSnapshotForUpdate")
	if workspaceLockAt < 0 || sourceLockAt < 0 || candidateLockAt < 0 || snapshotLockAt < 0 ||
		!(workspaceLockAt < sourceLockAt && sourceLockAt < candidateLockAt && candidateLockAt < snapshotLockAt) {
		t.Fatal("retention prune must lock workspace, source, candidate and snapshot in one order")
	}
}

func TestRoleSourceLifecycleUsesOneLockOrderAndClearsPendingSecrets(t *testing.T) {
	queryBody, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	lifecycleBody, err := os.ReadFile("lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	secretBody, err := os.ReadFile("secret_controlplane.go")
	if err != nil {
		t.Fatal(err)
	}
	queries := string(queryBody)
	lifecycle := string(lifecycleBody)
	secret := string(secretBody)
	for _, required := range []string{
		"-- name: CancelActiveRoleSourceScans :execrows",
		"status IN ('queued', 'claimed')",
		"-- name: CancelActiveRoleSourceSecretTransfers :execrows",
		"private_key_ciphertext = decode(repeat('00', 60), 'hex')",
		"envelope = NULL",
		"-- name: RebindDetachedRoleSource :one",
		"config_redacted = @config_redacted",
		"AND state = 'detached'",
		"source.state IN ('registered', 'active', 'error')",
	} {
		if !strings.Contains(queries, required) {
			t.Fatalf("role-source lifecycle persistence is missing %q", required)
		}
	}
	workspaceAt := strings.Index(lifecycle, "qtx.LockWorkspaceForRoleSourceMutation")
	sourceAt := strings.Index(lifecycle, "qtx.GetRoleSourceForUpdate")
	cancelScanAt := strings.Index(lifecycle, "qtx.CancelActiveRoleSourceScans")
	cancelTransferAt := strings.Index(lifecycle, "qtx.CancelActiveRoleSourceSecretTransfers")
	if workspaceAt < 0 || sourceAt <= workspaceAt || cancelScanAt <= sourceAt || cancelTransferAt <= cancelScanAt {
		t.Fatalf("lifecycle lock/cancel order is unsafe: workspace=%d source=%d scan=%d transfer=%d", workspaceAt, sourceAt, cancelScanAt, cancelTransferAt)
	}
	reportStart := strings.Index(secret, "func (c *ControlPlane) ReportSecretTransfer(")
	if reportStart < 0 {
		t.Fatal("ReportSecretTransfer is missing")
	}
	report := secret[reportStart:]
	reportSourceAt := strings.Index(report, "qtx.GetRoleSourceForUpdate")
	reportTransferAt := strings.Index(report, "qtx.GetRoleSourceSecretTransferForUpdate")
	if reportSourceAt < 0 || reportTransferAt <= reportSourceAt {
		t.Fatalf("secret transfer report does not lock source before transfer: source=%d transfer=%d", reportSourceAt, reportTransferAt)
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
