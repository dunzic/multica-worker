// Package runtimehealth owns runtime freshness thresholds shared by request
// handling, background transitions and operational telemetry.
package runtimehealth

import "time"

// StaleThresholdSeconds is the DB-heartbeat age at which an online runtime is
// no longer considered available when Redis liveness cannot answer. Keep this
// strictly above the heartbeat flush interval plus scheduler and network lag.
const StaleThresholdSeconds = 150

// StaleThreshold is the duration form used by read-side classifications.
const StaleThreshold = StaleThresholdSeconds * time.Second
