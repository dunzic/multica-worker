package metrics

import "github.com/prometheus/client_golang/prometheus"

type RoleSourceArtifactGCMetrics struct {
	Queued         prometheus.Counter
	ObjectsDeleted prometheus.Counter
	DeleteFailures prometheus.Counter
	Backlog        prometheus.Gauge
	Tombstones     prometheus.Gauge
}

func NewRoleSourceArtifactGCMetrics() *RoleSourceArtifactGCMetrics {
	return &RoleSourceArtifactGCMetrics{
		Queued:         prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "queued_total", Help: "Unreachable or workspace-deleted artifacts durably queued for deletion."}),
		ObjectsDeleted: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "object_deletes_total", Help: "Successful idempotent object-storage deletes, including tombstone passes."}),
		DeleteFailures: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "delete_failures_total", Help: "Object-storage deletes that failed and were retained for retry."}),
		Backlog:        prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "backlog", Help: "Active artifact deletion intents awaiting successful deletion."}),
		Tombstones:     prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "tombstones", Help: "Deleted artifact objects retained for scheduled late-PUT re-deletion."}),
	}
}

func (m *RoleSourceArtifactGCMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Queued, m.ObjectsDeleted, m.DeleteFailures, m.Backlog, m.Tombstones}
}
