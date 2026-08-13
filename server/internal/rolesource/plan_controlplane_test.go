package rolesource

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestSnapshotSummaryFromRowReturnsOnlyBoundedEvidence(t *testing.T) {
	snapshot := planTestSnapshot(t, planTestManifest())
	snapshot.SourceEvidence.Revision = "commit-123"
	refreshSnapshotDigest(t, &snapshot)
	manifest, _ := json.Marshal(snapshot.Manifest)
	diagnostics, _ := json.Marshal(snapshot.Diagnostics)
	evidence, _ := json.Marshal(snapshot.SourceEvidence)
	row := db.RoleSourceSnapshot{
		SnapshotDigest: snapshot.SnapshotDigest, ManifestDigest: snapshot.ManifestDigest,
		Kind: string(snapshot.Kind), AdapterVersion: snapshot.AdapterVersion, ContractVersion: snapshot.ContractVersion,
		Manifest: manifest, Diagnostics: diagnostics, SourceEvidence: evidence,
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC), Valid: true},
	}

	summary, err := SnapshotSummaryFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RoleCount != 1 || summary.CapabilityCount != 0 || summary.Revision != "commit-123" || summary.TreeDigest == "" {
		t.Fatalf("summary = %+v", summary)
	}
	body, _ := json.Marshal(summary)
	for _, forbidden := range []string{"instructions", "skills", "environment", "mcp", "automations", "path", "diagnostics"} {
		if strings.Contains(string(body), `"`+forbidden+`"`) {
			t.Fatalf("summary exposed %q: %s", forbidden, body)
		}
	}
}

func TestCompareSnapshotObjectsIsStableAndContentFree(t *testing.T) {
	from := planTestManifest()
	from.Roles[0].Environment = []EnvironmentKey{{Name: "SECRET_NAME", Required: true}}
	from.Capabilities = []Capability{{ID: "browser", Name: "Browser", Version: "1.0.0", Entrypoint: testArtifact("private/capability.md")}}
	to := planTestManifest()
	to.Roles[0].DisplayName = "Senior Writer"
	to.Roles[0].Skills[0].Entrypoint.Path = "private/new-skill.md"
	to.Roles = append(to.Roles, Role{ID: "reviewer", DisplayName: "Reviewer", Instructions: testArtifact("private/reviewer.md")})

	changes := compareSnapshotObjects(from, to)
	want := []SnapshotChange{
		{ObjectKind: "capability", ObjectID: "browser", DisplayName: "Browser", Operation: "removed"},
		{ObjectKind: "role", ObjectID: "reviewer", DisplayName: "Reviewer", Operation: "added"},
		{ObjectKind: "role", ObjectID: "writer", DisplayName: "Senior Writer", Operation: "changed"},
		{ObjectKind: "skill", ObjectID: "draft", ParentID: "writer", DisplayName: "Draft", Operation: "changed"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	body, _ := json.Marshal(changes)
	for _, forbidden := range []string{"SECRET_NAME", "private/", "digest", "media_type", "size_bytes"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("comparison exposed %q: %s", forbidden, body)
		}
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
