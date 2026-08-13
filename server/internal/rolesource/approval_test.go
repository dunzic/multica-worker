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

func TestApprovalDecisionsRequireExactImmutableAdoptionCandidate(t *testing.T) {
	plan := Plan{
		ContractVersion: PlanContractVersion, SourceID: "source-1", ToSnapshotDigest: testSHA256("a"),
		Applyable: true, Summary: PlanSummary{Create: 1}, Actions: []PlanAction{{
			Ref: ObjectRef{Kind: "skill", ParentID: "writer", ID: "draft"}, DisplayName: "Draft",
			Operation: PlanCreate, Risk: PlanRiskHigh, AfterDigest: testSHA256("b"), Reason: "adoption candidate",
			AdoptionCandidate: &AdoptionCandidate{TargetKind: "skill", TargetID: "00000000-0000-4000-8000-000000000051", VersionCommitment: testSHA256("c")},
		}},
	}
	digest, err := digestPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = digest
	decisions := &ApprovalDecisions{ContractVersion: PlanContractVersion, Archives: []ArchiveActionDecision{}, Adoptions: []AdoptionActionDecision{}}
	if err := ValidateApprovalDecisions(plan, "approved", decisions); err == nil {
		t.Fatal("approval accepted a missing adoption decision")
	}
	decisions.Adoptions = []AdoptionActionDecision{{
		Ref: plan.Actions[0].Ref, TargetKind: "skill", TargetID: plan.Actions[0].AdoptionCandidate.TargetID,
		VersionCommitment: plan.Actions[0].AdoptionCandidate.VersionCommitment,
	}}
	if err := ValidateApprovalDecisions(plan, "approved", decisions); err != nil {
		t.Fatalf("exact adoption decision rejected: %v", err)
	}
	decisions.Adoptions[0].TargetID = "00000000-0000-4000-8000-000000000052"
	if err := ValidateApprovalDecisions(plan, "approved", decisions); err == nil {
		t.Fatal("approval accepted a substituted adoption target")
	}
}
