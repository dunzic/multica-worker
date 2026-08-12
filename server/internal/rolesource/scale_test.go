package rolesource

import (
	"fmt"
	"testing"
	"time"
)

const productionScaleRoleCount = 10_000

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
