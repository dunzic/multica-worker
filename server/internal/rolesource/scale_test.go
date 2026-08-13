package rolesource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const productionScaleRoleCount = 10_000

const (
	productionApplyRoleCount     = 1_000
	productionApplySkillsPerRole = 10
	productionApplyArtifactBytes = int64(8 << 10)
)

func productionScaleManifest() Manifest {
	roles := make([]Role, productionScaleRoleCount)
	for i := range roles {
		id := fmt.Sprintf("role-%05d", i)
		roles[i] = Role{
			ID: id, DisplayName: fmt.Sprintf("Role %05d", i), Version: "1.0.0",
			Instructions: testArtifact("roles/" + id + "/instructions.md"),
		}
	}
	return Manifest{ContractVersion: ContractVersion, Roles: roles, Capabilities: []Capability{}}
}

func TestBuildPlanAtTenThousandRoleContractLimit(t *testing.T) {
	started := time.Now()
	snapshot := planTestSnapshot(t, productionScaleManifest())
	plan, err := BuildPlan("scale-source", nil, snapshot)
	if err != nil {
		t.Fatalf("build 10,000-role plan: %v", err)
	}
	if len(plan.Actions) != productionScaleRoleCount || plan.Summary.Create != productionScaleRoleCount || !plan.Applyable {
		t.Fatalf("10,000-role plan summary = actions %d, %+v", len(plan.Actions), plan.Summary)
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("validate 10,000-role plan: %v", err)
	}
	t.Logf("10,000-role snapshot + deterministic plan + validation completed in %s", time.Since(started))
}

func BenchmarkBuildPlanTenThousandRoles(b *testing.B) {
	snapshot := planTestSnapshot(b, productionScaleManifest())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := BuildPlan("scale-source", nil, snapshot); err != nil {
			b.Fatal(err)
		}
	}
}

func productionApplyArtifact(path string) ArtifactRef {
	digest := sha256.Sum256([]byte(path))
	return ArtifactRef{
		Path: path, Digest: "sha256:" + hex.EncodeToString(digest[:]),
		MediaType: "text/markdown", SizeBytes: productionApplyArtifactBytes,
	}
}

func productionApplyManifest() Manifest {
	roles := make([]Role, productionApplyRoleCount)
	for roleIndex := range roles {
		roleID := fmt.Sprintf("role-%04d", roleIndex)
		skills := make([]Skill, productionApplySkillsPerRole)
		for skillIndex := range skills {
			skillID := fmt.Sprintf("skill-%02d", skillIndex)
			skills[skillIndex] = Skill{
				ID: skillID, Name: fmt.Sprintf("Skill %02d", skillIndex),
				Entrypoint: productionApplyArtifact("roles/" + roleID + "/skills/" + skillID + "/SKILL.md"),
			}
		}
		roles[roleIndex] = Role{
			ID: roleID, DisplayName: fmt.Sprintf("Role %04d", roleIndex), Version: "1.0.0", Skills: skills,
			Instructions: productionApplyArtifact("roles/" + roleID + "/instructions.md"),
		}
	}
	return Manifest{ContractVersion: ContractVersion, Roles: roles, Capabilities: []Capability{}}
}

func TestMaterializationPreflightCoversThousandRolesAndTenThousandSkills(t *testing.T) {
	snapshot := planTestSnapshot(t, productionApplyManifest())
	refs, err := collectMaterializationArtifactRefs(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	wantCount := productionApplyRoleCount * (productionApplySkillsPerRole + 1)
	if len(refs) != wantCount || len(refs) > maxApplyArtifacts {
		t.Fatalf("materialization refs=%d, want=%d within limit=%d", len(refs), wantCount, maxApplyArtifacts)
	}
	var total int64
	for _, ref := range refs {
		total += ref.SizeBytes
	}
	if total > maxApplyArtifactBytes {
		t.Fatalf("materialization bytes=%d exceed limit=%d", total, maxApplyArtifactBytes)
	}
	plan, err := BuildPlan("scale-apply-source", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != wantCount || !plan.Applyable {
		t.Fatalf("apply-scale plan actions=%d applyable=%v", len(plan.Actions), plan.Applyable)
	}
}

func TestTenThousandAgentSkillBindingsFitOneDeterministicBatch(t *testing.T) {
	state := materializationState{}
	agentID := pgtype.UUID{Bytes: uuid.NewSHA1(uuid.NameSpaceOID, []byte("scale-agent")), Valid: true}
	for index := 0; index < productionApplyRoleCount*productionApplySkillsPerRole; index++ {
		skillID := pgtype.UUID{Bytes: uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("scale-skill-%05d", index))), Valid: true}
		if err := state.stageAgentSkillBinding(agentID, skillID); err != nil {
			t.Fatal(err)
		}
	}
	bindings := state.orderedAgentSkillBindings()
	if len(bindings) != productionApplyRoleCount*productionApplySkillsPerRole {
		t.Fatalf("batch bindings=%d", len(bindings))
	}
	for index := 1; index < len(bindings); index++ {
		if bindings[index-1].AgentID+"/"+bindings[index-1].SkillID >= bindings[index].AgentID+"/"+bindings[index].SkillID {
			t.Fatal("10,000 binding batch is not deterministic")
		}
	}
}

func TestTenThousandMaterializationNamesFitOnePreflight(t *testing.T) {
	snapshot := planTestSnapshot(t, productionScaleManifest())
	plan, err := BuildPlan("scale-name-source", nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	names, err := collectMaterializationNames(snapshot, plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != productionScaleRoleCount {
		t.Fatalf("name preflight=%d, want=%d", len(names), productionScaleRoleCount)
	}
	body, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 2<<20 {
		t.Fatalf("10,000 name preflight body=%d bytes exceeds bounded request budget", len(body))
	}
}

func TestTenThousandCapabilityVersionsFitBoundedBatches(t *testing.T) {
	capabilities := make([]Capability, 10_000)
	actions := make(map[string]PlanAction, len(capabilities))
	for index := range capabilities {
		id := fmt.Sprintf("capability-%05d", index)
		capabilities[index] = Capability{ID: id, Version: "1.0.0", Entrypoint: productionApplyArtifact(id + "/main.md")}
		actions[objectKey(ObjectRef{Kind: "capability", ID: id})] = PlanAction{Operation: PlanCreate, AfterDigest: testSHA256(id)}
	}
	versions, counts, err := collectCapabilityVersions(Snapshot{Manifest: Manifest{Capabilities: capabilities}}, actions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != len(capabilities) || counts.Created != len(capabilities) {
		t.Fatalf("capability batch versions=%d counts=%+v", len(versions), counts)
	}
	batches, err := capabilityVersionBatches(versions)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, batch := range batches {
		total += len(batch)
		if len(batch) > capabilityVersionBatchSize {
			t.Fatalf("capability batch items=%d", len(batch))
		}
		body, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > capabilityVersionBatchBytes {
			t.Fatalf("capability batch is %d bytes", len(body))
		}
	}
	if total != len(versions) {
		t.Fatalf("batched capabilities=%d want=%d", total, len(versions))
	}
}
