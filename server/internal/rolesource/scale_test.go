package rolesource

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
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
