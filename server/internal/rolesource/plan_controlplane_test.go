package rolesource

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDecodePersistedSnapshotRevalidatesContentAndColumns(t *testing.T) {
	snapshot := planTestSnapshot(t, planTestManifest())
	manifest, err := json.Marshal(snapshot.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := json.Marshal(snapshot.Diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(snapshot.SourceEvidence)
	if err != nil {
		t.Fatal(err)
	}
	row := db.RoleSourceSnapshot{
		SnapshotDigest: snapshot.SnapshotDigest, ManifestDigest: snapshot.ManifestDigest,
		Kind: string(snapshot.Kind), AdapterVersion: snapshot.AdapterVersion, ContractVersion: snapshot.ContractVersion,
		Manifest: manifest, Diagnostics: diagnostics, SourceEvidence: evidence,
	}
	decoded, err := DecodePersistedSnapshot(row)
	if err != nil {
		t.Fatalf("decode persisted snapshot: %v", err)
	}
	if decoded.SnapshotDigest != snapshot.SnapshotDigest || decoded.ManifestDigest != snapshot.ManifestDigest {
		t.Fatalf("decoded snapshot identity = %+v", decoded)
	}

	row.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := DecodePersistedSnapshot(row); err == nil {
		t.Fatal("persisted snapshot accepted a tampered indexed digest")
	}
}

func TestDecodePersistedPlanRevalidatesContentAndColumns(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	plan, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	row := db.RoleSourcePlan{
		PlanDigest: plan.PlanDigest, ToSnapshotDigest: plan.ToSnapshotDigest, Plan: body,
		FromSnapshotDigest: pgtype.Text{},
	}
	if _, err := DecodePersistedPlan(row); err != nil {
		t.Fatalf("decode persisted plan: %v", err)
	}

	row.ToSnapshotDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := DecodePersistedPlan(row); err == nil {
		t.Fatal("persisted plan accepted a mismatched target column")
	}
}
