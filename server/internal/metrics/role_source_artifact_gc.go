package metrics

import "github.com/prometheus/client_golang/prometheus"

type RoleSourceArtifactGCMetrics struct {
	Queued                      prometheus.Counter
	ObjectsDeleted              prometheus.Counter
	DeleteFailures              prometheus.Counter
	AmbiguousFailures           prometheus.Counter
	ReceiptsCompleted           prometheus.Counter
	LogicalBytesConfirmedAbsent prometheus.Counter
	Backlog                     prometheus.Gauge
	Tombstones                  prometheus.Gauge
}

func NewRoleSourceArtifactGCMetrics() *RoleSourceArtifactGCMetrics {
	return &RoleSourceArtifactGCMetrics{
		Queued:                      prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "queued_total", Help: "Unreachable or workspace-deleted artifacts durably queued for deletion."}),
		ObjectsDeleted:              prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "object_deletes_total", Help: "Successful idempotent object-storage deletes, including tombstone passes."}),
		DeleteFailures:              prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "delete_failures_total", Help: "Object-storage deletes that failed and were retained for retry."}),
		AmbiguousFailures:           prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "ambiguous_failures_total", Help: "Failed or abandoned purge attempts that may already have changed storage, making provider-operation counts lower bounds."}),
		ReceiptsCompleted:           prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "purge_receipts_total", Help: "Immutable receipts completed after the permanent-purge tombstone tail."}),
		LogicalBytesConfirmedAbsent: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "logical_bytes_confirmed_absent_total", Help: "Artifact body bytes covered by completed exact-key absence receipts; not provider billing savings."}),
		Backlog:                     prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "backlog", Help: "Active artifact deletion intents awaiting successful deletion."}),
		Tombstones:                  prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "multica", Subsystem: "role_source_artifact_gc", Name: "tombstones", Help: "Deleted artifact objects retained for scheduled late-PUT re-deletion."}),
	}
}

func (m *RoleSourceArtifactGCMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Queued, m.ObjectsDeleted, m.DeleteFailures, m.AmbiguousFailures, m.ReceiptsCompleted, m.LogicalBytesConfirmedAbsent, m.Backlog, m.Tombstones}
}
