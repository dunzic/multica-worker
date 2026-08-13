package rolesource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrInvalidLegalHold  = errors.New("invalid role source legal hold")
	ErrLegalHoldReleased = errors.New("role source legal hold is already released")
)

type LegalHoldScope string

const (
	LegalHoldScopeSource   LegalHoldScope = "source"
	LegalHoldScopeSnapshot LegalHoldScope = "snapshot"
)

type LegalHoldReason string

const (
	LegalHoldReasonInvestigation    LegalHoldReason = "investigation"
	LegalHoldReasonLitigation       LegalHoldReason = "litigation"
	LegalHoldReasonRegulatory       LegalHoldReason = "regulatory"
	LegalHoldReasonCustomerRequest  LegalHoldReason = "customer_request"
	LegalHoldReasonSecurityIncident LegalHoldReason = "security_incident"
)

type LegalHoldReleaseReason string

const (
	LegalHoldReleaseResolved             LegalHoldReleaseReason = "resolved"
	LegalHoldReleaseCourtOrder           LegalHoldReleaseReason = "court_order"
	LegalHoldReleaseEnteredInError       LegalHoldReleaseReason = "entered_in_error"
	LegalHoldReleaseAuthorizationExpired LegalHoldReleaseReason = "authorization_expired"
)

type CreateLegalHoldInput struct {
	WorkspaceID     string
	SourceID        string
	ActorUserID     string
	RequestKey      string
	Scope           LegalHoldScope
	SnapshotDigest  string
	ReasonCode      LegalHoldReason
	ReferenceDigest string
}

type ReleaseLegalHoldInput struct {
	WorkspaceID     string
	SourceID        string
	HoldID          string
	ActorUserID     string
	RequestKey      string
	ReasonCode      LegalHoldReleaseReason
	ReferenceDigest string
}

// LegalHold is the safe audit projection. Request keys are intentionally
// excluded; external case identifiers are represented only by commitments.
type LegalHold struct {
	ID                     string
	WorkspaceID            string
	SourceID               string
	Scope                  LegalHoldScope
	SnapshotDigest         string
	ReasonCode             LegalHoldReason
	ReferenceDigest        string
	CreatedBy              string
	CreatedAt              string
	ReleaseReasonCode      LegalHoldReleaseReason
	ReleaseReferenceDigest string
	ReleasedBy             string
	ReleasedAt             string
}

func (hold LegalHold) Active() bool { return hold.ReleasedAt == "" }

func (c *ControlPlane) CreateLegalHold(ctx context.Context, input CreateLegalHoldInput) (LegalHold, error) {
	if err := validateCreateLegalHoldInput(input); err != nil {
		return LegalHold{}, err
	}
	workspaceID, sourceID, actorID, err := parseThreeUUIDs(input.WorkspaceID, input.SourceID, input.ActorUserID)
	if err != nil {
		return LegalHold{}, fmt.Errorf("%w: invalid identity", ErrInvalidLegalHold)
	}
	holdID, err := newPGUUID()
	if err != nil {
		return LegalHold{}, err
	}
	snapshotDigest := nullableLegalHoldDigest(input.SnapshotDigest)
	referenceDigest := nullableLegalHoldDigest(input.ReferenceDigest)
	requestKeyDigest := legalHoldRequestKeyDigest(input.RequestKey)

	tx, err := c.database.Begin(ctx)
	if err != nil {
		return LegalHold{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return LegalHold{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return LegalHold{}, err
	}
	if existing, getErr := qtx.GetRoleSourceLegalHoldByRequestKey(ctx, db.GetRoleSourceLegalHoldByRequestKeyParams{
		WorkspaceID: workspaceID, SourceID: sourceID, RequestKeyDigest: requestKeyDigest,
	}); getErr == nil {
		if !sameLegalHoldRequest(existing, input, actorID) {
			return LegalHold{}, ErrIdempotencyConflict
		}
		release, releaseErr := qtx.GetRoleSourceLegalHoldRelease(ctx, db.GetRoleSourceLegalHoldReleaseParams{
			HoldID: existing.ID, WorkspaceID: workspaceID, SourceID: sourceID,
		})
		if releaseErr != nil && !errors.Is(releaseErr, pgx.ErrNoRows) {
			return LegalHold{}, releaseErr
		}
		return legalHoldFromRows(existing, release), nil
	} else if !errors.Is(getErr, pgx.ErrNoRows) {
		return LegalHold{}, getErr
	}
	if input.Scope == LegalHoldScopeSnapshot {
		if _, err := qtx.GetRoleSourceSnapshot(ctx, db.GetRoleSourceSnapshotParams{
			SourceID: sourceID, WorkspaceID: workspaceID, SnapshotDigest: input.SnapshotDigest,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return LegalHold{}, fmt.Errorf("%w: snapshot does not exist", ErrInvalidLegalHold)
			}
			return LegalHold{}, err
		}
	}
	row, err := qtx.CreateRoleSourceLegalHold(ctx, db.CreateRoleSourceLegalHoldParams{
		ID: holdID, WorkspaceID: workspaceID, SourceID: sourceID, RequestKeyDigest: requestKeyDigest,
		Scope: string(input.Scope), SnapshotDigest: snapshotDigest, ReasonCode: string(input.ReasonCode),
		ReferenceDigest: referenceDigest, CreatedBy: actorID,
	})
	if err != nil {
		return LegalHold{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "legal_hold_created", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: util.UUIDToString(row.ID), SnapshotDigest: input.SnapshotDigest, Result: "active",
	}); err != nil {
		return LegalHold{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LegalHold{}, err
	}
	return legalHoldFromRows(row, db.RoleSourceLegalHoldRelease{}), nil
}

func (c *ControlPlane) ReleaseLegalHold(ctx context.Context, input ReleaseLegalHoldInput) (LegalHold, error) {
	if err := validateReleaseLegalHoldInput(input); err != nil {
		return LegalHold{}, err
	}
	workspaceID, sourceID, holdID, actorID, err := parseFourLegalHoldUUIDs(input.WorkspaceID, input.SourceID, input.HoldID, input.ActorUserID)
	if err != nil {
		return LegalHold{}, fmt.Errorf("%w: invalid identity", ErrInvalidLegalHold)
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return LegalHold{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return LegalHold{}, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return LegalHold{}, err
	}
	hold, err := qtx.GetRoleSourceLegalHoldForUpdate(ctx, db.GetRoleSourceLegalHoldForUpdateParams{
		ID: holdID, WorkspaceID: workspaceID, SourceID: sourceID,
	})
	if err != nil {
		return LegalHold{}, err
	}
	existing, getErr := qtx.GetRoleSourceLegalHoldRelease(ctx, db.GetRoleSourceLegalHoldReleaseParams{
		HoldID: holdID, WorkspaceID: workspaceID, SourceID: sourceID,
	})
	if getErr == nil {
		if !sameLegalHoldReleaseRequest(existing, input, actorID) {
			return LegalHold{}, ErrLegalHoldReleased
		}
		return legalHoldFromRows(hold, existing), nil
	}
	if !errors.Is(getErr, pgx.ErrNoRows) {
		return LegalHold{}, getErr
	}
	release, err := qtx.CreateRoleSourceLegalHoldRelease(ctx, db.CreateRoleSourceLegalHoldReleaseParams{
		HoldID: holdID, WorkspaceID: workspaceID, SourceID: sourceID,
		RequestKeyDigest: legalHoldRequestKeyDigest(input.RequestKey), ReasonCode: string(input.ReasonCode),
		ReferenceDigest: nullableLegalHoldDigest(input.ReferenceDigest), ReleasedBy: actorID,
	})
	if err != nil {
		return LegalHold{}, err
	}
	if err := c.appendAudit(ctx, qtx, source, "legal_hold_released", AuditActor{Type: "user", ID: input.ActorUserID}, AuditPayload{
		OperationID: util.UUIDToString(hold.ID), SnapshotDigest: textValue(hold.SnapshotDigest), Result: "released",
	}); err != nil {
		return LegalHold{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LegalHold{}, err
	}
	return legalHoldFromRows(hold, release), nil
}

func (c *ControlPlane) ListLegalHolds(ctx context.Context, workspaceIDText, sourceIDText string, limit int32) ([]LegalHold, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: result limit must be between 1 and 200", ErrInvalidLegalHold)
	}
	workspaceID, sourceID, err := parseTwoUUIDs(workspaceIDText, sourceIDText)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid identity", ErrInvalidLegalHold)
	}
	if _, err := c.queries().GetRoleSourceInWorkspace(ctx, db.GetRoleSourceInWorkspaceParams{ID: sourceID, WorkspaceID: workspaceID}); err != nil {
		return nil, err
	}
	rows, err := c.queries().ListRoleSourceLegalHolds(ctx, db.ListRoleSourceLegalHoldsParams{
		WorkspaceID: workspaceID, SourceID: sourceID, ResultLimit: limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]LegalHold, 0, len(rows))
	for _, row := range rows {
		result = append(result, LegalHold{
			ID: util.UUIDToString(row.ID), WorkspaceID: util.UUIDToString(row.WorkspaceID), SourceID: util.UUIDToString(row.SourceID),
			Scope: LegalHoldScope(row.Scope), SnapshotDigest: textValue(row.SnapshotDigest), ReasonCode: LegalHoldReason(row.ReasonCode),
			ReferenceDigest: textValue(row.ReferenceDigest), CreatedBy: util.UUIDToString(row.CreatedBy), CreatedAt: util.TimestampToString(row.CreatedAt),
			ReleaseReasonCode: LegalHoldReleaseReason(textValue(row.ReleaseReasonCode)), ReleaseReferenceDigest: textValue(row.ReleaseReferenceDigest),
			ReleasedBy: util.UUIDToString(row.ReleasedBy), ReleasedAt: util.TimestampToString(row.ReleasedAt),
		})
	}
	return result, nil
}

func validateCreateLegalHoldInput(input CreateLegalHoldInput) error {
	if !validLegalHoldRequestKey(input.RequestKey) || !validLegalHoldReason(input.ReasonCode) || !validOptionalDigest(input.ReferenceDigest) {
		return ErrInvalidLegalHold
	}
	switch input.Scope {
	case LegalHoldScopeSource:
		if input.SnapshotDigest != "" {
			return ErrInvalidLegalHold
		}
	case LegalHoldScopeSnapshot:
		if !sha256Pattern.MatchString(input.SnapshotDigest) {
			return ErrInvalidLegalHold
		}
	default:
		return ErrInvalidLegalHold
	}
	return nil
}

func validateReleaseLegalHoldInput(input ReleaseLegalHoldInput) error {
	if !validLegalHoldRequestKey(input.RequestKey) || !validLegalHoldReleaseReason(input.ReasonCode) || !validOptionalDigest(input.ReferenceDigest) {
		return ErrInvalidLegalHold
	}
	return nil
}

func validLegalHoldRequestKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200 && !strings.ContainsAny(value, "\r\n\x00")
}

func legalHoldRequestKeyDigest(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validOptionalDigest(value string) bool { return value == "" || sha256Pattern.MatchString(value) }

func validLegalHoldReason(reason LegalHoldReason) bool {
	switch reason {
	case LegalHoldReasonInvestigation, LegalHoldReasonLitigation, LegalHoldReasonRegulatory, LegalHoldReasonCustomerRequest, LegalHoldReasonSecurityIncident:
		return true
	default:
		return false
	}
}

func validLegalHoldReleaseReason(reason LegalHoldReleaseReason) bool {
	switch reason {
	case LegalHoldReleaseResolved, LegalHoldReleaseCourtOrder, LegalHoldReleaseEnteredInError, LegalHoldReleaseAuthorizationExpired:
		return true
	default:
		return false
	}
}

func nullableLegalHoldDigest(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func sameLegalHoldRequest(row db.RoleSourceLegalHold, input CreateLegalHoldInput, actorID pgtype.UUID) bool {
	return row.Scope == string(input.Scope) && textValue(row.SnapshotDigest) == input.SnapshotDigest &&
		row.ReasonCode == string(input.ReasonCode) && textValue(row.ReferenceDigest) == input.ReferenceDigest && row.CreatedBy == actorID
}

func sameLegalHoldReleaseRequest(row db.RoleSourceLegalHoldRelease, input ReleaseLegalHoldInput, actorID pgtype.UUID) bool {
	return row.RequestKeyDigest == legalHoldRequestKeyDigest(input.RequestKey) && row.ReasonCode == string(input.ReasonCode) &&
		textValue(row.ReferenceDigest) == input.ReferenceDigest && row.ReleasedBy == actorID
}

func legalHoldFromRows(hold db.RoleSourceLegalHold, release db.RoleSourceLegalHoldRelease) LegalHold {
	return LegalHold{
		ID: util.UUIDToString(hold.ID), WorkspaceID: util.UUIDToString(hold.WorkspaceID), SourceID: util.UUIDToString(hold.SourceID),
		Scope: LegalHoldScope(hold.Scope), SnapshotDigest: textValue(hold.SnapshotDigest), ReasonCode: LegalHoldReason(hold.ReasonCode),
		ReferenceDigest: textValue(hold.ReferenceDigest), CreatedBy: util.UUIDToString(hold.CreatedBy), CreatedAt: util.TimestampToString(hold.CreatedAt),
		ReleaseReasonCode: LegalHoldReleaseReason(release.ReasonCode), ReleaseReferenceDigest: textValue(release.ReferenceDigest),
		ReleasedBy: util.UUIDToString(release.ReleasedBy), ReleasedAt: util.TimestampToString(release.ReleasedAt),
	}
}

func parseFourLegalHoldUUIDs(values ...string) (pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	if len(values) != 4 {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, errors.New("expected four UUIDs")
	}
	parsed := [4]pgtype.UUID{}
	for index, value := range values {
		id, err := util.ParseUUID(value)
		if err != nil {
			return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, err
		}
		parsed[index] = id
	}
	return parsed[0], parsed[1], parsed[2], parsed[3], nil
}
