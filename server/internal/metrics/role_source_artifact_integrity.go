package metrics

import "github.com/prometheus/client_golang/prometheus"

// RoleSourceArtifactIntegrityMetrics uses only bounded outcome labels. Object,
// tenant and digest identity stays in the database and never enters metrics.
type RoleSourceArtifactIntegrityMetrics struct {
	Outcomes    *prometheus.CounterVec
	Failures    *prometheus.CounterVec
	Pending     prometheus.Gauge
	Quarantined prometheus.Gauge
}

func NewRoleSourceArtifactIntegrityMetrics() *RoleSourceArtifactIntegrityMetrics {
	return &RoleSourceArtifactIntegrityMetrics{
		Outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source_artifact_integrity", Name: "outcomes_total",
			Help: "Bounded outcomes from role-source artifact body readback verification.",
		}, []string{"outcome"}),
		Failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source_artifact_integrity", Name: "failures_total",
			Help: "Bounded database and state-transition failures in artifact readback verification.",
		}, []string{"stage"}),
		Pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica", Subsystem: "role_source_artifact_integrity", Name: "pending",
			Help: "Artifact integrity rows pending or actively undergoing readback verification.",
		}),
		Quarantined: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica", Subsystem: "role_source_artifact_integrity", Name: "quarantined",
			Help: "Artifact bodies quarantined after a confirmed absence or digest/size mismatch.",
		}),
	}
}

func (m *RoleSourceArtifactIntegrityMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Outcomes, m.Failures, m.Pending, m.Quarantined}
}

func (m *RoleSourceArtifactIntegrityMetrics) RecordOutcome(outcome string) {
	switch outcome {
	case "healthy", "missing", "size_mismatch", "digest_mismatch", "read_failed":
	default:
		outcome = "read_failed"
	}
	m.Outcomes.WithLabelValues(outcome).Inc()
}

func (m *RoleSourceArtifactIntegrityMetrics) RecordFailure(stage string) {
	switch stage {
	case "claim", "complete", "quarantine", "release", "count":
	default:
		stage = "unknown"
	}
	m.Failures.WithLabelValues(stage).Inc()
}
