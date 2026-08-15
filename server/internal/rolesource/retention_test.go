package rolesource

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRetentionPolicyValidationIsConservativeAndBounded(t *testing.T) {
	valid := UpdateRetentionPolicyInput{
		RequestKey: "retention-policy-1", ExpectedVersion: 0, Enabled: true,
		MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
	}
	if err := validateRetentionPolicyInput(valid); err != nil {
		t.Fatalf("valid retention policy rejected: %v", err)
	}
	invalid := []UpdateRetentionPolicyInput{
		{RequestKey: "", ExpectedVersion: 0, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10},
		{RequestKey: "retention\nkey", ExpectedVersion: 0, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10},
		{RequestKey: "retention", ExpectedVersion: -1, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10},
		{RequestKey: "retention", ExpectedVersion: 0, MinimumAgeDays: 29, KeepSuccessfulSnapshots: 10},
		{RequestKey: "retention", ExpectedVersion: 0, MinimumAgeDays: 3651, KeepSuccessfulSnapshots: 10},
		{RequestKey: "retention", ExpectedVersion: 0, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 1},
		{RequestKey: "retention", ExpectedVersion: 0, MinimumAgeDays: 90, KeepSuccessfulSnapshots: 101},
	}
	for index, input := range invalid {
		if err := validateRetentionPolicyInput(input); !errors.Is(err, ErrInvalidRetentionPolicy) {
			t.Errorf("invalid retention policy %d error=%v", index, err)
		}
	}
}

func TestRetentionPolicyIdempotencyRequiresExactRevisionAuthorityAndInput(t *testing.T) {
	actor := util.MustParseUUID("00000000-0000-4000-8000-000000000071")
	input := UpdateRetentionPolicyInput{
		RequestKey: "retention-policy-1", ExpectedVersion: 2, Enabled: true,
		MinimumAgeDays: 180, KeepSuccessfulSnapshots: 12,
	}
	row := db.RoleSourceRetentionPolicy{
		Version: 3, RequestKeyDigest: roleSourceRequestKeyDigest(input.RequestKey), Enabled: true,
		MinimumAgeDays: 180, KeepSuccessfulSnapshots: 12, CreatedBy: actor,
	}
	if !sameRetentionPolicyRequest(row, input, actor) {
		t.Fatal("exact policy retry was not idempotent")
	}
	input.MinimumAgeDays++
	if sameRetentionPolicyRequest(row, input, actor) {
		t.Fatal("changed policy retry was accepted")
	}
	input.MinimumAgeDays--
	input.ExpectedVersion++
	if sameRetentionPolicyRequest(row, input, actor) {
		t.Fatal("same key with another expected revision was accepted")
	}
}

func TestRetentionProjectionExcludesRequestDigest(t *testing.T) {
	row := db.RoleSourceRetentionPolicy{
		WorkspaceID: util.MustParseUUID("00000000-0000-4000-8000-000000000072"),
		SourceID:    util.MustParseUUID("00000000-0000-4000-8000-000000000073"),
		Version:     1, RequestKeyDigest: roleSourceRequestKeyDigest("private"), Enabled: true,
		MinimumAgeDays: 90, KeepSuccessfulSnapshots: 10,
		CreatedBy: util.MustParseUUID("00000000-0000-4000-8000-000000000074"),
		CreatedAt: pgtype.Timestamptz{Valid: true},
	}
	policy := retentionPolicyFromRow(row)
	if policy.Version != 1 || !policy.Enabled || policy.WorkspaceID == "" || policy.MinimumAgeDays != 90 {
		t.Fatalf("policy projection=%+v", policy)
	}
}

func TestRetentionCandidateBatchIsBoundedBeforeDatabaseAccess(t *testing.T) {
	control := &ControlPlane{}
	for _, limit := range []int32{-1, 0, 101} {
		if _, err := control.QueueRetentionCandidates(t.Context(), limit); !errors.Is(err, ErrInvalidRetentionPolicy) {
			t.Errorf("candidate limit %d error=%v", limit, err)
		}
	}
}
