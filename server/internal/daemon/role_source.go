package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/rolesource"
	"github.com/multica-ai/multica/server/internal/rolesource/agentwaker"
	"github.com/multica-ai/multica/server/internal/rolesource/manifestdir"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	roleSourceConfigVersion   = 1
	maxRoleSourceConfigBytes  = 1 << 20
	maxRoleSourceConfigs      = 512
	maxRoleSourceRoots        = 64
	roleSourceScanConcurrency = 2
)

var roleSourceRecoveryPollInterval = 5 * time.Minute

const (
	roleSourceLeaseRenewAhead = 5 * time.Minute
	roleSourceReportReserve   = 30 * time.Second
	roleSourceRenewRetry      = 15 * time.Second
)

var roleSourceConfigIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type roleSourceConfigDocument struct {
	Version      int                                `json:"version"`
	DigestKey    []byte                             `json:"digest_key"`
	AllowedRoots []string                           `json:"allowed_roots"`
	Sources      map[string]RoleSourceManagedSource `json:"sources"`
}

// RoleSourceManagedSource is the source-neutral local adapter configuration
// accepted by the managed daemon configuration API. Config is intentionally
// opaque here; the registered adapter owns strict decoding and validation.
type RoleSourceManagedSource struct {
	Kind   rolesource.Kind `json:"kind"`
	Config json.RawMessage `json:"config"`
}

type roleSourceLocalConfig = RoleSourceManagedSource

// roleSourceScanner owns local source authority. The control plane only sends
// an opaque config ID; resolving it to a path and invoking a filesystem adapter
// happens exclusively in this daemon process.
type roleSourceScanner struct {
	registry       *rolesource.Registry
	configs        map[string]roleSourceLocalConfig
	configRevision string
	semaphore      chan struct{}
}

func loadRoleSourceScanner(configPath string) (*roleSourceScanner, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, nil
	}
	body, err := readRoleSourceConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	defer clear(body)
	document, err := decodeRoleSourceConfigDocument(body)
	if err != nil {
		return nil, err
	}
	defer clear(document.DigestKey)
	scanner, err := buildRoleSourceScanner(document)
	if err != nil {
		return nil, err
	}
	scanner.configRevision = roleSourceConfigRevision(body)
	return scanner, nil
}

func (s *roleSourceScanner) attestationForRuntime(runtimeID string) (protocol.RoleSourceConfigAttestation, error) {
	if s == nil {
		return protocol.NewRoleSourceConfigAttestation(false, "", nil)
	}
	attestedSources := make([]protocol.RoleSourceLoadedConfig, 0, len(s.configs))
	for configID, config := range s.configs {
		descriptor, ok := s.registry.Descriptor(config.Kind)
		if !ok {
			return protocol.RoleSourceConfigAttestation{}, fmt.Errorf("role source config %q adapter descriptor is unavailable", configID)
		}
		configIDDigest, err := protocol.RoleSourceConfigIDDigest(runtimeID, configID)
		if err != nil {
			return protocol.RoleSourceConfigAttestation{}, err
		}
		attestedSources = append(attestedSources, protocol.RoleSourceLoadedConfig{
			ConfigIDDigest: configIDDigest, Kind: string(config.Kind), AdapterVersion: descriptor.AdapterVersion,
		})
	}
	sort.Slice(attestedSources, func(i, j int) bool { return attestedSources[i].ConfigIDDigest < attestedSources[j].ConfigIDDigest })
	revision, err := protocol.RoleSourceConfigRevisionDigest(runtimeID, s.configRevision)
	if err != nil {
		return protocol.RoleSourceConfigAttestation{}, err
	}
	return protocol.NewRoleSourceConfigAttestation(true, revision, attestedSources)
}

func readRoleSourceConfigFile(configPath string) ([]byte, error) {
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath {
		return nil, errors.New("role source config file must be a clean absolute path")
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		return nil, fmt.Errorf("inspect role source config file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("role source config file must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("role source config file permissions must be 0600 or stricter")
	}
	if info.Size() <= 0 || info.Size() > maxRoleSourceConfigBytes {
		return nil, errors.New("role source config file size is outside the allowed range")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("open role source config file: %w", err)
	}
	defer file.Close() //nolint:errcheck
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened role source config file: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("role source config file changed during secure open")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxRoleSourceConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read role source config file: %w", err)
	}
	if len(body) > maxRoleSourceConfigBytes {
		return nil, errors.New("role source config file exceeds size limit")
	}
	return body, nil
}

func decodeRoleSourceConfigDocument(body []byte) (roleSourceConfigDocument, error) {
	var document roleSourceConfigDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		clear(document.DigestKey)
		return roleSourceConfigDocument{}, fmt.Errorf("decode role source config file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		clear(document.DigestKey)
		return roleSourceConfigDocument{}, errors.New("role source config file contains trailing JSON")
	}
	return document, nil
}

func buildRoleSourceScanner(document roleSourceConfigDocument) (*roleSourceScanner, error) {
	if document.Version != roleSourceConfigVersion {
		return nil, fmt.Errorf("unsupported role source config version %d", document.Version)
	}
	if len(document.Sources) == 0 || len(document.Sources) > maxRoleSourceConfigs {
		return nil, fmt.Errorf("role source config count must be between 1 and %d", maxRoleSourceConfigs)
	}
	requiresAgentWaker := false
	for _, config := range document.Sources {
		if config.Kind == agentwaker.Kind {
			requiresAgentWaker = true
			break
		}
	}
	key := append([]byte(nil), document.DigestKey...)
	defer clear(key)
	if (requiresAgentWaker || len(key) != 0) && len(key) != 32 {
		return nil, errors.New("role source digest_key must be exactly 32 base64-encoded bytes when AgentWaker is configured")
	}
	allowedRoots, err := validateRoleSourceAllowedRoots(document.AllowedRoots)
	if err != nil {
		return nil, err
	}
	rootValidator := roleSourceRootValidator(allowedRoots)
	manifestDirectoryAdapter, err := manifestdir.New(rootValidator)
	if err != nil {
		return nil, err
	}
	adapters := []rolesource.Adapter{manifestDirectoryAdapter}
	if requiresAgentWaker {
		agentWakerAdapter, err := agentwaker.New(key, rootValidator)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, agentWakerAdapter)
	}
	registry, err := rolesource.NewRegistry(adapters...)
	if err != nil {
		return nil, err
	}
	configs := make(map[string]roleSourceLocalConfig, len(document.Sources))
	for configID, config := range document.Sources {
		if !roleSourceConfigIDPattern.MatchString(configID) {
			return nil, fmt.Errorf("invalid role source config id %q", configID)
		}
		if _, ok := registry.Descriptor(config.Kind); !ok {
			return nil, fmt.Errorf("unsupported local role source kind %q", config.Kind)
		}
		if len(config.Config) == 0 || len(config.Config) > 64<<10 {
			return nil, fmt.Errorf("role source config %q has invalid size", configID)
		}
		if _, err := registry.RedactConfig(config.Kind, config.Config); err != nil {
			return nil, fmt.Errorf("validate role source config %q: %w", configID, err)
		}
		configs[configID] = roleSourceLocalConfig{Kind: config.Kind, Config: append(json.RawMessage(nil), config.Config...)}
	}
	return &roleSourceScanner{registry: registry, configs: configs, semaphore: make(chan struct{}, roleSourceScanConcurrency)}, nil
}

func (s *roleSourceScanner) acquire(ctx context.Context) bool {
	if s == nil {
		return false
	}
	select {
	case s.semaphore <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *roleSourceScanner) tryAcquire() bool {
	if s == nil {
		return false
	}
	select {
	case s.semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *roleSourceScanner) release() {
	<-s.semaphore
}

func validateRoleSourceAllowedRoots(raw []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > maxRoleSourceRoots {
		return nil, fmt.Errorf("role source allowed_roots count must be between 1 and %d", maxRoleSourceRoots)
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, candidate := range raw {
		candidate = strings.TrimSpace(candidate)
		if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate || candidate == string(filepath.Separator) {
			return nil, fmt.Errorf("role source allowed root %q must be a clean, non-root absolute path", candidate)
		}
		resolved, err := canonicalRoleSourceDirectory(candidate)
		if err != nil {
			return nil, fmt.Errorf("validate role source allowed root %q: %w", candidate, err)
		}
		if resolved != candidate {
			return nil, fmt.Errorf("role source allowed root %q contains a symlink", candidate)
		}
		if !seen[resolved] {
			seen[resolved] = true
			result = append(result, resolved)
		}
	}
	return result, nil
}

func roleSourceRootValidator(allowedRoots []string) func(string) error {
	return func(candidate string) error {
		resolved, err := canonicalRoleSourceDirectory(candidate)
		if err != nil {
			return err
		}
		if resolved != candidate {
			return errors.New("role source root contains a symlink")
		}
		for _, allowed := range allowedRoots {
			relative, err := filepath.Rel(allowed, resolved)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil
			}
		}
		return errors.New("role source root is outside the configured allowed roots")
	}
}

func canonicalRoleSourceDirectory(candidate string) (string, error) {
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("role source path must be a non-symlink directory")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func (s *roleSourceScanner) scan(ctx context.Context, pending protocol.DaemonHeartbeatPendingRoleSourceScan) (rolesource.Snapshot, string) {
	if s == nil {
		return rolesource.Snapshot{}, "scanner_unavailable"
	}
	config, ok := s.configs[pending.DaemonConfigID]
	if !ok {
		return rolesource.Snapshot{}, "config_not_found"
	}
	descriptor, ok := s.registry.Descriptor(rolesource.Kind(pending.Kind))
	if !ok || config.Kind != rolesource.Kind(pending.Kind) {
		return rolesource.Snapshot{}, "adapter_not_supported"
	}
	if descriptor.AdapterVersion != pending.AdapterVersion {
		return rolesource.Snapshot{}, "adapter_version_mismatch"
	}
	snapshot, err := s.registry.Scan(ctx, rolesource.Kind(pending.Kind), rolesource.ScanRequest{
		WorkspaceID: pending.WorkspaceID, SourceID: pending.SourceID, Config: config.Config,
		PreviousSnapshotDigest: pending.PreviousSnapshotDigest, RequestedAt: time.Now().UTC(),
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return rolesource.Snapshot{}, "scan_timeout"
		}
		return rolesource.Snapshot{}, "source_invalid"
	}
	return snapshot, ""
}

func (s *roleSourceScanner) openArtifact(ctx context.Context, pending protocol.DaemonHeartbeatPendingRoleSourceScan, ref rolesource.ArtifactRef) (io.ReadCloser, error) {
	if s == nil {
		return nil, errors.New("role source scanner is unavailable")
	}
	config, ok := s.configs[pending.DaemonConfigID]
	if !ok || config.Kind != rolesource.Kind(pending.Kind) {
		return nil, errors.New("role source artifact configuration is unavailable")
	}
	return s.registry.OpenArtifact(ctx, rolesource.Kind(pending.Kind), rolesource.ScanRequest{
		WorkspaceID: pending.WorkspaceID, SourceID: pending.SourceID, Config: config.Config,
		PreviousSnapshotDigest: pending.PreviousSnapshotDigest, RequestedAt: time.Now().UTC(),
	}, ref)
}

func (s *roleSourceScanner) sealSecretTransfer(ctx context.Context, pending protocol.DaemonHeartbeatPendingRoleSourceSecretTransfer) (rolesource.SecretEnvelope, string) {
	if s == nil {
		return rolesource.SecretEnvelope{}, "scanner_unavailable"
	}
	config, ok := s.configs[pending.DaemonConfigID]
	if !ok {
		return rolesource.SecretEnvelope{}, "config_not_found"
	}
	kind := rolesource.Kind(pending.Kind)
	descriptor, ok := s.registry.Descriptor(kind)
	if !ok || config.Kind != kind {
		return rolesource.SecretEnvelope{}, "adapter_not_supported"
	}
	if descriptor.AdapterVersion != pending.AdapterVersion || pending.ContractVersion != rolesource.SecretEnvelopeContractVersion {
		return rolesource.SecretEnvelope{}, "adapter_version_mismatch"
	}
	request := rolesource.ScanRequest{
		WorkspaceID: pending.WorkspaceID, SourceID: pending.SourceID, Config: config.Config, RequestedAt: time.Now().UTC(),
	}
	snapshot, err := s.registry.Scan(ctx, kind, request)
	if err != nil {
		if ctx.Err() != nil {
			return rolesource.SecretEnvelope{}, "secret_export_timeout"
		}
		return rolesource.SecretEnvelope{}, "source_invalid"
	}
	if snapshot.SnapshotDigest != pending.SnapshotDigest {
		return rolesource.SecretEnvelope{}, "snapshot_changed"
	}
	payload, err := s.registry.ExportSecretPayload(ctx, kind, request, snapshot, pending.RoleID)
	if err != nil {
		return rolesource.SecretEnvelope{}, "secret_export_failed"
	}
	defer rolesource.ClearSecretEnvelopePayload(&payload)
	claims := rolesource.SecretEnvelopeClaims{
		ContractVersion: pending.ContractVersion, TransferID: pending.TransferID,
		WorkspaceID: pending.WorkspaceID, SourceID: pending.SourceID, RoleID: pending.RoleID,
		SnapshotDigest: pending.SnapshotDigest, ExpiresAt: pending.ExpiresAt,
	}
	envelope, err := rolesource.SealSecretEnvelope(pending.PublicKey, claims, payload)
	if err != nil {
		if ctx.Err() != nil {
			return rolesource.SecretEnvelope{}, "secret_export_timeout"
		}
		return rolesource.SecretEnvelope{}, "envelope_seal_failed"
	}
	return envelope, ""
}

func (d *Daemon) roleSourceHeartbeatOptions(runtimeID string, forcePoll bool) HeartbeatOptions {
	option := HeartbeatOptions{
		SupportsRoleSourceConfigAttestation: true,
		RoleSourceConfigAttestation:         d.pendingRoleSourceConfigAttestation(runtimeID),
	}
	if d.roleSources == nil {
		return option
	}
	option.SupportsRoleSourceScan = true
	option.SupportsRoleSourceSecretTransfer = true
	now := time.Now()
	d.roleSourcePollMu.Lock()
	last := d.roleSourceLastPoll[runtimeID]
	if (forcePoll || last.IsZero() || now.Sub(last) >= roleSourceRecoveryPollInterval) && d.roleSources.tryAcquire() {
		d.roleSourceLastPoll[runtimeID] = now
		option.PollRoleSourceScan = true
		option.PollRoleSourceSecretTransfer = true
	}
	d.roleSourcePollMu.Unlock()
	return option
}

func (d *Daemon) pendingRoleSourceConfigAttestation(runtimeID string) *protocol.RoleSourceConfigAttestation {
	attestation, err := d.roleSources.attestationForRuntime(runtimeID)
	if err != nil {
		d.logger.Warn("build role source config attestation failed", "runtime_id", runtimeID, "error", err)
		return nil
	}
	d.roleSourceAttestationMu.Lock()
	defer d.roleSourceAttestationMu.Unlock()
	if d.roleSourceAttestationAccepted == nil {
		d.roleSourceAttestationAccepted = make(map[string]string)
	}
	if d.roleSourceAttestationAccepted[runtimeID] == attestation.AttestationID {
		return nil
	}
	attestation.Sources = append([]protocol.RoleSourceLoadedConfig(nil), attestation.Sources...)
	return &attestation
}

func (d *Daemon) acceptRoleSourceConfigAttestation(runtimeID, attestationID string) {
	if runtimeID == "" || attestationID == "" {
		return
	}
	attestation, err := d.roleSources.attestationForRuntime(runtimeID)
	if err != nil || attestationID != attestation.AttestationID {
		return
	}
	d.roleSourceAttestationMu.Lock()
	defer d.roleSourceAttestationMu.Unlock()
	if d.roleSourceAttestationAccepted == nil {
		d.roleSourceAttestationAccepted = make(map[string]string)
	}
	d.roleSourceAttestationAccepted[runtimeID] = attestationID
}

func (d *Daemon) releaseRoleSourcePollReservation(option HeartbeatOptions) {
	if (option.PollRoleSourceScan || option.PollRoleSourceSecretTransfer) && d.roleSources != nil {
		d.roleSources.release()
	}
}

func (d *Daemon) handleRoleSourceSecretTransfer(ctx context.Context, runtimeID string, pending PendingRoleSourceSecretTransfer, pollReserved bool) {
	if d.roleSources == nil {
		return
	}
	if !pollReserved && !d.roleSources.acquire(ctx) {
		return
	}
	defer func() {
		d.roleSources.release()
		go d.handlePendingWorkHint(runtimeID, protocol.PendingWorkKindRoleSourceSecretTransfer)
	}()
	leaseExpiresAt, err := time.Parse(time.RFC3339Nano, pending.LeaseExpiresAt)
	if err != nil || !leaseExpiresAt.After(time.Now()) {
		d.logger.Warn("role source secret transfer has invalid lease expiry", "runtime_id", runtimeID, "transfer_id", pending.TransferID)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, pending.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) || expiresAt.Before(leaseExpiresAt) {
		d.logger.Warn("role source secret transfer has invalid challenge expiry", "runtime_id", runtimeID, "transfer_id", pending.TransferID)
		return
	}
	transferCtx, cancel := context.WithDeadline(ctx, leaseExpiresAt)
	envelope, errorCode := d.roleSources.sealSecretTransfer(transferCtx, pending)
	cancel()
	result := RoleSourceSecretTransferResult{LeaseToken: pending.LeaseToken}
	if errorCode == "" {
		result.Status = "completed"
		result.Envelope = &envelope
	} else {
		result.Status = "failed"
		result.ErrorCode = errorCode
	}
	reportCtx, cancelReport := context.WithDeadline(d.recoveryContext(), leaseExpiresAt)
	err = d.client.ReportRoleSourceSecretTransferResult(reportCtx, runtimeID, pending, result)
	cancelReport()
	if err != nil {
		d.logger.Warn("role source secret transfer report failed", "runtime_id", runtimeID, "transfer_id", pending.TransferID, "status", result.Status, "error_code", result.ErrorCode, "error", err)
		return
	}
	d.logger.Info("role source secret transfer completed", "runtime_id", runtimeID, "transfer_id", pending.TransferID, "status", result.Status, "error_code", result.ErrorCode)
}

func (d *Daemon) resetRoleSourceRecoveryPoll(runtimeID string) {
	if d.roleSources == nil {
		return
	}
	d.roleSourcePollMu.Lock()
	delete(d.roleSourceLastPoll, runtimeID)
	d.roleSourcePollMu.Unlock()
}

func (d *Daemon) handleRoleSourceScan(ctx context.Context, runtimeID string, pending PendingRoleSourceScan, pollReserved bool) {
	if d.roleSources == nil {
		return
	}
	if !pollReserved && !d.roleSources.acquire(ctx) {
		return
	}
	defer func() {
		d.roleSources.release()
		// Drain another queued source promptly now that a bounded scan slot is
		// available. The empty follow-up poll is cheap and does not recur.
		go d.handlePendingWorkHint(runtimeID, protocol.PendingWorkKindRoleSourceScan)
	}()

	leaseExpiresAt, err := time.Parse(time.RFC3339Nano, pending.LeaseExpiresAt)
	if err != nil {
		d.logger.Warn("role source scan command has invalid lease expiry", "runtime_id", runtimeID, "request_id", pending.RequestID)
		return
	}
	if !leaseExpiresAt.After(time.Now()) {
		d.logger.Info("role source scan lease already expired", "runtime_id", runtimeID, "request_id", pending.RequestID)
		return
	}
	lease := &roleSourceLeaseState{expiresAt: leaseExpiresAt}
	scanCtx, cancelScan := context.WithCancel(ctx)
	scanDone := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		d.renewRoleSourceScanLease(scanCtx, cancelScan, runtimeID, pending, lease, scanDone)
	}()
	snapshot, errorCode := d.roleSources.scan(scanCtx, pending)
	if errorCode == "" {
		errorCode = d.uploadRoleSourceArtifacts(scanCtx, runtimeID, pending, snapshot)
	}
	close(scanDone)
	cancelScan()
	<-renewDone

	result := RoleSourceScanResult{LeaseToken: pending.LeaseToken}
	if errorCode == "" {
		result.Status = "completed"
		result.Snapshot = &snapshot
	} else {
		result.Status = "failed"
		result.ErrorCode = errorCode
	}
	reportCtx, cancelReport := context.WithDeadline(d.recoveryContext(), lease.expires())
	err = d.client.ReportRoleSourceScanResult(reportCtx, runtimeID, pending, result)
	cancelReport()
	if err != nil {
		d.logger.Warn("role source scan terminal report failed",
			"runtime_id", runtimeID, "request_id", pending.RequestID, "status", result.Status, "error_code", result.ErrorCode, "error", err)
		return
	}
	d.logger.Info("role source scan completed",
		"runtime_id", runtimeID, "request_id", pending.RequestID, "status", result.Status,
		"snapshot_digest", snapshot.SnapshotDigest, "error_code", result.ErrorCode)
}

func (d *Daemon) uploadRoleSourceArtifacts(ctx context.Context, runtimeID string, pending PendingRoleSourceScan, snapshot rolesource.Snapshot) string {
	refs, err := rolesource.CollectArtifactRefs(snapshot)
	if err != nil {
		return "artifact_manifest_invalid"
	}
	for start := 0; start < len(refs); start += 1_000 {
		end := start + 1_000
		if end > len(refs) {
			end = len(refs)
		}
		missing, err := d.client.CheckRoleSourceArtifacts(ctx, runtimeID, pending, refs[start:end])
		if err != nil {
			return "artifact_preflight_failed"
		}
		expected := make(map[string]rolesource.ArtifactRef, end-start)
		for _, ref := range refs[start:end] {
			expected[ref.Digest] = ref
		}
		for _, ref := range missing {
			want, ok := expected[ref.Digest]
			if !ok || want != ref {
				return "artifact_preflight_invalid"
			}
			if err := d.uploadRoleSourceArtifactWithRetry(ctx, runtimeID, pending, ref); err != nil {
				if errors.Is(err, rolesource.ErrChangedDuringRead) {
					return "source_changed"
				}
				return "artifact_upload_failed"
			}
		}
	}
	return ""
}

func (d *Daemon) uploadRoleSourceArtifactWithRetry(ctx context.Context, runtimeID string, pending PendingRoleSourceScan, ref rolesource.ArtifactRef) error {
	var lastErr error
	for attempt, delay := range []time.Duration{0, time.Second, 2 * time.Second} {
		if delay > 0 {
			if err := retrySleep(ctx, delay); err != nil {
				return lastErr
			}
		}
		body, err := d.roleSources.openArtifact(ctx, pending, ref)
		if err != nil {
			return err
		}
		err = d.client.UploadRoleSourceArtifact(ctx, runtimeID, pending, ref, body)
		closeErr := body.Close()
		if err == nil && closeErr == nil {
			return nil
		}
		if err == nil {
			err = closeErr
		}
		lastErr = err
		if !isTransientError(err) || attempt == 2 {
			return err
		}
	}
	return lastErr
}

type roleSourceLeaseState struct {
	mu        sync.RWMutex
	expiresAt time.Time
}

func (s *roleSourceLeaseState) expires() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.expiresAt
}

func (s *roleSourceLeaseState) set(expiresAt time.Time) {
	s.mu.Lock()
	s.expiresAt = expiresAt
	s.mu.Unlock()
}

func (d *Daemon) renewRoleSourceScanLease(ctx context.Context, cancelScan context.CancelFunc, runtimeID string, pending PendingRoleSourceScan, lease *roleSourceLeaseState, scanDone <-chan struct{}) {
	for {
		expiresAt := lease.expires()
		renewAt := expiresAt.Add(-roleSourceLeaseRenewAhead)
		wait := time.Until(renewAt)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-scanDone:
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		renewCtx, cancelRenew := context.WithTimeout(d.recoveryContext(), 10*time.Second)
		response, err := d.client.RenewRoleSourceScanLease(renewCtx, runtimeID, pending)
		cancelRenew()
		if err == nil {
			newExpiry, parseErr := time.Parse(time.RFC3339Nano, response.LeaseExpiresAt)
			if parseErr == nil && newExpiry.After(expiresAt) {
				lease.set(newExpiry)
				continue
			}
			err = errors.New("server returned an invalid role source lease expiry")
		}
		if time.Until(expiresAt) <= roleSourceReportReserve {
			d.logger.Warn("role source scan lease renewal exhausted", "runtime_id", runtimeID, "request_id", pending.RequestID)
			cancelScan()
			return
		}
		d.logger.Debug("role source scan lease renewal will retry", "runtime_id", runtimeID, "request_id", pending.RequestID, "error", err)
		retryTimer := time.NewTimer(roleSourceRenewRetry)
		select {
		case <-scanDone:
			retryTimer.Stop()
			return
		case <-ctx.Done():
			retryTimer.Stop()
			return
		case <-retryTimer.C:
		}
	}
}
