package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
)

const roleSourceConfigReloadInterval = 5 * time.Second

const (
	roleSourceConfigReloadStatusUnloaded = "unloaded"
	roleSourceConfigReloadStatusLoaded   = "loaded"
	roleSourceConfigReloadStatusDegraded = "degraded"
)

type roleSourceConfigReloadState struct {
	Status           string
	Revision         string
	ErrorCode        string
	LastAttemptAt    time.Time
	LastSuccessfulAt time.Time
}

// RoleSourceConfigHealth is the non-secret, local-only operational view of
// managed role-source configuration. It deliberately excludes the file path,
// config IDs, allowed roots, and adapter payloads.
type RoleSourceConfigHealth struct {
	Status           string `json:"status"`
	Revision         string `json:"revision,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
	LastAttemptAt    string `json:"last_attempt_at,omitempty"`
	LastSuccessfulAt string `json:"last_successful_at,omitempty"`
}

func (d *Daemon) currentRoleSourceScanner() *roleSourceScanner {
	return d.roleSources.Load()
}

func (d *Daemon) roleSourceConfigHealth() *RoleSourceConfigHealth {
	if strings.TrimSpace(d.cfg.roleSourceConfigPath) == "" {
		return nil
	}
	d.roleSourceReloadMu.RLock()
	state := d.roleSourceReload
	d.roleSourceReloadMu.RUnlock()
	health := &RoleSourceConfigHealth{
		Status:    state.Status,
		Revision:  state.Revision,
		ErrorCode: state.ErrorCode,
	}
	if !state.LastAttemptAt.IsZero() {
		health.LastAttemptAt = state.LastAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	if !state.LastSuccessfulAt.IsZero() {
		health.LastSuccessfulAt = state.LastSuccessfulAt.UTC().Format(time.RFC3339Nano)
	}
	return health
}

func (d *Daemon) setRoleSourceReloadState(state roleSourceConfigReloadState) {
	d.roleSourceReloadMu.Lock()
	d.roleSourceReload = state
	d.roleSourceReloadMu.Unlock()
}

func (d *Daemon) roleSourceConfigReloadLoop(ctx context.Context) {
	if strings.TrimSpace(d.cfg.roleSourceConfigPath) == "" {
		return
	}
	// Reconcile once immediately in case the default config was created during
	// startup preflight, then continue with a bounded polling cadence. Polling
	// follows atomic rename across platforms without adding watcher-specific
	// failure modes or dependencies.
	d.reloadRoleSourceConfigOnce(time.Now())
	ticker := time.NewTicker(roleSourceConfigReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			d.reloadRoleSourceConfigOnce(now)
		}
	}
}

func (d *Daemon) reloadRoleSourceConfigOnce(now time.Time) {
	path := strings.TrimSpace(d.cfg.roleSourceConfigPath)
	if path == "" {
		return
	}
	current := d.currentRoleSourceScanner()
	knownRevision := ""
	if current != nil {
		knownRevision = current.configRevision
	}
	next, changed, err := loadChangedRoleSourceScanner(path, knownRevision)
	if err != nil {
		if current == nil && errors.Is(err, os.ErrNotExist) {
			d.setRoleSourceReloadState(roleSourceConfigReloadState{
				Status: roleSourceConfigReloadStatusUnloaded, LastAttemptAt: now,
			})
			return
		}
		state := roleSourceConfigReloadState{
			Status: roleSourceConfigReloadStatusDegraded, ErrorCode: roleSourceConfigReloadErrorCode(err), LastAttemptAt: now,
		}
		if current != nil {
			state.Revision = current.configRevision
			d.roleSourceReloadMu.RLock()
			state.LastSuccessfulAt = d.roleSourceReload.LastSuccessfulAt
			d.roleSourceReloadMu.RUnlock()
		}
		d.roleSourceReloadMu.RLock()
		previous := d.roleSourceReload
		d.roleSourceReloadMu.RUnlock()
		d.setRoleSourceReloadState(state)
		if previous.Status != state.Status || previous.ErrorCode != state.ErrorCode {
			message := "role source config reload rejected; scanner remains unloaded"
			if current != nil {
				message = "role source config reload rejected; retaining last-known-good configuration"
			}
			d.logger.Warn(message, "error_code", state.ErrorCode)
		}
		return
	}

	if !changed {
		// A valid restoration of the active bytes clears a prior degraded state
		// even though there is no new generation to publish.
		d.roleSourceReloadMu.RLock()
		wasDegraded := d.roleSourceReload.Status == roleSourceConfigReloadStatusDegraded
		d.roleSourceReloadMu.RUnlock()
		d.setRoleSourceReloadState(roleSourceConfigReloadState{
			Status: roleSourceConfigReloadStatusLoaded, Revision: current.configRevision,
			LastAttemptAt: now, LastSuccessfulAt: now,
		})
		if wasDegraded {
			d.logger.Info("role source configuration recovered")
		}
		return
	}

	// Publish a fully built scanner in one pointer swap. In-flight operations
	// retain their captured generation; new heartbeats and scans observe next.
	d.roleSources.Store(next)
	d.roleSourceAttestationMu.Lock()
	clear(d.roleSourceAttestationAccepted)
	d.roleSourceAttestationMu.Unlock()
	d.roleSourcePollMu.Lock()
	clear(d.roleSourceLastPoll)
	d.roleSourcePollMu.Unlock()
	d.setRoleSourceReloadState(roleSourceConfigReloadState{
		Status: roleSourceConfigReloadStatusLoaded, Revision: next.configRevision,
		LastAttemptAt: now, LastSuccessfulAt: now,
	})
	d.logger.Info("role source configuration hot-reloaded")
}

func roleSourceConfigReloadErrorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "file_missing"
	case errors.Is(err, os.ErrPermission):
		return "file_permission_denied"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "permissions") || strings.Contains(message, "non-symlink") || strings.Contains(message, "changed during secure open") {
		return "file_security_rejected"
	}
	if strings.Contains(message, "decode role source config") || strings.Contains(message, "unsupported role source config") ||
		strings.Contains(message, "invalid role source") || strings.Contains(message, "outside allowed roots") ||
		strings.Contains(message, "config count") || strings.Contains(message, "digest_key") || strings.Contains(message, "trailing json") {
		return "config_invalid"
	}
	return "file_read_failed"
}
