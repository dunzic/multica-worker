package rolesource

import (
	"errors"
	"fmt"
	"sort"
)

type ArchiveDecision string

const (
	ArchiveDecisionArchive ArchiveDecision = "archive"
	ArchiveDecisionRetain  ArchiveDecision = "retain"
)

// ApprovalDecisions is deliberately closed and source-neutral. The initial
// contract requires an explicit choice for every archive candidate; future
// conflict/adoption decisions extend this typed structure under a new contract
// version rather than accepting arbitrary JSON.
type ApprovalDecisions struct {
	ContractVersion string                  `json:"contract_version"`
	Archives        []ArchiveActionDecision `json:"archives"`
}

type ArchiveActionDecision struct {
	Ref      ObjectRef       `json:"ref"`
	Decision ArchiveDecision `json:"decision"`
}

func ValidateApprovalDecisions(plan Plan, decision string, decisions *ApprovalDecisions) error {
	if err := ValidatePlan(plan); err != nil {
		return err
	}
	switch decision {
	case "rejected":
		if decisions != nil {
			return errors.New("rejected approval must not include apply decisions")
		}
		return nil
	case "approved":
	default:
		return fmt.Errorf("invalid approval decision %q", decision)
	}
	if !plan.Applyable {
		return errors.New("blocked plan cannot be approved")
	}
	if decisions == nil || decisions.ContractVersion != PlanContractVersion {
		return errors.New("approved plan requires matching approval decisions contract")
	}

	wanted := make(map[string]ObjectRef)
	for _, action := range plan.Actions {
		if action.Operation == PlanArchiveCandidate {
			wanted[objectKey(action.Ref)] = action.Ref
		}
	}
	seen := make(map[string]bool, len(decisions.Archives))
	previous := ""
	for index, archive := range decisions.Archives {
		if err := validateObjectRef(archive.Ref); err != nil {
			return fmt.Errorf("archive decision %d: %w", index, err)
		}
		key := objectKey(archive.Ref)
		if index > 0 && key <= previous {
			return errors.New("archive decisions are not in unique canonical order")
		}
		previous = key
		if _, ok := wanted[key]; !ok {
			return fmt.Errorf("archive decision %d does not match a plan archive candidate", index)
		}
		if archive.Decision != ArchiveDecisionArchive && archive.Decision != ArchiveDecisionRetain {
			return fmt.Errorf("archive decision %d has invalid value %q", index, archive.Decision)
		}
		seen[key] = true
	}
	if len(seen) != len(wanted) {
		return errors.New("every archive candidate requires an explicit archive or retain decision")
	}
	return nil
}

func CanonicalizeApprovalDecisions(decisions *ApprovalDecisions) {
	if decisions == nil {
		return
	}
	sort.Slice(decisions.Archives, func(i, j int) bool {
		return objectKey(decisions.Archives[i].Ref) < objectKey(decisions.Archives[j].Ref)
	})
}
