package metrics

import "github.com/prometheus/client_golang/prometheus"

type RoleSourceRetentionMetrics struct {
	Queued              prometheus.Counter
	Outcomes            *prometheus.CounterVec
	Failures            prometheus.Counter
	Backlog             prometheus.Gauge
	OldestActiveSeconds prometheus.Gauge
}

func NewRoleSourceRetentionMetrics() *RoleSourceRetentionMetrics {
	return &RoleSourceRetentionMetrics{
		Queued: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source_retention", Name: "queued_total",
			Help: "Historical snapshot candidates durably queued after policy checks.",
		}),
		Outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source_retention", Name: "outcomes_total",
			Help: "Bounded outcomes from transactional historical snapshot retention checks.",
		}, []string{"outcome"}),
		Failures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "role_source_retention", Name: "failures_total",
			Help: "Retention attempts that failed before a safe prune or bounded deferral committed.",
		}),
		Backlog: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica", Subsystem: "role_source_retention", Name: "backlog",
			Help: "Pending or claimed historical snapshot candidates.",
		}),
		OldestActiveSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica", Subsystem: "role_source_retention", Name: "oldest_active_seconds",
			Help: "Age in seconds of the oldest pending or claimed retention candidate.",
		}),
	}
}

func (m *RoleSourceRetentionMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Queued, m.Outcomes, m.Failures, m.Backlog, m.OldestActiveSeconds}
}

func (m *RoleSourceRetentionMetrics) RecordOutcome(outcome string) {
	switch outcome {
	case "pruned", "policy_age", "current_snapshot", "legal_hold", "task_pin", "object_mapping",
		"active_transfer", "active_apply", "recent_plan", "rollback_reserve", "policy_disabled",
		"snapshot_missing", "state_conflict":
	default:
		outcome = "internal_failure"
	}
	m.Outcomes.WithLabelValues(outcome).Inc()
}
