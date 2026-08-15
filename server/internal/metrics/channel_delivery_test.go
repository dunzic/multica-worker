package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestChannelDeliveryMetricsNormalizeCallerValuesAndCarryNoIdentity(t *testing.T) {
	m := NewChannelDeliveryMetrics()
	m.RecordChannelDeliveryTransition("private-workspace", "private-task", "private-status", "raw provider error with token")
	m.RecordChannelDeliveryTransition("slack", "failure_notice", "delivered", "none")
	m.RecordChannelDeliveryTransition("dingtalk", "chat_reply", "ambiguous", "partial_delivery")
	m.RecordChannelDeliveryReconcile("private-worker-error")
	m.RecordChannelDeliveryReconcile("write_failed")
	m.RecordChannelDeliveryReconcile("retry_published")

	if got := testutil.ToFloat64(m.transitions.WithLabelValues("unknown", "unknown", "failed", "provider_error")); got != 1 {
		t.Fatalf("normalized transition=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.transitions.WithLabelValues("slack", "failure_notice", "delivered", "none")); got != 1 {
		t.Fatalf("known transition=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.transitions.WithLabelValues("dingtalk", "chat_reply", "ambiguous", "partial_delivery")); got != 1 {
		t.Fatalf("ambiguity transition=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.reconciles.WithLabelValues("query_failed")); got != 1 {
		t.Fatalf("normalized reconcile=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.reconciles.WithLabelValues("write_failed")); got != 1 {
		t.Fatalf("write-failed reconcile=%v, want 1", got)
	}
	if got := testutil.ToFloat64(m.reconciles.WithLabelValues("retry_published")); got != 1 {
		t.Fatalf("published retry reconcile=%v, want 1", got)
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

func TestChannelDeliveryHelmRulesSeparateFailureFromAmbiguity(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	body, err := os.ReadFile(filepath.Join(root, "deploy", "helm", "multica", "templates", "prometheusrule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rule := string(body)
	for _, required := range []string{
		"MulticaChannelDeliveryFailuresElevated",
		"MulticaChannelDeliveryAcceptanceAmbiguous",
		"MulticaChannelDeliveryReconcilerErrors",
		"MulticaChannelDeliveryAuthorizedRetryStalled",
		`status="failed"`,
		`status="ambiguous"`,
		`outcome=~"query_failed|write_failed"`,
		`outcome=~"retry_query_failed|retry_publish_failed|retry_unconsumed"`,
		"automatic resend is blocked",
	} {
		if !strings.Contains(rule, required) {
			t.Fatalf("channel-delivery alert rules are missing %q", required)
		}
	}
	if strings.Contains(rule, `status=~"failed|lease_expired"`) {
		t.Fatal("expired leases are still presented as retryable failures")
	}
}

func TestChannelDeliveryMetricsTypedNilIsNoop(t *testing.T) {
	var m *ChannelDeliveryMetrics

	if got := m.Collectors(); got != nil {
		t.Fatalf("typed-nil collectors = %#v, want nil", got)
	}
	// A typed nil pointer can be stored in the delivery metrics interface when
	// METRICS_ADDR is unset. Both background reconciliation and connector
	// transitions must remain no-ops instead of panicking the server.
	m.RecordChannelDeliveryReconcile("completed")
	m.RecordChannelDeliveryTransition("slack", "chat_reply", "delivered", "none")
}
