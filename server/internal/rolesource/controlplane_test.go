package rolesource

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestControlPlaneMaterializationConcurrencyIsBounded(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		wantCap int
		valid   bool
	}{
		{name: "default", wantCap: DefaultMaterializationConcurrency, valid: true},
		{name: "one", value: 1, wantCap: 1, valid: true},
		{name: "maximum", value: MaxMaterializationConcurrency, wantCap: MaxMaterializationConcurrency, valid: true},
		{name: "negative", value: -1},
		{name: "above maximum", value: MaxMaterializationConcurrency + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeMaterializationConcurrency(test.value)
			if test.valid {
				if err != nil || got != test.wantCap {
					t.Fatalf("capacity=%d err=%v, want %d", got, err, test.wantCap)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid concurrency %d was accepted", test.value)
			}
		})
	}
}

func TestMaterializationAdmissionRejectsWorkBeyondConfiguredCapacity(t *testing.T) {
	control := &ControlPlane{materializeSlots: make(chan struct{}, 2)}
	if !control.acquireMaterializeSlot() || !control.acquireMaterializeSlot() {
		t.Fatal("configured materialization capacity was not admitted")
	}
	if control.acquireMaterializeSlot() {
		t.Fatal("materialization beyond configured capacity was admitted")
	}
	control.releaseMaterializeSlot()
	if !control.acquireMaterializeSlot() {
		t.Fatal("released materialization capacity was not reusable")
	}
}

func TestCanonicalJSONObjectIsStableAndRejectsTrailingValues(t *testing.T) {
	first, err := canonicalJSONObject(json.RawMessage(`{"z":1,"a":{"b":true}}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalJSONObject(json.RawMessage(` { "a": { "b": true }, "z": 1 } `), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("canonical JSON differs: %s != %s", first, second)
	}
	for _, invalid := range []string{`[]`, `null`, `{"ok":true} false`, `{"ok":true} 1`} {
		if _, err := canonicalJSONObject(json.RawMessage(invalid), 1024); err == nil {
			t.Fatalf("canonicalJSONObject accepted %s", invalid)
		}
	}
}

func TestScanReportValidationUsesStableSentinel(t *testing.T) {
	controlPlane := &ControlPlane{}
	if _, err := controlPlane.ReportScanSuccess(context.Background(), ReportScanSuccessInput{}); !errors.Is(err, ErrInvalidScanReport) {
		t.Fatalf("invalid success report error = %v", err)
	}
	if _, err := controlPlane.ReportScanFailure(context.Background(), ReportScanFailureInput{ErrorCode: "NOT SAFE"}); !errors.Is(err, ErrInvalidScanReport) {
		t.Fatalf("invalid failure report error = %v", err)
	}
}

func TestTerminalScanReportIdempotencyRequiresExactRuntimeLeaseAndOutcome(t *testing.T) {
	runtimeID := util.MustParseUUID("00000000-0000-4000-8000-000000000051")
	leaseToken := util.MustParseUUID("00000000-0000-4000-8000-000000000052")
	source := db.RoleSource{RuntimeID: runtimeID}
	success := db.RoleSourceScanRequest{
		Status: "succeeded", ClaimedByRuntimeID: runtimeID, LeaseToken: leaseToken,
		SnapshotDigest: pgtype.Text{String: testSHA256("snapshot"), Valid: true},
	}
	if !isIdempotentScanSuccess(success, source, runtimeID, leaseToken, testSHA256("snapshot")) {
		t.Fatal("exact duplicate success report was not idempotent")
	}
	if isIdempotentScanSuccess(success, source, runtimeID, util.MustParseUUID("00000000-0000-4000-8000-000000000053"), testSHA256("snapshot")) ||
		isIdempotentScanSuccess(success, source, runtimeID, leaseToken, testSHA256("different")) {
		t.Fatal("conflicting success report was accepted as idempotent")
	}

	failure := db.RoleSourceScanRequest{
		Status: "failed", ClaimedByRuntimeID: runtimeID, LeaseToken: leaseToken,
		ErrorCode: pgtype.Text{String: "source_changed", Valid: true},
	}
	if !isIdempotentScanFailure(failure, source, runtimeID, leaseToken, "source_changed") {
		t.Fatal("exact duplicate failure report was not idempotent")
	}
	if isIdempotentScanFailure(failure, source, runtimeID, leaseToken, "permission_denied") {
		t.Fatal("conflicting failure report was accepted as idempotent")
	}
}

func TestConfigSummaryRejectsPathLikeAndDuplicateAttributes(t *testing.T) {
	pathLike := ConfigSummary{Configured: true, Attributes: []ConfigAttribute{{Name: "root_name", Value: "/private/source"}}}
	if err := validateConfigSummary(&pathLike); err == nil {
		t.Fatal("validateConfigSummary accepted an absolute path")
	}
	duplicate := ConfigSummary{Configured: true, Attributes: []ConfigAttribute{
		{Name: "root_name", Value: "one"},
		{Name: "root_name", Value: "two"},
	}}
	if err := validateConfigSummary(&duplicate); err == nil {
		t.Fatal("validateConfigSummary accepted duplicate attributes")
	}
}
