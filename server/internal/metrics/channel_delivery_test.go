package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestChannelDeliveryMetricsNormalizeCallerValuesAndCarryNoIdentity(t *testing.T) {
	m := NewChannelDeliveryMetrics()
	m.RecordChannelDeliveryTransition("private-workspace", "private-task", "private-status", "raw provider error with token")
	m.RecordChannelDeliveryTransition("slack", "failure_notice", "delivered", "none")
	m.RecordChannelDeliveryReconcile("private-worker-error")

	if got := testutil.ToFloat64(m.transitions.WithLabelValues("unknown", "unknown", "failed", "provider_error")); got != 1 {
		t.Fatalf("normalized transition=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.transitions.WithLabelValues("slack", "failure_notice", "delivered", "none")); got != 1 {
		t.Fatalf("known transition=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.reconciles.WithLabelValues("query_failed")); got != 1 {
		t.Fatalf("normalized reconcile=%v, want 1", got)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(m.Collectors()...)
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
