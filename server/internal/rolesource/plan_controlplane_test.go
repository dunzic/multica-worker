package rolesource

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
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

func TestConfigurationChangeReviewProjectionCannotContainValuesOrDefinitions(t *testing.T) {
	fromManifest := planTestManifest()
	fromManifest.Roles[0].Environment = []EnvironmentKey{{Name: "OPENAI_API_KEY", Secret: true, Configured: true, ValueDigest: testHMACSHA256("a")}}
	fromManifest.Roles[0].MCP = []MCPServer{{ID: "browser", DefinitionHash: testSHA256("b"), Environment: []string{"OPENAI_API_KEY"}}}
	toManifest := planTestManifest()
	toManifest.Roles[0].Environment = []EnvironmentKey{{Name: "OPENAI_API_KEY", Secret: true, Configured: true, ValueDigest: testHMACSHA256("c")}}
	toManifest.Roles[0].MCP = []MCPServer{{ID: "browser", DefinitionHash: testSHA256("d"), Environment: []string{"OPENAI_API_KEY"}}}
	from, to := planTestSnapshot(t, fromManifest), planTestSnapshot(t, toManifest)
	plan, err := BuildPlan("source-1", &from, to)
	if err != nil {
		t.Fatal(err)
	}
	review := configurationChangeReview(plan, 0, 100)
	if review.EnvironmentCount != 1 || review.MCPCount != 1 || len(review.Changes) != 2 {
		t.Fatalf("configuration review = %+v", review)
	}
	body, _ := json.Marshal(review)
	for _, forbidden := range []string{"value_digest", "definition_hash", `"environment":`, "description", "url", "header", "command"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("configuration review exposed %q: %s", forbidden, body)
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

func TestResolveAdoptionCandidatesFreezesOneUnmanagedTarget(t *testing.T) {
	snapshot := planTestSnapshot(t, planTestManifest())
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	requests, refs, err := collectAdoptionTargetRequests(snapshot, plan)
	if err != nil || len(requests) == 0 {
		t.Fatalf("adoption requests=%+v err=%v", requests, err)
	}
	request := refs[objectKey(ObjectRef{Kind: "role", ID: "writer"})]
	row := db.ListRoleSourceAdoptionTargetsForUpdateRow{
		TargetKind: request.TargetKind, RequestedName: request.Name,
		TargetID:         util.MustParseUUID("00000000-0000-4000-8000-000000000051"),
		UpdatedAt:        pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC), Valid: true},
		AdoptionEligible: pgtype.Bool{Bool: true, Valid: true},
	}
	if err := resolveAdoptionCandidates(&plan, refs, []db.ListRoleSourceAdoptionTargetsForUpdateRow{row}, nil); err != nil {
		t.Fatal(err)
	}
	action := actionIndex(plan)[objectKey(ObjectRef{Kind: "role", ID: "writer"})]
	if action.AdoptionCandidate == nil || action.AdoptionCandidate.TargetID != util.UUIDToString(row.TargetID) || action.Risk != PlanRiskHigh {
		t.Fatalf("adoption action=%+v", action)
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("resolved plan invalid: %v", err)
	}
}

func TestResolveAdoptionCandidatesBlocksAmbiguousOrManagedTarget(t *testing.T) {
	for _, test := range []struct {
		name string
		rows func(adoptionTargetRequest) []db.ListRoleSourceAdoptionTargetsForUpdateRow
		code string
	}{
		{
			name: "ambiguous",
			rows: func(request adoptionTargetRequest) []db.ListRoleSourceAdoptionTargetsForUpdateRow {
				return []db.ListRoleSourceAdoptionTargetsForUpdateRow{
					{TargetKind: request.TargetKind, RequestedName: request.Name, TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000051")},
					{TargetKind: request.TargetKind, RequestedName: request.Name, TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000052")},
				}
			},
			code: "adoption_target_ambiguous",
		},
		{
			name: "managed",
			rows: func(request adoptionTargetRequest) []db.ListRoleSourceAdoptionTargetsForUpdateRow {
				return []db.ListRoleSourceAdoptionTargetsForUpdateRow{{
					TargetKind: request.TargetKind, RequestedName: request.Name, TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000051"),
					ManagedBySourceID: util.MustParseUUID("00000000-0000-4000-8000-000000000042"),
				}}
			},
			code: "adoption_target_managed",
		},
		{
			name: "ineligible",
			rows: func(request adoptionTargetRequest) []db.ListRoleSourceAdoptionTargetsForUpdateRow {
				return []db.ListRoleSourceAdoptionTargetsForUpdateRow{{
					TargetKind: request.TargetKind, RequestedName: request.Name, TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000051"),
				}}
			},
			code: "adoption_target_ineligible",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := planTestSnapshot(t, planTestManifest())
			plan, err := BuildPlan("source-1", nil, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			_, refs, err := collectAdoptionTargetRequests(snapshot, plan)
			if err != nil {
				t.Fatal(err)
			}
			request := refs[objectKey(ObjectRef{Kind: "role", ID: "writer"})]
			if err := resolveAdoptionCandidates(&plan, refs, test.rows(request), nil); err != nil {
				t.Fatal(err)
			}
			if plan.Applyable || len(plan.Blockers) != 1 || plan.Blockers[0].Code != test.code {
				t.Fatalf("blocked plan=%+v", plan)
			}
			if err := ValidatePlan(plan); err != nil {
				t.Fatalf("blocked plan invalid: %v", err)
			}
		})
	}
}

func TestResolveAdoptionCandidatesRequiresCompatibleAutopilotAssignee(t *testing.T) {
	for _, test := range []struct {
		name             string
		roleTargetID     string
		autopilotAgentID string
		wantBlocked      bool
	}{
		{name: "compatible", roleTargetID: "00000000-0000-4000-8000-000000000051", autopilotAgentID: "00000000-0000-4000-8000-000000000051"},
		{name: "different agent", roleTargetID: "00000000-0000-4000-8000-000000000051", autopilotAgentID: "00000000-0000-4000-8000-000000000052", wantBlocked: true},
		{name: "unresolved role", autopilotAgentID: "00000000-0000-4000-8000-000000000052", wantBlocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := planTestManifest()
			manifest.Roles[0].Automations = []Automation{{
				ID: "daily", Name: "Daily", Schedule: "0 9 * * *", Timezone: "UTC", Prompt: testArtifact("automations/daily.md"),
			}}
			snapshot := planTestSnapshot(t, manifest)
			plan, err := BuildPlan("source-1", nil, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			_, refs, err := collectAdoptionTargetRequests(snapshot, plan)
			if err != nil {
				t.Fatal(err)
			}
			automationRef := ObjectRef{Kind: "automation", ParentID: "writer", ID: "daily"}
			request := refs[objectKey(automationRef)]
			rows := []db.ListRoleSourceAdoptionTargetsForUpdateRow{{
				TargetKind: "autopilot", RequestedName: request.Name,
				TargetID:         util.MustParseUUID("00000000-0000-4000-8000-000000000053"),
				UpdatedAt:        pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC), Valid: true},
				AdoptionEligible: pgtype.Bool{Bool: true, Valid: true}, DependencyTargetID: util.MustParseUUID(test.autopilotAgentID),
			}}
			mappings := map[string]db.RoleSourceObjectMapping{}
			if test.roleTargetID != "" {
				mappings[objectKey(ObjectRef{Kind: "role", ID: "writer"})] = db.RoleSourceObjectMapping{
					TargetKind: "agent", TargetID: util.MustParseUUID(test.roleTargetID),
				}
			}
			if err := resolveAdoptionCandidates(&plan, refs, rows, mappings); err != nil {
				t.Fatal(err)
			}
			action := actionIndex(plan)[objectKey(automationRef)]
			if test.wantBlocked {
				if plan.Applyable || action.AdoptionCandidate != nil || len(plan.Blockers) != 1 || plan.Blockers[0].Code != "adoption_dependency_incompatible" {
					t.Fatalf("incompatible plan=%+v action=%+v", plan, action)
				}
			} else if !plan.Applyable || action.AdoptionCandidate == nil || len(plan.Blockers) != 0 {
				t.Fatalf("compatible plan=%+v action=%+v", plan, action)
			}
			if err := ValidatePlan(plan); err != nil {
				t.Fatalf("resolved plan invalid: %v", err)
			}
		})
	}
}

func TestResolveAdoptionCandidatesAcceptsRoleAndAutopilotTogether(t *testing.T) {
	manifest := planTestManifest()
	manifest.Roles[0].Automations = []Automation{{
		ID: "daily", Name: "Daily", Schedule: "0 9 * * *", Timezone: "UTC", Prompt: testArtifact("automations/daily.md"),
	}}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, refs, err := collectAdoptionTargetRequests(snapshot, plan)
	if err != nil {
		t.Fatal(err)
	}
	agentID := util.MustParseUUID("00000000-0000-4000-8000-000000000051")
	updatedAt := pgtype.Timestamptz{Time: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC), Valid: true}
	rows := []db.ListRoleSourceAdoptionTargetsForUpdateRow{
		{
			TargetKind: "agent", RequestedName: refs[objectKey(ObjectRef{Kind: "role", ID: "writer"})].Name,
			TargetID: agentID, UpdatedAt: updatedAt, AdoptionEligible: pgtype.Bool{Bool: true, Valid: true},
		},
		{
			TargetKind: "autopilot", RequestedName: refs[objectKey(ObjectRef{Kind: "automation", ParentID: "writer", ID: "daily"})].Name,
			TargetID: util.MustParseUUID("00000000-0000-4000-8000-000000000052"), UpdatedAt: updatedAt,
			AdoptionEligible: pgtype.Bool{Bool: true, Valid: true}, DependencyTargetID: agentID,
		},
	}
	if err := resolveAdoptionCandidates(&plan, refs, rows, nil); err != nil {
		t.Fatal(err)
	}
	actions := actionIndex(plan)
	if !plan.Applyable || len(plan.Blockers) != 0 ||
		actions[objectKey(ObjectRef{Kind: "role", ID: "writer"})].AdoptionCandidate == nil ||
		actions[objectKey(ObjectRef{Kind: "automation", ParentID: "writer", ID: "daily"})].AdoptionCandidate == nil {
		t.Fatalf("joint adoption plan=%+v", plan)
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("joint adoption plan invalid: %v", err)
	}
}
