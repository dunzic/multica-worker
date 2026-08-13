package rolesource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var (
	ErrScanAlreadyActive = errors.New("role source already has an active scan")
	ErrScanLeaseLost     = errors.New("role source scan lease is stale or no longer owned")
	ErrInvalidScanReport = errors.New("invalid role source scan report")
	ErrScanSourceState   = errors.New("role source state does not accept scans")
)

type controlPlaneDB interface {
	db.DBTX
	Begin(context.Context) (pgx.Tx, error)
}

// ControlPlane owns source registration, durable scan work and immutable scan
// reports and approved materialization. Every successful mutation writes its
// audit event in the same database transaction.
type ControlPlane struct {
	database         controlPlaneDB
	catalog          DescriptorProvider
	artifacts        ArtifactReader
	secretBoxes      map[string]SecretBox
	secretKeyID      string
	materializeSlots chan struct{}
	applyMetrics     ApplyMetrics
	now              func() time.Time
}

// ApplyMetrics accepts bounded operational labels only. Implementations must
// never attach workspace, source, actor, plan, approval or request identifiers
// to metric series; those belong in the tenant-scoped audit ledger and logs.
type ApplyMetrics interface {
	RecordApplyError(mode, stage, code string)
	RecordApplyFailureAudit(mode, stage, code, outcome string)
	RecordApplyCommitReconciliation(outcome string)
}

type ArtifactReader interface {
	GetReader(context.Context, string) (io.ReadCloser, error)
}

func NewControlPlane(database controlPlaneDB, catalog DescriptorProvider) (*ControlPlane, error) {
	if database == nil || catalog == nil {
		return nil, errors.New("role source control plane requires database and adapter catalog")
	}
	return &ControlPlane{
		database: database, catalog: catalog, materializeSlots: make(chan struct{}, 8), now: time.Now,
	}, nil
}

func (c *ControlPlane) SetArtifactReader(reader ArtifactReader) {
	c.artifacts = reader
}

func (c *ControlPlane) SetApplyMetrics(metrics ApplyMetrics) {
	c.applyMetrics = metrics
}

type SecretBox interface {
	SealWithAAD([]byte, []byte) ([]byte, error)
	OpenWithAAD([]byte, []byte) ([]byte, error)
}

func (c *ControlPlane) SetSecretBox(box SecretBox, keyID string) error {
	if box == nil || !kindPattern.MatchString(keyID) {
		return errors.New("role source secret box requires a valid key id")
	}
	if c.secretBoxes == nil {
		c.secretBoxes = make(map[string]SecretBox)
	}
	c.secretBoxes[keyID] = box
	c.secretKeyID = keyID
	return nil
}

func (c *ControlPlane) AddSecretDecryptionBox(box SecretBox, keyID string) error {
	if box == nil || !kindPattern.MatchString(keyID) {
		return errors.New("role source secret box requires a valid key id")
	}
	if c.secretBoxes == nil {
		c.secretBoxes = make(map[string]SecretBox)
	}
	if _, exists := c.secretBoxes[keyID]; exists {
		return errors.New("role source secret key id is duplicated")
	}
	c.secretBoxes[keyID] = box
	return nil
}

func (c *ControlPlane) secretBoxFor(keyID string) (SecretBox, bool) {
	box, ok := c.secretBoxes[keyID]
	return box, ok
}

type RegisterSourceInput struct {
	WorkspaceID    string
	RuntimeID      string
	ActorUserID    string
	Name           string
	Kind           Kind
	AdapterVersion string
	DaemonConfigID string
	ConfigSummary  ConfigSummary
	Policy         json.RawMessage
}

func (c *ControlPlane) RegisterSource(ctx context.Context, input RegisterSourceInput) (db.RoleSource, error) {
	descriptor, ok := c.catalog.Descriptor(input.Kind)
	if !ok {
		return db.RoleSource{}, fmt.Errorf("%w: %s", ErrAdapterNotFound, input.Kind)
	}
	if descriptor.AdapterVersion != input.AdapterVersion {
		return db.RoleSource{}, fmt.Errorf("adapter version %q does not match registered version %q", input.AdapterVersion, descriptor.AdapterVersion)
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 200 || strings.TrimSpace(input.DaemonConfigID) == "" {
		return db.RoleSource{}, errors.New("role source requires bounded name and daemon_config_id")
	}
	if _, err := protocol.RoleSourceConfigIDDigest(input.RuntimeID, strings.TrimSpace(input.DaemonConfigID)); err != nil {
		return db.RoleSource{}, fmt.Errorf("invalid daemon_config_id: %w", err)
	}
	if err := validateConfigSummary(&input.ConfigSummary); err != nil {
		return db.RoleSource{}, err
	}
	policy, err := canonicalJSONObject(input.Policy, 64<<10)
	if err != nil {
		return db.RoleSource{}, fmt.Errorf("validate role source policy: %w", err)
	}
	configSummary, err := json.Marshal(input.ConfigSummary)
	if err != nil {
		return db.RoleSource{}, err
	}
	workspaceID, runtimeID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.RuntimeID, input.ActorUserID)
	if err != nil {
		return db.RoleSource{}, err
	}
	sourceID, err := newPGUUID()
	if err != nil {
		return db.RoleSource{}, err
	}

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSource{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSource{}, err
	}
	if _, err := qtx.LockRoleSourceRuntimeForRegistration(ctx, db.LockRoleSourceRuntimeForRegistrationParams{RuntimeID: runtimeID, WorkspaceID: workspaceID}); err != nil {
		return db.RoleSource{}, fmt.Errorf("validate role source runtime: %w", err)
	}
	source, err := qtx.CreateRoleSource(ctx, db.CreateRoleSourceParams{
		ID: sourceID, WorkspaceID: workspaceID, RuntimeID: runtimeID,
		Name: strings.TrimSpace(input.Name), Kind: string(input.Kind), AdapterVersion: input.AdapterVersion,
		DaemonConfigID: strings.TrimSpace(input.DaemonConfigID), ConfigRedacted: configSummary, Policy: policy, ActorUserID: actorID,
	})
	if err != nil {
		return db.RoleSource{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "source_registered", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		AdapterKind: input.Kind, AdapterVersion: input.AdapterVersion, Result: "succeeded",
	}); err != nil {
		return db.RoleSource{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSource{}, err
	}
	return source, nil
}

func (c *ControlPlane) RequestScan(ctx context.Context, workspaceIDText, sourceIDText, actorUserIDText, requestKey string) (db.RoleSourceScanRequest, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if !validLegalHoldRequestKey(requestKey) {
		return db.RoleSourceScanRequest{}, false, fmt.Errorf("invalid role source scan request key")
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(workspaceIDText, sourceIDText, actorUserIDText)
	if err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	requestID, err := newPGUUID()
	if err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	requestKeyDigest := roleSourceRequestKeyDigest(requestKey)
	if existing, getErr := qtx.GetRoleSourceScanRequestByRequestKey(ctx, db.GetRoleSourceScanRequestByRequestKeyParams{
		SourceID: sourceID, WorkspaceID: workspaceID, RequestKeyDigest: pgtype.Text{String: requestKeyDigest, Valid: true},
	}); getErr == nil {
		if existing.RequestedBy != actorID {
			return db.RoleSourceScanRequest{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return db.RoleSourceScanRequest{}, false, getErr
	}
	if source.State == "detached" || source.State == "paused" {
		return db.RoleSourceScanRequest{}, false, fmt.Errorf("%w: %q", ErrScanSourceState, source.State)
	}
	request, err := qtx.CreateRoleSourceScanRequest(ctx, db.CreateRoleSourceScanRequestParams{
		ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID, RequestedBy: actorID,
		ExpectedAdapterVersion: source.AdapterVersion,
		RequestKeyDigest:       pgtype.Text{String: requestKeyDigest, Valid: true},
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return db.RoleSourceScanRequest{}, false, ErrScanAlreadyActive
		}
		return db.RoleSourceScanRequest{}, false, err
	}
	if err := c.appendAudit(ctx, qtx, source, "scan_queued", AuditActor{Type: "user", ID: actorUserIDText}, AuditPayload{
		OperationID: util.UUIDToString(request.ID), AdapterKind: Kind(source.Kind), AdapterVersion: source.AdapterVersion, Result: "queued",
	}); err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceScanRequest{}, false, err
	}
	return request, true, nil
}

type ClaimedScan struct {
	Request db.RoleSourceScanRequest
	Source  db.RoleSource
}

func (c *ControlPlane) ClaimNextScan(ctx context.Context, runtimeIDText string, leaseDuration time.Duration) (ClaimedScan, error) {
	if leaseDuration < 15*time.Second || leaseDuration > 15*time.Minute {
		return ClaimedScan{}, errors.New("scan lease duration must be between 15 seconds and 15 minutes")
	}
	runtimeID, err := util.ParseUUID(runtimeIDText)
	if err != nil {
		return ClaimedScan{}, err
	}
	leaseToken, err := newPGUUID()
	if err != nil {
		return ClaimedScan{}, err
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return ClaimedScan{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	runtime, err := qtx.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return ClaimedScan{}, err
	}
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, runtime.WorkspaceID); err != nil {
		return ClaimedScan{}, err
	}
	request, err := qtx.ClaimNextRoleSourceScan(ctx, db.ClaimNextRoleSourceScanParams{
		RuntimeID: runtimeID, LeaseToken: leaseToken,
		LeaseDuration: pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true},
	})
	if err != nil {
		return ClaimedScan{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: request.SourceID, WorkspaceID: request.WorkspaceID})
	if err != nil {
		return ClaimedScan{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "scan_claimed", AuditActor{Type: "runtime", ID: runtimeIDText}, AuditPayload{
		OperationID: util.UUIDToString(request.ID), AdapterKind: Kind(source.Kind), AdapterVersion: source.AdapterVersion, Result: "claimed",
	}); err != nil {
		return ClaimedScan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedScan{}, err
	}
	return ClaimedScan{Request: request, Source: source}, nil
}

func (c *ControlPlane) ListSources(ctx context.Context, workspaceIDText string) ([]db.RoleSource, error) {
	workspaceID, err := util.ParseUUID(workspaceIDText)
	if err != nil {
		return nil, err
	}
	return c.queries().ListRoleSourcesInWorkspace(ctx, workspaceID)
}

func (c *ControlPlane) GetSource(ctx context.Context, workspaceIDText, sourceIDText string) (db.RoleSource, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return db.RoleSource{}, err
	}
	return c.queries().GetRoleSourceInWorkspace(ctx, db.GetRoleSourceInWorkspaceParams{ID: sourceID, WorkspaceID: workspaceID})
}

func (c *ControlPlane) GetScan(ctx context.Context, workspaceIDText, sourceIDText, requestIDText string) (db.RoleSourceScanRequest, error) {
	workspaceID, sourceID, requestID, err := parseThreeUUIDs(workspaceIDText, sourceIDText, requestIDText)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	return c.queries().GetRoleSourceScanRequest(ctx, db.GetRoleSourceScanRequestParams{ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID})
}

func (c *ControlPlane) GetLatestScan(ctx context.Context, workspaceIDText, sourceIDText string) (db.RoleSourceScanRequest, error) {
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	return c.queries().GetLatestRoleSourceScanRequest(ctx, db.GetLatestRoleSourceScanRequestParams{
		SourceID: sourceID, WorkspaceID: workspaceID,
	})
}

func (c *ControlPlane) RenewScanLease(ctx context.Context, workspaceIDText, sourceIDText, requestIDText, runtimeIDText, leaseTokenText string, leaseDuration time.Duration) (db.RoleSourceScanRequest, error) {
	if leaseDuration < 15*time.Second || leaseDuration > 15*time.Minute {
		return db.RoleSourceScanRequest{}, errors.New("scan lease duration must be between 15 seconds and 15 minutes")
	}
	workspaceID, sourceID, requestID, runtimeID, leaseToken, err := parseFiveUUIDs(
		workspaceIDText, sourceIDText, requestIDText, runtimeIDText, leaseTokenText,
	)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	row, err := qtx.RenewRoleSourceScanLease(ctx, db.RenewRoleSourceScanLeaseParams{
		LeaseDuration: pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true},
		ID:            requestID, SourceID: sourceID, WorkspaceID: workspaceID, RuntimeID: runtimeID, LeaseToken: leaseToken,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.RoleSourceScanRequest{}, ErrScanLeaseLost
	}
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	return row, nil
}

func (c *ControlPlane) queries() *db.Queries {
	return db.New(c.database)
}

type ReportScanSuccessInput struct {
	WorkspaceID string
	SourceID    string
	RequestID   string
	RuntimeID   string
	LeaseToken  string
	Snapshot    Snapshot
}

func (c *ControlPlane) ReportScanSuccess(ctx context.Context, input ReportScanSuccessInput) (db.RoleSourceSnapshot, error) {
	snapshot, err := validatedSnapshotCopy(input.Snapshot)
	if err != nil {
		return db.RoleSourceSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidScanReport, err)
	}
	artifactRefs, err := CollectArtifactRefs(snapshot)
	if err != nil {
		return db.RoleSourceSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidScanReport, err)
	}
	workspaceID, sourceID, requestID, runtimeID, leaseToken, err := parseFiveUUIDs(input.WorkspaceID, input.SourceID, input.RequestID, input.RuntimeID, input.LeaseToken)
	if err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	manifest, _ := json.Marshal(snapshot.Manifest)
	diagnostics, _ := json.Marshal(snapshot.Diagnostics)
	evidence, _ := json.Marshal(snapshot.SourceEvidence)

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	request, err := qtx.GetRoleSourceScanRequestForUpdate(ctx, db.GetRoleSourceScanRequestForUpdateParams{ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	if isIdempotentScanSuccess(request, source, runtimeID, leaseToken, snapshot.SnapshotDigest) {
		return qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
			SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: snapshot.SnapshotDigest,
		})
	}
	if request.Status != "claimed" || request.ClaimedByRuntimeID != runtimeID || request.LeaseToken != leaseToken || source.RuntimeID != runtimeID {
		return db.RoleSourceSnapshot{}, ErrScanLeaseLost
	}
	if request.ExpectedAdapterVersion != snapshot.AdapterVersion || source.AdapterVersion != snapshot.AdapterVersion || source.Kind != string(snapshot.Kind) {
		return db.RoleSourceSnapshot{}, fmt.Errorf("%w: adapter identity does not match source registration", ErrInvalidScanReport)
	}
	if err := verifySnapshotArtifacts(ctx, qtx, workspaceID, artifactRefs); err != nil {
		return db.RoleSourceSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidScanReport, err)
	}
	stored, err := qtx.InsertRoleSourceSnapshot(ctx, db.InsertRoleSourceSnapshotParams{
		SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: snapshot.SnapshotDigest, ManifestDigest: snapshot.ManifestDigest,
		Kind: string(snapshot.Kind), AdapterVersion: snapshot.AdapterVersion, ContractVersion: snapshot.ContractVersion,
		Manifest: manifest, Diagnostics: diagnostics, SourceEvidence: evidence, ReportedByRuntimeID: runtimeID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		stored, err = qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: snapshot.SnapshotDigest})
	}
	if err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	if err := persistSnapshotArtifactEdges(ctx, qtx, workspaceID, sourceID, snapshot.SnapshotDigest, artifactRefs); err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	if _, err := qtx.CompleteRoleSourceScanSuccess(ctx, db.CompleteRoleSourceScanSuccessParams{
		SnapshotDigest: pgtype.Text{String: snapshot.SnapshotDigest, Valid: true}, ID: requestID, SourceID: sourceID,
		WorkspaceID: workspaceID, RuntimeID: runtimeID, LeaseToken: leaseToken,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoleSourceSnapshot{}, ErrScanLeaseLost
		}
		return db.RoleSourceSnapshot{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "scan_succeeded", AuditActor{Type: "runtime", ID: input.RuntimeID}, AuditPayload{
		OperationID: input.RequestID, SnapshotDigest: snapshot.SnapshotDigest, ManifestDigest: snapshot.ManifestDigest,
		AdapterKind: snapshot.Kind, AdapterVersion: snapshot.AdapterVersion, Result: "succeeded", DiagnosticCount: len(snapshot.Diagnostics),
	}); err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceSnapshot{}, err
	}
	return stored, nil
}

type ReportScanFailureInput struct {
	WorkspaceID string
	SourceID    string
	RequestID   string
	RuntimeID   string
	LeaseToken  string
	ErrorCode   string
}

func (c *ControlPlane) ReportScanFailure(ctx context.Context, input ReportScanFailureInput) (db.RoleSourceScanRequest, error) {
	if !stableIDPattern.MatchString(input.ErrorCode) {
		return db.RoleSourceScanRequest{}, fmt.Errorf("%w: invalid scan error code %q", ErrInvalidScanReport, input.ErrorCode)
	}
	workspaceID, sourceID, requestID, runtimeID, leaseToken, err := parseFiveUUIDs(input.WorkspaceID, input.SourceID, input.RequestID, input.RuntimeID, input.LeaseToken)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	request, err := qtx.GetRoleSourceScanRequestForUpdate(ctx, db.GetRoleSourceScanRequestForUpdateParams{ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	if isIdempotentScanFailure(request, source, runtimeID, leaseToken, input.ErrorCode) {
		return request, nil
	}
	if request.Status != "claimed" || request.ClaimedByRuntimeID != runtimeID || request.LeaseToken != leaseToken || source.RuntimeID != runtimeID {
		return db.RoleSourceScanRequest{}, ErrScanLeaseLost
	}
	completed, err := qtx.CompleteRoleSourceScanFailure(ctx, db.CompleteRoleSourceScanFailureParams{
		ErrorCode: pgtype.Text{String: input.ErrorCode, Valid: true}, ID: requestID, SourceID: sourceID,
		WorkspaceID: workspaceID, RuntimeID: runtimeID, LeaseToken: leaseToken,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoleSourceScanRequest{}, ErrScanLeaseLost
		}
		return db.RoleSourceScanRequest{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "scan_failed", AuditActor{Type: "runtime", ID: input.RuntimeID}, AuditPayload{
		OperationID: input.RequestID, AdapterKind: Kind(source.Kind), AdapterVersion: source.AdapterVersion,
		Result: "failed", ErrorCode: input.ErrorCode,
	}); err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceScanRequest{}, err
	}
	return completed, nil
}

func isIdempotentScanSuccess(request db.RoleSourceScanRequest, source db.RoleSource, runtimeID, leaseToken pgtype.UUID, snapshotDigest string) bool {
	return request.Status == "succeeded" && request.ClaimedByRuntimeID == runtimeID && request.LeaseToken == leaseToken &&
		source.RuntimeID == runtimeID && request.SnapshotDigest.Valid && request.SnapshotDigest.String == snapshotDigest
}

func isIdempotentScanFailure(request db.RoleSourceScanRequest, source db.RoleSource, runtimeID, leaseToken pgtype.UUID, errorCode string) bool {
	return request.Status == "failed" && request.ClaimedByRuntimeID == runtimeID && request.LeaseToken == leaseToken &&
		source.RuntimeID == runtimeID && request.ErrorCode.Valid && request.ErrorCode.String == errorCode
}

func (c *ControlPlane) appendAudit(ctx context.Context, qtx *db.Queries, source db.RoleSource, eventType string, actor AuditActor, payload AuditPayload) error {
	sequence, err := qtx.AllocateRoleSourceAuditSequence(ctx, db.AllocateRoleSourceAuditSequenceParams{ID: source.ID, WorkspaceID: source.WorkspaceID})
	if err != nil {
		return err
	}
	previous := ""
	if sequence > 1 {
		latest, err := qtx.GetLatestRoleSourceAuditEvent(ctx, db.GetLatestRoleSourceAuditEventParams{SourceID: source.ID, WorkspaceID: source.WorkspaceID})
		if err != nil {
			return fmt.Errorf("load previous role source audit event: %w", err)
		}
		if latest.Sequence != sequence-1 {
			return errors.New("role source audit sequence is discontinuous")
		}
		previous = latest.EventDigest
	}
	event, err := BuildAuditEvent(util.UUIDToString(source.ID), util.UUIDToString(source.WorkspaceID), sequence, eventType, actor, previous, payload, c.now())
	if err != nil {
		return err
	}
	eventID, err := newPGUUID()
	if err != nil {
		return err
	}
	actorID := pgtype.UUID{}
	if event.Actor.ID != "" {
		actorID, err = util.ParseUUID(event.Actor.ID)
		if err != nil {
			return err
		}
	}
	payloadBody, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	_, err = qtx.InsertRoleSourceAuditEvent(ctx, db.InsertRoleSourceAuditEventParams{
		ID: eventID, SourceID: source.ID, WorkspaceID: source.WorkspaceID, Sequence: sequence,
		EventType: event.EventType, ActorType: event.Actor.Type, ActorID: actorID,
		PreviousEventDigest: pgtype.Text{String: previous, Valid: previous != ""}, EventDigest: event.EventDigest,
		Payload: payloadBody, OccurredAt: pgtype.Timestamptz{Time: event.OccurredAt, Valid: true},
	})
	return err
}

func canonicalJSONObject(raw json.RawMessage, limit int) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > limit {
		return nil, errors.New("JSON object exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("JSON value must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON contains trailing values")
	}
	return json.Marshal(value)
}

func newPGUUID() (pgtype.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func parseTwoUUIDs(a, b string) (pgtype.UUID, pgtype.UUID, error) {
	first, err := util.ParseUUID(a)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	second, err := util.ParseUUID(b)
	return first, second, err
}

func parseThreeUUIDs(a, b, c string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	first, err := util.ParseUUID(a)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	second, err := util.ParseUUID(b)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
	}
	third, err := util.ParseUUID(c)
	return first, second, third, err
}

func parseFiveUUIDs(values ...string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	if len(values) != 5 {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, errors.New("expected five UUIDs")
	}
	parsed := [5]pgtype.UUID{}
	for index, value := range values {
		id, err := util.ParseUUID(value)
		if err != nil {
			return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
		}
		parsed[index] = id
	}
	return parsed[0], parsed[1], parsed[2], parsed[3], parsed[4], nil
}
