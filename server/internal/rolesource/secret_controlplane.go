package rolesource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInvalidSecretTransfer   = errors.New("invalid role source secret transfer request")
	ErrSecretStoreUnavailable  = errors.New("role source secret store is unavailable")
	ErrSecretTransferLeaseLost = errors.New("role source secret transfer lease is stale or no longer owned")
)

type RequestSecretTransferInput struct {
	WorkspaceID string
	SourceID    string
	PlanDigest  string
	ApprovalID  string
	RoleID      string
	RequestKey  string
	ActorUserID string
}

type ClaimedSecretTransfer struct {
	Transfer db.RoleSourceSecretTransfer
	Source   db.RoleSource
	Claims   SecretEnvelopeClaims
}

type ReportSecretTransferInput struct {
	WorkspaceID string
	SourceID    string
	TransferID  string
	RuntimeID   string
	LeaseToken  string
	Status      string
	Envelope    *SecretEnvelope
	ErrorCode   string
}

func (c *ControlPlane) RequestSecretTransfer(ctx context.Context, input RequestSecretTransferInput) (db.RoleSourceSecretTransfer, error) {
	input.RequestKey = strings.TrimSpace(input.RequestKey)
	currentBox, ok := c.secretBoxFor(c.secretKeyID)
	if !ok || c.secretKeyID == "" {
		return db.RoleSourceSecretTransfer{}, ErrSecretStoreUnavailable
	}
	if !sha256Pattern.MatchString(input.PlanDigest) || !stableIDPattern.MatchString(input.RoleID) || input.RequestKey == "" || len(input.RequestKey) > 200 {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid plan, role or request key", ErrInvalidSecretTransfer)
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: %v", ErrInvalidSecretTransfer, err)
	}
	approvalID, err := util.ParseUUID(input.ApprovalID)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid approval id", ErrInvalidSecretTransfer)
	}
	input.WorkspaceID, input.SourceID = util.UUIDToString(workspaceID), util.UUIDToString(sourceID)
	input.ApprovalID, input.ActorUserID = util.UUIDToString(approvalID), util.UUIDToString(actorID)

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if source.State == "detached" || source.State == "paused" {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: source state does not permit transfer", ErrInvalidSecretTransfer)
	}
	descriptor, ok := c.catalog.Descriptor(Kind(source.Kind))
	if !ok || descriptor.AdapterVersion != source.AdapterVersion || !descriptor.Capabilities.SecretTransfer {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: source adapter does not support secret transfer", ErrInvalidSecretTransfer)
	}
	planRow, err := qtx.GetRoleSourcePlan(ctx, db.GetRoleSourcePlanParams{SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: input.PlanDigest})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	plan, err := DecodePersistedPlan(planRow)
	if err != nil || !plan.Applyable || !snapshotCASMatches(source.CurrentSnapshotDigest, plan.FromSnapshotDigest) {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: plan is blocked or stale", ErrInvalidSecretTransfer)
	}
	approval, err := qtx.GetRoleSourcePlanApprovalByID(ctx, db.GetRoleSourcePlanApprovalByIDParams{
		ID: approvalID, SourceID: sourceID, WorkspaceID: workspaceID, PlanDigest: plan.PlanDigest,
	})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if _, err := decodeApprovedDecisions(plan, approval); err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: approval is not usable", ErrInvalidSecretTransfer)
	}
	snapshotRow, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: plan.ToSnapshotDigest})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	snapshot, err := DecodePersistedSnapshot(snapshotRow)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	role, ok := findSnapshotRole(snapshot, input.RoleID)
	if !ok || !roleNeedsSecretTransfer(role) {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: role does not declare transferable environment or MCP", ErrInvalidSecretTransfer)
	}

	existing, existingErr := qtx.GetRoleSourceSecretTransferByRequest(ctx, db.GetRoleSourceSecretTransferByRequestParams{
		SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
	})
	if existingErr == nil {
		if !secretTransferIdentityMatches(existing, input, actorID) {
			return db.RoleSourceSecretTransfer{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourceSecretTransfer{}, err
		}
		return existing, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return db.RoleSourceSecretTransfer{}, existingErr
	}

	transferID, err := newPGUUID()
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	now := c.now().UTC()
	claims := SecretEnvelopeClaims{
		ContractVersion: SecretEnvelopeContractVersion, TransferID: util.UUIDToString(transferID),
		WorkspaceID: input.WorkspaceID, SourceID: input.SourceID, RoleID: input.RoleID,
		SnapshotDigest: plan.ToSnapshotDigest, ExpiresAt: now.Add(15 * time.Minute).Format(time.RFC3339Nano),
	}
	claimsBody, err := canonicalSecretEnvelopeClaims(claims, now, true)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	keyPair, err := NewSecretEnvelopeKeyPair()
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	defer clear(keyPair.PrivateKey)
	privateCiphertext, err := currentBox.SealWithAAD(keyPair.PrivateKey, claimsBody)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, claims.ExpiresAt)
	row, err := qtx.InsertRoleSourceSecretTransfer(ctx, db.InsertRoleSourceSecretTransferParams{
		ID: transferID, WorkspaceID: workspaceID, SourceID: sourceID, RuntimeID: source.RuntimeID,
		PlanDigest: plan.PlanDigest, ApprovalID: approvalID, SnapshotDigest: plan.ToSnapshotDigest,
		RoleID: input.RoleID, RequestKey: input.RequestKey, PublicKey: keyPair.PublicKey,
		PrivateKeyCiphertext: privateCiphertext, KeyID: c.secretKeyID, Claims: claimsBody,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, CreatedBy: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = qtx.GetRoleSourceSecretTransferByRequest(ctx, db.GetRoleSourceSecretTransferByRequestParams{
			SourceID: sourceID, WorkspaceID: workspaceID, RequestKey: input.RequestKey,
		})
		if err != nil || !secretTransferIdentityMatches(row, input, actorID) {
			return db.RoleSourceSecretTransfer{}, ErrIdempotencyConflict
		}
	} else if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "secret_transfer_requested", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: util.UUIDToString(row.ID), SnapshotDigest: row.SnapshotDigest, PlanDigest: row.PlanDigest, Result: "pending",
	}); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	return row, nil
}

func findSnapshotRole(snapshot Snapshot, roleID string) (Role, bool) {
	for _, role := range snapshot.Manifest.Roles {
		if role.ID == roleID {
			return role, true
		}
	}
	return Role{}, false
}

func roleNeedsSecretTransfer(role Role) bool {
	for _, environment := range role.Environment {
		if environment.Configured {
			return true
		}
	}
	return len(role.MCP) > 0
}

func secretTransferIdentityMatches(row db.RoleSourceSecretTransfer, input RequestSecretTransferInput, actorID pgtype.UUID) bool {
	claims, err := decodeSecretTransferClaims(row.Claims)
	if err != nil {
		return false
	}
	_, err = canonicalSecretEnvelopeClaims(claims, row.CreatedAt.Time.UTC(), false)
	if err != nil {
		return false
	}
	return row.PlanDigest == input.PlanDigest &&
		util.UUIDToString(row.ApprovalID) == input.ApprovalID && row.RoleID == input.RoleID && row.CreatedBy == actorID &&
		claims.TransferID == util.UUIDToString(row.ID) && claims.WorkspaceID == input.WorkspaceID && claims.SourceID == input.SourceID &&
		claims.RoleID == input.RoleID && claims.SnapshotDigest == row.SnapshotDigest && row.KeyID != "" &&
		claims.ContractVersion == SecretEnvelopeContractVersion
}

func decodeSecretTransferClaims(body []byte) (SecretEnvelopeClaims, error) {
	var claims SecretEnvelopeClaims
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return claims, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return claims, errors.New("secret transfer claims contain trailing JSON")
	}
	return claims, nil
}

func (c *ControlPlane) ClaimNextSecretTransfer(ctx context.Context, runtimeIDText string, leaseDuration time.Duration) (ClaimedSecretTransfer, error) {
	if len(c.secretBoxes) == 0 || c.secretKeyID == "" {
		return ClaimedSecretTransfer{}, ErrSecretStoreUnavailable
	}
	if leaseDuration < 15*time.Second || leaseDuration > 5*time.Minute {
		return ClaimedSecretTransfer{}, errors.New("secret transfer lease duration must be between 15 seconds and 5 minutes")
	}
	runtimeID, err := util.ParseUUID(runtimeIDText)
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	leaseToken, err := newPGUUID()
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	runtime, err := qtx.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, runtime.WorkspaceID); err != nil {
		return ClaimedSecretTransfer{}, err
	}
	row, err := qtx.ClaimNextRoleSourceSecretTransfer(ctx, db.ClaimNextRoleSourceSecretTransferParams{
		RuntimeID: runtimeID, LeaseToken: leaseToken,
		LeaseDuration: pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true},
	})
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	if row.WorkspaceID != runtime.WorkspaceID || row.RuntimeID != runtime.ID {
		return ClaimedSecretTransfer{}, errors.New("claimed secret transfer escaped runtime tenant boundary")
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: row.SourceID, WorkspaceID: row.WorkspaceID})
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	if source.RuntimeID != runtime.ID || source.State == "detached" || source.State == "paused" {
		return ClaimedSecretTransfer{}, ErrSecretTransferLeaseLost
	}
	claims, err := validatedStoredSecretTransfer(row)
	if err != nil {
		return ClaimedSecretTransfer{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "secret_transfer_claimed", AuditActor{Type: "runtime", ID: runtimeIDText}, AuditPayload{
		OperationID: util.UUIDToString(row.ID), SnapshotDigest: row.SnapshotDigest, PlanDigest: row.PlanDigest, Result: "claimed",
	}); err != nil {
		return ClaimedSecretTransfer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClaimedSecretTransfer{}, err
	}
	return ClaimedSecretTransfer{Transfer: row, Source: source, Claims: claims}, nil
}

func (c *ControlPlane) ReportSecretTransfer(ctx context.Context, input ReportSecretTransferInput) (db.RoleSourceSecretTransfer, error) {
	if len(c.secretBoxes) == 0 || c.secretKeyID == "" {
		return db.RoleSourceSecretTransfer{}, ErrSecretStoreUnavailable
	}
	workspaceID, sourceID, transferID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.TransferID)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid transfer identity", ErrInvalidSecretTransfer)
	}
	runtimeID, leaseToken, err := parseTwoUUIDs(input.RuntimeID, input.LeaseToken)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid runtime lease identity", ErrInvalidSecretTransfer)
	}
	if input.Status != "completed" && input.Status != "failed" {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid terminal status", ErrInvalidSecretTransfer)
	}
	if input.Status == "failed" {
		if input.Envelope != nil || !kindPattern.MatchString(input.ErrorCode) {
			return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: invalid failure report", ErrInvalidSecretTransfer)
		}
	} else if input.Envelope == nil || input.ErrorCode != "" {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: completed report requires only envelope", ErrInvalidSecretTransfer)
	}

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	row, err := qtx.GetRoleSourceSecretTransferForUpdate(ctx, db.GetRoleSourceSecretTransferForUpdateParams{
		ID: transferID, SourceID: sourceID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if row.RuntimeID != runtimeID || source.RuntimeID != runtimeID || row.ClaimedByRuntimeID != runtimeID || row.LeaseToken != leaseToken {
		return db.RoleSourceSecretTransfer{}, ErrSecretTransferLeaseLost
	}
	claims, err := validatedStoredSecretTransfer(row)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if input.Status == "failed" {
		if row.Status == "failed" && row.ErrorCode.Valid && row.ErrorCode.String == input.ErrorCode {
			if err := tx.Commit(ctx); err != nil {
				return db.RoleSourceSecretTransfer{}, err
			}
			return row, nil
		}
		if row.Status != "claimed" {
			return db.RoleSourceSecretTransfer{}, ErrSecretTransferLeaseLost
		}
		row, err = qtx.FailRoleSourceSecretTransfer(ctx, db.FailRoleSourceSecretTransferParams{
			ID: transferID, SourceID: sourceID, WorkspaceID: workspaceID, RuntimeID: runtimeID, LeaseToken: leaseToken, ErrorCode: pgtype.Text{String: input.ErrorCode, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.RoleSourceSecretTransfer{}, ErrSecretTransferLeaseLost
		}
		if err != nil {
			return db.RoleSourceSecretTransfer{}, err
		}
		if err := c.appendAudit(ctx, qtx, source, "secret_transfer_failed", AuditActor{Type: "runtime", ID: input.RuntimeID}, AuditPayload{
			OperationID: input.TransferID, SnapshotDigest: row.SnapshotDigest, PlanDigest: row.PlanDigest, Result: input.ErrorCode,
		}); err != nil {
			return db.RoleSourceSecretTransfer{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return db.RoleSourceSecretTransfer{}, err
		}
		return row, nil
	}

	if input.Envelope.Claims != claims {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: envelope claims do not match challenge", ErrInvalidSecretEnvelope)
	}
	body, err := json.Marshal(input.Envelope)
	if err != nil || len(body) > 400<<10 {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("%w: envelope exceeds storage limit", ErrInvalidSecretEnvelope)
	}
	digestValue := sha256.Sum256(body)
	digest := "sha256:" + fmt.Sprintf("%x", digestValue[:])
	if row.Status == "submitted" {
		if row.EnvelopeDigest.Valid && row.EnvelopeDigest.String == digest && bytes.Equal(row.Envelope, body) {
			if err := tx.Commit(ctx); err != nil {
				return db.RoleSourceSecretTransfer{}, err
			}
			return row, nil
		}
		return db.RoleSourceSecretTransfer{}, ErrIdempotencyConflict
	}
	secretBox, ok := c.secretBoxFor(row.KeyID)
	if row.Status != "claimed" || !ok {
		return db.RoleSourceSecretTransfer{}, ErrSecretTransferLeaseLost
	}
	privateKey, err := secretBox.OpenWithAAD(row.PrivateKeyCiphertext, row.Claims)
	if err != nil {
		return db.RoleSourceSecretTransfer{}, fmt.Errorf("open secret transfer private key: %w", err)
	}
	defer clear(privateKey)
	payload, err := OpenSecretEnvelope(privateKey, *input.Envelope, c.now().UTC())
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	ClearSecretEnvelopePayload(&payload)
	row, err = qtx.SubmitRoleSourceSecretTransfer(ctx, db.SubmitRoleSourceSecretTransferParams{
		ID: transferID, SourceID: sourceID, WorkspaceID: workspaceID, RuntimeID: runtimeID, LeaseToken: leaseToken,
		Envelope: body, EnvelopeDigest: pgtype.Text{String: digest, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.RoleSourceSecretTransfer{}, ErrSecretTransferLeaseLost
	}
	if err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "secret_transfer_submitted", AuditActor{Type: "runtime", ID: input.RuntimeID}, AuditPayload{
		OperationID: input.TransferID, SnapshotDigest: row.SnapshotDigest, PlanDigest: row.PlanDigest, Result: digest,
	}); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceSecretTransfer{}, err
	}
	return row, nil
}

func validatedStoredSecretTransfer(row db.RoleSourceSecretTransfer) (SecretEnvelopeClaims, error) {
	claims, err := decodeSecretTransferClaims(row.Claims)
	if err != nil {
		return SecretEnvelopeClaims{}, fmt.Errorf("decode stored secret transfer claims: %w", err)
	}
	if _, err := canonicalSecretEnvelopeClaims(claims, row.CreatedAt.Time.UTC(), false); err != nil {
		return SecretEnvelopeClaims{}, err
	}
	if claims.TransferID != util.UUIDToString(row.ID) || claims.WorkspaceID != util.UUIDToString(row.WorkspaceID) ||
		claims.SourceID != util.UUIDToString(row.SourceID) || claims.RoleID != row.RoleID || claims.SnapshotDigest != row.SnapshotDigest {
		return SecretEnvelopeClaims{}, errors.New("stored secret transfer claims do not match row identity")
	}
	return claims, nil
}
