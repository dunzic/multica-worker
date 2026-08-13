package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRoleSourceMetricsReportBoundedApplyAndAuditOutcomes(t *testing.T) {
	m := NewRoleSourceMetrics()
	m.RecordApplyError("apply", "materialization", "state_conflict")
	m.RecordApplyFailureAudit("apply", "materialization", "state_conflict", "persisted")
	m.RecordApplyFailureAudit("apply", "materialization", "state_conflict", "persist_failed")
	m.RecordApplyCommitReconciliation("confirmed_succeeded")
	m.RecordRuntimeConfigAttestation("accepted_loaded")

	if got := testutil.ToFloat64(m.applyErrors.WithLabelValues("apply", "materialization", "state_conflict")); got != 1 {
		t.Fatalf("apply error metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.failureAuditWrites.WithLabelValues("apply", "materialization", "state_conflict", "persisted")); got != 1 {
		t.Fatalf("persisted audit metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.failureAuditWrites.WithLabelValues("apply", "materialization", "state_conflict", "persist_failed")); got != 1 {
		t.Fatalf("failed audit metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.commitReconciliations.WithLabelValues("confirmed_succeeded")); got != 1 {
		t.Fatalf("commit reconciliation metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.runtimeAttestations.WithLabelValues("accepted_loaded")); got != 1 {
		t.Fatalf("runtime attestation metric=%v, want 1", got)
	}
}

func TestRoleSourceMetricsCarryNoTenantOrRequestLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewRoleSourceMetrics()
	reg.MustRegister(m.Collectors()...)
	m.RecordApplyError("rollback", "commit", "internal_failure")
	m.RecordApplyFailureAudit("rollback", "commit", "internal_failure", "persist_failed")

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if _, forbidden := forbiddenMetricLabels[label.GetName()]; forbidden {
					t.Fatalf("%s has forbidden label %q", family.GetName(), label.GetName())
				}
			}
		}
	}
}

func TestRoleSourceMetricsNormalizeUnboundedCallerValues(t *testing.T) {
	m := NewRoleSourceMetrics()
	m.RecordApplyError("workspace-123", "request-456", "private database error for tenant-789")
	m.RecordApplyFailureAudit("workspace-123", "request-456", "private database error for tenant-789", "source-abc")
	m.RecordApplyCommitReconciliation("workspace-123")
	m.RecordRuntimeConfigAttestation("workspace-123")

	if got := testutil.ToFloat64(m.applyErrors.WithLabelValues("unknown", "unknown", "internal_failure")); got != 1 {
		t.Fatalf("normalized apply error metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.failureAuditWrites.WithLabelValues("unknown", "unknown", "internal_failure", "unknown")); got != 1 {
		t.Fatalf("normalized audit metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.commitReconciliations.WithLabelValues("unknown")); got != 1 {
		t.Fatalf("normalized reconciliation metric=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.runtimeAttestations.WithLabelValues("invalid")); got != 1 {
		t.Fatalf("normalized runtime attestation metric=%v, want 1", got)
	}
}

func TestRoleSourceArtifactIntegrityMetricsAreBoundedAndIdentityFree(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewRoleSourceArtifactIntegrityMetrics()
	reg.MustRegister(m.Collectors()...)
	m.RecordOutcome("workspace-private-object-key")
	m.RecordFailure("workspace-private-object-key")

	if got := testutil.ToFloat64(m.Outcomes.WithLabelValues("read_failed")); got != 1 {
		t.Fatalf("normalized integrity outcome=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("unknown")); got != 1 {
		t.Fatalf("normalized integrity failure=%v, want 1", got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if _, forbidden := forbiddenMetricLabels[label.GetName()]; forbidden {
					t.Fatalf("%s has forbidden label %q", family.GetName(), label.GetName())
				}
			}
		}
	}
}

func TestRoleSourceMetricLabelsArePartOfTheGlobalCardinalityContract(t *testing.T) {
	for metric, want := range map[string][]string{
		"multica_role_source_apply_errors_total":                 {labelMode, labelStage, labelCode},
		"multica_role_source_apply_failure_audit_writes_total":   {labelMode, labelStage, labelCode, labelOutcome},
		"multica_role_source_apply_commit_reconciliations_total": {labelOutcome},
		"multica_role_source_runtime_config_attestations_total":  {labelOutcome},
		"multica_role_source_runtime_availability":               {labelStatus},
		"multica_role_source_artifact_integrity_outcomes_total":  {labelOutcome},
		"multica_role_source_artifact_integrity_failures_total":  {labelStage},
	} {
		got, ok := operationalMetricLabels[metric]
		if !ok {
			t.Fatalf("%s is absent from operationalMetricLabels", metric)
		}
		if len(got) != len(want) {
			t.Fatalf("%s labels=%v, want %v", metric, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s labels=%v, want %v", metric, got, want)
			}
		}
	}
}

func TestRegistryExposesRoleSourceMetrics(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	if r.RoleSource == nil || r.RoleSourceArtifactGC == nil || r.RoleSourceArtifactIntegrity == nil || r.RoleSourceRetention == nil {
		t.Fatal("role-source metrics are not wired into the production registry")
	}
	r.RoleSource.RecordApplyFailureAudit("unknown", "preflight", "capacity_exhausted", "id_generation_failed")
	r.RoleSource.RecordApplyCommitReconciliation("query_failed")
	r.RoleSource.RecordRuntimeConfigAttestation("accepted_unloaded")
	r.RoleSourceArtifactGC.DeleteFailures.Inc()
	r.RoleSourceArtifactIntegrity.RecordOutcome("read_failed")
	r.RoleSourceArtifactIntegrity.RecordFailure("claim")
	r.RoleSourceArtifactIntegrity.Quarantined.Set(1)
	r.RoleSourceRetention.Failures.Inc()
	r.RoleSourceRetention.RecordOutcome("legal_hold")

	families, err := r.Gatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"multica_role_source_apply_failure_audit_writes_total":   false,
		"multica_role_source_apply_commit_reconciliations_total": false,
		"multica_role_source_runtime_config_attestations_total":  false,
		"multica_role_source_artifact_gc_delete_failures_total":  false,
		"multica_role_source_artifact_integrity_outcomes_total":  false,
		"multica_role_source_artifact_integrity_failures_total":  false,
		"multica_role_source_artifact_integrity_quarantined":     false,
		"multica_role_source_retention_failures_total":           false,
		"multica_role_source_retention_outcomes_total":           false,
	}
	for _, family := range families {
		if _, ok := wanted[family.GetName()]; ok {
			wanted[family.GetName()] = true
		}
	}
	for name, seen := range wanted {
		if !seen {
			t.Fatalf("production registry did not expose %s", name)
		}
	}
}

func TestHelmRulePagesOnMissingRoleSourceFailureEvidence(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	templateBody, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "multica", "templates", "prometheusrule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rule := string(templateBody)
	for _, required := range []string{
		"MulticaRoleSourceApplyFailureAuditWriteFailed",
		"MulticaRoleSourceApplyCommitReconciliationFailed",
		"MulticaRoleSourceArtifactGCDeleteFailures",
		"MulticaRoleSourceArtifactIntegrityQuarantined",
		"MulticaRoleSourceArtifactIntegrityReadFailures",
		"MulticaRoleSourceArtifactIntegrityWorkerFailures",
		"MulticaRoleSourceRetentionFailures",
		"MulticaRoleSourceRetentionBacklogOld",
		"MulticaRoleSourceRuntimeAttestationPersistenceFailed",
		"MulticaRoleSourceRuntimeUnavailable",
		"multica_role_source_apply_failure_audit_writes_total",
		"multica_role_source_apply_commit_reconciliations_total",
		"multica_role_source_runtime_config_attestations_total",
		"multica_role_source_runtime_availability",
		`outcome=~"persist_failed|id_generation_failed"`,
		"roleSourceAuditWriteFailureFor",
		"roleSourceSeverity",
		"roleSourceRuntimeUnavailableFor",
	} {
		if !strings.Contains(rule, required) {
			t.Fatalf("role-source alert rule is missing %q", required)
		}
	}
	for _, forbidden := range []string{"workspace_id", "source_id", "request_key", "plan_digest", "actor_user_id"} {
		if strings.Contains(rule, forbidden) {
			t.Fatalf("role-source alert uses forbidden tenant/request identifier %q", forbidden)
		}
	}

	valuesBody, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "multica", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	values := string(valuesBody)
	if !strings.Contains(values, "roleSourceAuditWriteFailureFor: 1m") ||
		!strings.Contains(values, "roleSourceCommitReconciliationFailureFor: 1m") ||
		!strings.Contains(values, "roleSourceRuntimeUnavailableFor: 10m") ||
		!strings.Contains(values, "roleSourceSeverity: critical") {
		t.Fatal("role-source audit alert defaults must stay short and critical")
	}
}
