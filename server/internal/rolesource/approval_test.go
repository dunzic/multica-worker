package rolesource

import "testing"

func TestApprovalDecisionsRequireEveryArchiveCandidate(t *testing.T) {
	base := planTestSnapshot(t, planTestManifest())
	targetManifest := planTestManifest()
	targetManifest.Roles[0].Skills = nil
	target := planTestSnapshot(t, targetManifest)
	plan, err := BuildPlan("source-1", &base, target)
	if err != nil {
		t.Fatal(err)
	}
	decisions := &ApprovalDecisions{ContractVersion: PlanContractVersion}
	if err := ValidateApprovalDecisions(plan, "approved", decisions); err == nil {
		t.Fatal("approval accepted a missing archive decision")
	}
	for _, action := range plan.Actions {
		if action.Operation == PlanArchiveCandidate {
			decisions.Archives = append(decisions.Archives, ArchiveActionDecision{
				Ref: action.Ref, Decision: ArchiveDecisionRetain,
			})
		}
	}
	CanonicalizeApprovalDecisions(decisions)
	if err := ValidateApprovalDecisions(plan, "approved", decisions); err != nil {
		t.Fatalf("explicit archive decision rejected: %v", err)
	}
}

func TestApprovalDecisionsRejectBlockedPlanAndRejectedPayload(t *testing.T) {
	target := planTestSnapshot(t, planTestManifest())
	target.Diagnostics = []Diagnostic{{
		Severity: DiagnosticError, Code: "source_invalid", Message: "Source is invalid",
	}}
	refreshSnapshotDigest(t, &target)
	plan, err := BuildPlan("source-1", nil, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateApprovalDecisions(plan, "approved", &ApprovalDecisions{ContractVersion: PlanContractVersion}); err == nil {
		t.Fatal("approval accepted a blocked plan")
	}
	if err := ValidateApprovalDecisions(plan, "rejected", nil); err != nil {
		t.Fatalf("rejection without apply decisions failed: %v", err)
	}
	if err := ValidateApprovalDecisions(plan, "rejected", &ApprovalDecisions{ContractVersion: PlanContractVersion}); err == nil {
		t.Fatal("rejection accepted apply decisions")
	}
}
