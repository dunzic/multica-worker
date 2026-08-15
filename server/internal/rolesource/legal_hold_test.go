package rolesource

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestLegalHoldValidationIsClosedAndContentFree(t *testing.T) {
	valid := CreateLegalHoldInput{
		RequestKey: "hold-request-1", Scope: LegalHoldScopeSnapshot,
		SnapshotDigest: testSHA256("a"), ReasonCode: LegalHoldReasonLitigation,
		ReferenceDigest: testSHA256("b"),
	}
	if err := validateCreateLegalHoldInput(valid); err != nil {
		t.Fatalf("valid hold rejected: %v", err)
	}
	invalid := []CreateLegalHoldInput{
		{RequestKey: "hold-request-1", Scope: LegalHoldScopeSource, SnapshotDigest: valid.SnapshotDigest, ReasonCode: LegalHoldReasonLitigation},
		{RequestKey: "hold-request-1", Scope: LegalHoldScopeSnapshot, ReasonCode: LegalHoldReasonLitigation},
		{RequestKey: "hold-request-1", Scope: LegalHoldScopeSnapshot, SnapshotDigest: valid.SnapshotDigest, ReasonCode: "free_text_case"},
		{RequestKey: "hold\nrequest", Scope: LegalHoldScopeSource, ReasonCode: LegalHoldReasonRegulatory},
		{RequestKey: "hold-request-1", Scope: LegalHoldScopeSource, ReasonCode: LegalHoldReasonRegulatory, ReferenceDigest: "CASE-123"},
	}
	for index, input := range invalid {
		if err := validateCreateLegalHoldInput(input); !errors.Is(err, ErrInvalidLegalHold) {
			t.Errorf("invalid hold %d error=%v", index, err)
		}
	}
	if err := validateReleaseLegalHoldInput(ReleaseLegalHoldInput{
		RequestKey: "release-1", ReasonCode: LegalHoldReleaseCourtOrder, ReferenceDigest: testSHA256("c"),
	}); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
	if err := validateReleaseLegalHoldInput(ReleaseLegalHoldInput{RequestKey: "release-1", ReasonCode: "delete_it"}); !errors.Is(err, ErrInvalidLegalHold) {
		t.Fatalf("free-text release reason error=%v", err)
	}
}

func TestLegalHoldIdempotencyRequiresExactAuthorityAndInput(t *testing.T) {
	actor := util.MustParseUUID("00000000-0000-4000-8000-000000000011")
	row := db.RoleSourceLegalHold{
		Scope: "snapshot", SnapshotDigest: pgtype.Text{String: testSHA256("a"), Valid: true},
		ReasonCode: "litigation", ReferenceDigest: pgtype.Text{String: testSHA256("b"), Valid: true}, CreatedBy: actor,
	}
	input := CreateLegalHoldInput{
		Scope: LegalHoldScopeSnapshot, SnapshotDigest: row.SnapshotDigest.String,
		ReasonCode: LegalHoldReasonLitigation, ReferenceDigest: row.ReferenceDigest.String,
	}
	if !sameLegalHoldRequest(row, input, actor) {
		t.Fatal("exact hold retry was not idempotent")
	}
	input.ReasonCode = LegalHoldReasonInvestigation
	if sameLegalHoldRequest(row, input, actor) {
		t.Fatal("changed hold retry was accepted")
	}

	release := db.RoleSourceLegalHoldRelease{
		RequestKeyDigest: roleSourceRequestKeyDigest("release-1"), ReasonCode: "resolved",
		ReferenceDigest: pgtype.Text{String: testSHA256("c"), Valid: true}, ReleasedBy: actor,
	}
	releaseInput := ReleaseLegalHoldInput{
		RequestKey: "release-1", ReasonCode: LegalHoldReleaseResolved, ReferenceDigest: release.ReferenceDigest.String,
	}
	if !sameLegalHoldReleaseRequest(release, releaseInput, actor) {
		t.Fatal("exact release retry was not idempotent")
	}
	releaseInput.RequestKey = "different"
	if sameLegalHoldReleaseRequest(release, releaseInput, actor) {
		t.Fatal("changed release retry was accepted")
	}
}

func TestLegalHoldProjectionExcludesRequestKeys(t *testing.T) {
	hold := db.RoleSourceLegalHold{
		ID:               util.MustParseUUID("00000000-0000-4000-8000-000000000021"),
		WorkspaceID:      util.MustParseUUID("00000000-0000-4000-8000-000000000022"),
		SourceID:         util.MustParseUUID("00000000-0000-4000-8000-000000000023"),
		RequestKeyDigest: roleSourceRequestKeyDigest("must-not-project"), Scope: "source", ReasonCode: "regulatory",
		CreatedBy: util.MustParseUUID("00000000-0000-4000-8000-000000000024"),
		CreatedAt: pgtype.Timestamptz{Valid: true},
	}
	projected := legalHoldFromRows(hold, db.RoleSourceLegalHoldRelease{RequestKeyDigest: roleSourceRequestKeyDigest("also-private")})
	if !projected.Active() || projected.ID == "" || projected.Scope != LegalHoldScopeSource {
		t.Fatalf("hold projection=%+v", projected)
	}
}
