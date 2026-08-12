package metrics

import "github.com/prometheus/client_golang/prometheus"

// RoleSourceMetrics reports only bounded protocol states. Tenant and request
// identifiers are intentionally absent to keep series cardinality safe at
// large workspace/user counts and to keep audit identity out of telemetry.
type RoleSourceMetrics struct {
	applyErrors           *prometheus.CounterVec
	failureAuditWrites    *prometheus.CounterVec
	commitReconciliations *prometheus.CounterVec
	runtimeAttestations   *prometheus.CounterVec
}

func NewRoleSourceMetrics() *RoleSourceMetrics {
	return &RoleSourceMetrics{
		applyErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source", Name: "apply_errors_total",
			Help: "Role-source apply calls that returned an error after safe tenant/request identity was established.",
		}, []string{"mode", "stage", "code"}),
		failureAuditWrites: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source", Name: "apply_failure_audit_writes_total",
			Help: "Attempts to persist independent role-source apply-error audit evidence, partitioned by bounded outcome.",
		}, []string{"mode", "stage", "code", "outcome"}),
		commitReconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source", Name: "apply_commit_reconciliations_total",
			Help: "Independent receipt checks after a role-source apply commit returned an error.",
		}, []string{"outcome"}),
		runtimeAttestations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source", Name: "runtime_config_attestations_total",
			Help: "Bounded outcomes for daemon loaded-configuration attestation attempts.",
		}, []string{"outcome"}),
	}
}

func (m *RoleSourceMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.applyErrors, m.failureAuditWrites, m.commitReconciliations, m.runtimeAttestations}
}

func (m *RoleSourceMetrics) RecordApplyError(mode, stage, code string) {
	m.applyErrors.WithLabelValues(roleSourceMode(mode), roleSourceStage(stage), roleSourceCode(code)).Inc()
}

func (m *RoleSourceMetrics) RecordApplyFailureAudit(mode, stage, code, outcome string) {
	m.failureAuditWrites.WithLabelValues(roleSourceMode(mode), roleSourceStage(stage), roleSourceCode(code), roleSourceAuditOutcome(outcome)).Inc()
}

func (m *RoleSourceMetrics) RecordApplyCommitReconciliation(outcome string) {
	m.commitReconciliations.WithLabelValues(roleSourceCommitReconciliationOutcome(outcome)).Inc()
}

func (m *RoleSourceMetrics) RecordRuntimeConfigAttestation(outcome string) {
	m.runtimeAttestations.WithLabelValues(roleSourceRuntimeAttestationOutcome(outcome)).Inc()
}

func roleSourceMode(value string) string {
	switch value {
	case "apply", "rollback", "unknown":
		return value
	default:
		return "unknown"
	}
}

func roleSourceStage(value string) string {
	switch value {
	case "preflight", "transaction", "materialization", "finalize", "commit":
		return value
	default:
		return "unknown"
	}
}

func roleSourceCode(value string) string {
	switch value {
	case "request_cancelled", "deadline_exceeded", "capacity_exhausted", "materialization_blocked",
		"state_conflict", "invalid_request", "invalid_secret_transfer", "dependency_unavailable",
		"resource_not_found", "internal_failure":
		return value
	default:
		return "internal_failure"
	}
}

func roleSourceAuditOutcome(value string) string {
	switch value {
	case "persisted", "persist_failed", "id_generation_failed":
		return value
	default:
		return "unknown"
	}
}

func roleSourceCommitReconciliationOutcome(value string) string {
	switch value {
	case "confirmed_succeeded", "not_found", "query_failed", "conflict":
		return value
	default:
		return "unknown"
	}
}

func roleSourceRuntimeAttestationOutcome(value string) string {
	switch value {
	case "accepted_loaded", "accepted_unloaded", "invalid", "persist_failed":
		return value
	default:
		return "invalid"
	}
}
