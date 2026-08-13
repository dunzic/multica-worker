package metrics

import "github.com/prometheus/client_golang/prometheus"

// ChannelDeliveryMetrics exposes only connector protocol states. Tenant,
// installation, task, session, correlation and provider-message identities are
// deliberately absent from labels.
type ChannelDeliveryMetrics struct {
	transitions *prometheus.CounterVec
	reconciles  *prometheus.CounterVec
}

func NewChannelDeliveryMetrics() *ChannelDeliveryMetrics {
	return &ChannelDeliveryMetrics{
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "channel_delivery", Name: "transitions_total",
			Help: "Content-free channel-delivery ledger transitions by bounded connector, operation, status and safe error code.",
		}, []string{"connector", "operation", "status", "error_code"}),
		reconciles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "channel_delivery", Name: "reconciliations_total",
			Help: "Channel-delivery expired-lease reconciliation outcomes.",
		}, []string{"outcome"}),
	}
}

func (m *ChannelDeliveryMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.transitions, m.reconciles}
}

func (m *ChannelDeliveryMetrics) RecordChannelDeliveryTransition(connector, operation, status, errorCode string) {
	m.transitions.WithLabelValues(channelDeliveryConnector(connector), channelDeliveryOperation(operation), channelDeliveryStatus(status), channelDeliveryError(errorCode)).Inc()
}

func (m *ChannelDeliveryMetrics) RecordChannelDeliveryReconcile(outcome string) {
	switch outcome {
	case "completed", "query_failed":
	default:
		outcome = "query_failed"
	}
	m.reconciles.WithLabelValues(outcome).Inc()
}

func channelDeliveryConnector(value string) string {
	switch value {
	case "slack", "dingtalk", "feishu", "wecom":
		return value
	default:
		return "unknown"
	}
}

func channelDeliveryOperation(value string) string {
	switch value {
	case "chat_reply", "failure_notice":
		return value
	default:
		return "unknown"
	}
}

func channelDeliveryStatus(value string) string {
	switch value {
	case "delivered", "readback", "failed", "lease_expired":
		return value
	default:
		return "failed"
	}
}

func channelDeliveryError(value string) string {
	switch value {
	case "none", "timeout", "authorization", "rate_limited", "provider_error", "delivery_state_conflict":
		return value
	default:
		return "provider_error"
	}
}
