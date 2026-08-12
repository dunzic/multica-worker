package rolesource

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestValidateMaterializationScopeAcceptsSafeSourceNeutralObjects(t *testing.T) {
	manifest := planTestManifest()
	manifest.Capabilities = []Capability{{
		ID: "browser", Name: "Browser", Version: "1.0.0", Entrypoint: testArtifact("capabilities/browser/SKILL.md"),
	}}
	manifest.Roles[0].Automations = []Automation{{
		ID: "daily", Name: "Daily", Schedule: "0 9 * * *", Timezone: "Asia/Shanghai", Prompt: testArtifact("automations/daily.md"),
	}}
	snapshot := planTestSnapshot(t, manifest)
	plan, err := BuildPlan("source-1", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); err != nil {
		t.Fatalf("safe materialization rejected: %v", err)
	}
}

func TestValidateMaterializationScopeBlocksFieldsWithoutOwnershipContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"profile", func(manifest *Manifest) {
			value := testArtifact("roles/writer/profile.md")
			manifest.Roles[0].Profile = &value
		}},
		{"environment", func(manifest *Manifest) {
			manifest.Roles[0].Environment = []EnvironmentKey{{Name: "TOKEN", Secret: true, Required: true}}
		}},
		{"mcp", func(manifest *Manifest) {
			manifest.Roles[0].MCP = []MCPServer{{ID: "browser", DefinitionHash: testSHA256("a")}}
		}},
		{"capability binding", func(manifest *Manifest) {
			manifest.Capabilities = []Capability{{ID: "browser", Name: "Browser", Version: "1", Profiles: []string{"default"}, PermissionModes: []string{"private"}, Entrypoint: testArtifact("capability.md")}}
			manifest.Roles[0].CapabilityBindings = []CapabilityBinding{{CapabilityID: "browser", SkillID: "draft", Profile: "default", VersionConstraint: "1", PermissionMode: "private"}}
		}},
		{"supporting skill file", func(manifest *Manifest) {
			manifest.Roles[0].Skills[0].Artifacts = []ArtifactRef{testArtifact("helper.txt")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := planTestManifest()
			test.mutate(&manifest)
			snapshot := planTestSnapshot(t, manifest)
			plan, err := BuildPlan("source-1", nil, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateMaterializationScope(snapshot, plan, map[string]ArchiveDecision{}); !errors.Is(err, ErrMaterializationBlocked) {
				t.Fatalf("scope error = %v, want materialization blocked", err)
			}
		})
	}
}

func TestValidateMaterializationScopeRequiresRetainForSkillRemoval(t *testing.T) {
	base := planTestSnapshot(t, planTestManifest())
	targetManifest := planTestManifest()
	targetManifest.Roles[0].Skills = []Skill{}
	target := planTestSnapshot(t, targetManifest)
	plan, err := BuildPlan("source-1", &base, target)
	if err != nil {
		t.Fatal(err)
	}
	ref := ObjectRef{Kind: "skill", ParentID: "writer", ID: "draft"}
	if err := validateMaterializationScope(target, plan, map[string]ArchiveDecision{objectKey(ref): ArchiveDecisionArchive}); !errors.Is(err, ErrMaterializationBlocked) {
		t.Fatalf("archive skill error = %v", err)
	}
	if err := validateMaterializationScope(target, plan, map[string]ArchiveDecision{objectKey(ref): ArchiveDecisionRetain}); err != nil {
		t.Fatalf("retain skill rejected: %v", err)
	}
}

func TestApplyReceiptDigestDetectsTampering(t *testing.T) {
	applyID := util.MustParseUUID("00000000-0000-4000-8000-000000000045")
	receipt := ApplyReceipt{
		ContractVersion: ApplyReceiptContractVersion, ApplyID: util.UUIDToString(applyID),
		SourceID: "00000000-0000-4000-8000-000000000042", WorkspaceID: "00000000-0000-4000-8000-000000000001",
		SnapshotDigest: testSHA256("s"), PlanDigest: testSHA256("p"), ApprovalID: "00000000-0000-4000-8000-000000000044",
		Mappings: []ApplyMapping{},
	}
	_, digest, err := encodeApplyReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptDigest = digest
	body, _, err := encodeApplyReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	row := db.RoleSourceApply{
		ID: applyID, SourceID: util.MustParseUUID(receipt.SourceID), WorkspaceID: util.MustParseUUID(receipt.WorkspaceID),
		Mode: "apply", ActorUserID: util.MustParseUUID("00000000-0000-4000-8000-000000000003"),
		Status: "succeeded", SnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest,
		ReceiptDigest: pgtype.Text{String: digest, Valid: true}, Receipt: body,
	}
	if _, err := decodeApplyReceipt(row); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	plan := Plan{ToSnapshotDigest: receipt.SnapshotDigest, PlanDigest: receipt.PlanDigest}
	input := ApplyPlanInput{SourceID: receipt.SourceID, WorkspaceID: receipt.WorkspaceID, ApprovalID: receipt.ApprovalID}
	if _, err := matchIdempotentApply(row, plan, input, row.ActorUserID); err != nil {
		t.Fatalf("exact retry rejected: %v", err)
	}
	input.ApprovalID = "00000000-0000-4000-8000-000000000099"
	if _, err := matchIdempotentApply(row, plan, input, row.ActorUserID); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	var tampered map[string]any
	if err := json.Unmarshal(body, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["counts"] = map[string]any{"created": 99}
	row.Receipt, _ = json.Marshal(tampered)
	if _, err := decodeApplyReceipt(row); err == nil {
		t.Fatal("tampered receipt accepted")
	}
}

func TestSnapshotCASMatchesNullAndExactDigestOnly(t *testing.T) {
	digest := testSHA256("a")
	if !snapshotCASMatches(pgtype.Text{}, "") || !snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, digest) {
		t.Fatal("valid snapshot CAS did not match")
	}
	if snapshotCASMatches(pgtype.Text{}, digest) || snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, "") || snapshotCASMatches(pgtype.Text{String: digest, Valid: true}, testSHA256("b")) {
		t.Fatal("stale snapshot CAS matched")
	}
}

func TestMaterializationQueriesPreserveUserManagedFields(t *testing.T) {
	tests := []struct {
		file      string
		query     string
		forbidden []string
	}{
		{"agent.sql", "UpdateRoleSourceAgent", []string{"custom_env =", "mcp_config =", "model =", "permission_mode =", "status =", "archived_at ="}},
		{"autopilot.sql", "UpdateRoleSourceAutopilot", []string{"status =", "enabled =", "assignee_id =", "execution_mode ="}},
		{"skill.sql", "UpdateRoleSourceSkill", []string{"config =", "created_by ="}},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", test.file))
			if err != nil {
				t.Fatal(err)
			}
			start := "-- name: " + test.query + " "
			text := string(body)
			index := strings.Index(text, start)
			if index < 0 {
				t.Fatalf("query %s not found", test.query)
			}
			section := text[index:]
			if next := strings.Index(section[len(start):], "\n-- name: "); next >= 0 {
				section = section[:len(start)+next]
			}
			if set := strings.Index(section, "\nSET "); set >= 0 {
				section = section[set:]
				if where := strings.Index(section, "\nWHERE "); where >= 0 {
					section = section[:where]
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(section, forbidden) {
					t.Fatalf("%s mutates user-managed field through %q", test.query, forbidden)
				}
			}
		})
	}
}
