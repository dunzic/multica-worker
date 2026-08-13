package rolesource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
)

const maxNormalizedObjects = 100_000

type planObject struct {
	ref                 ObjectRef
	displayName         string
	roleID              string
	needsSecretTransfer bool
	digest              string
}

// BuildPlan compares immutable snapshots without consulting mutable source
// data. A nil from snapshot represents initial import. Deletes are deliberately
// archive candidates; ownership/adoption decisions are resolved by the later
// materialization gate and recorded in the persisted approval.
func BuildPlan(sourceID string, from *Snapshot, to Snapshot) (Plan, error) {
	if !stableIDPattern.MatchString(sourceID) {
		return Plan{}, fmt.Errorf("invalid source id %q", sourceID)
	}
	canonicalTo, err := validatedSnapshotCopy(to)
	if err != nil {
		return Plan{}, fmt.Errorf("validate target snapshot: %w", err)
	}
	if from != nil && from.Kind != canonicalTo.Kind {
		return Plan{}, fmt.Errorf("snapshot adapter kind changed from %q to %q", from.Kind, canonicalTo.Kind)
	}

	var canonicalFrom *Snapshot
	if from != nil {
		copy, err := validatedSnapshotCopy(*from)
		if err != nil {
			return Plan{}, fmt.Errorf("validate base snapshot: %w", err)
		}
		canonicalFrom = &copy
	}

	before, err := flattenSnapshot(canonicalFrom)
	if err != nil {
		return Plan{}, err
	}
	after, err := flattenSnapshot(&canonicalTo)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		ContractVersion:  PlanContractVersion,
		SourceID:         sourceID,
		ToSnapshotDigest: canonicalTo.SnapshotDigest,
		Applyable:        true,
		Actions:          []PlanAction{},
		Blockers:         blockersFromDiagnostics(canonicalTo.Diagnostics),
	}
	plan.Blockers = append(plan.Blockers, materializationPlanBlockers(canonicalTo)...)
	sortPlanBlockers(plan.Blockers)
	if canonicalFrom != nil {
		plan.FromSnapshotDigest = canonicalFrom.SnapshotDigest
	}

	keys := make([]string, 0, len(before)+len(after))
	seen := make(map[string]bool, len(before)+len(after))
	for key := range before {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range after {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		oldObject, hadOld := before[key]
		newObject, hasNew := after[key]
		action := makePlanAction(oldObject, hadOld, newObject, hasNew)
		diagnostics := blockingCodes(plan.Blockers, action.Ref, newObject.roleID)
		if len(diagnostics) > 0 && action.Operation != PlanArchiveCandidate {
			action.ProposedOperation = action.Operation
			action.Operation = PlanBlocked
			action.Risk = PlanRiskHigh
			action.Reason = "target snapshot has blocking diagnostics for this object"
			action.BlockingDiagnostics = diagnostics
		}
		plan.Actions = append(plan.Actions, action)
		incrementPlanSummary(&plan.Summary, action.Operation)
	}

	if len(plan.Blockers) > 0 || plan.Summary.Blocked > 0 {
		plan.Applyable = false
	}
	digest, err := digestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}

// BuildRollbackPlan creates a new forward plan whose target is an immutable
// historical snapshot. Mode participates in the digest so rollback intent can
// never be confused with an ordinary reconciliation of the same snapshot pair.
func BuildRollbackPlan(sourceID string, from Snapshot, historicalTarget Snapshot) (Plan, error) {
	plan, err := BuildPlan(sourceID, &from, historicalTarget)
	if err != nil {
		return Plan{}, err
	}
	if from.SnapshotDigest == historicalTarget.SnapshotDigest {
		return Plan{}, errors.New("rollback target is already active")
	}
	plan.Mode = PlanModeRollback
	plan.PlanDigest = ""
	digest, err := digestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}

// ValidatePlan verifies a plan loaded from persistence before approval or
// apply. It detects corruption/tampering and rejects semantically inconsistent
// summaries, operations and ordering rather than trusting JSONB fields.
func ValidatePlan(plan Plan) error {
	if plan.ContractVersion != PlanContractVersion {
		return fmt.Errorf("plan contract %q, expected %q", plan.ContractVersion, PlanContractVersion)
	}
	if !stableIDPattern.MatchString(plan.SourceID) {
		return fmt.Errorf("invalid source id %q", plan.SourceID)
	}
	if plan.Mode != "" && plan.Mode != PlanModeRollback {
		return fmt.Errorf("invalid plan mode %q", plan.Mode)
	}
	if !sha256Pattern.MatchString(plan.ToSnapshotDigest) ||
		(plan.FromSnapshotDigest != "" && !sha256Pattern.MatchString(plan.FromSnapshotDigest)) ||
		!sha256Pattern.MatchString(plan.PlanDigest) {
		return errors.New("plan contains an invalid digest")
	}
	if len(plan.Actions) > maxNormalizedObjects || len(plan.Blockers) > 10_000 {
		return errors.New("plan action or blocker count exceeds hard limit")
	}

	var summary PlanSummary
	previousKey := ""
	for index, action := range plan.Actions {
		if err := validateObjectRef(action.Ref); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
		key := objectKey(action.Ref)
		if index > 0 && key <= previousKey {
			return errors.New("plan actions are not in unique canonical order")
		}
		previousKey = key
		if len(action.DisplayName) > 1024 || action.Reason == "" || len(action.Reason) > 4096 {
			return fmt.Errorf("action %d contains invalid display name or reason", index)
		}
		if action.BeforeDigest != "" && !sha256Pattern.MatchString(action.BeforeDigest) {
			return fmt.Errorf("action %d has invalid before digest", index)
		}
		if action.AfterDigest != "" && !sha256Pattern.MatchString(action.AfterDigest) {
			return fmt.Errorf("action %d has invalid after digest", index)
		}
		if err := validateActionSemantics(action); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
		incrementPlanSummary(&summary, action.Operation)
	}
	if summary != plan.Summary {
		return fmt.Errorf("plan summary does not match actions: got %+v, computed %+v", plan.Summary, summary)
	}

	for index, blocker := range plan.Blockers {
		if !stableIDPattern.MatchString(blocker.Code) || blocker.Message == "" || len(blocker.Message) > 4096 {
			return fmt.Errorf("blocker %d contains invalid code or message", index)
		}
		if blocker.Object != (ObjectRef{}) {
			if err := validateObjectRef(blocker.Object); err != nil {
				return fmt.Errorf("blocker %d: %w", index, err)
			}
		}
	}
	wantApplyable := len(plan.Blockers) == 0 && plan.Summary.Blocked == 0
	if plan.Applyable != wantApplyable {
		return fmt.Errorf("plan applyable is %t, expected %t", plan.Applyable, wantApplyable)
	}
	digest, err := digestPlan(plan)
	if err != nil {
		return err
	}
	if digest != plan.PlanDigest {
		return fmt.Errorf("plan digest mismatch: got %q, computed %q", plan.PlanDigest, digest)
	}
	return nil
}

func validateObjectRef(ref ObjectRef) error {
	switch ref.Kind {
	case "capability", "role":
		if ref.ParentID != "" {
			return errors.New("top-level object must not have parent_id")
		}
	case "skill", "capability_binding", "environment", "mcp", "automation":
		if !stableIDPattern.MatchString(ref.ParentID) {
			return errors.New("role-owned object requires a valid parent_id")
		}
	default:
		return fmt.Errorf("unknown object kind %q", ref.Kind)
	}
	if !stableIDPattern.MatchString(ref.ID) {
		return fmt.Errorf("invalid object id %q", ref.ID)
	}
	return nil
}

func validateActionSemantics(action PlanAction) error {
	if action.AdoptionCandidate != nil {
		candidate := action.AdoptionCandidate
		if action.Operation != PlanCreate || action.Risk != PlanRiskHigh || (candidate.TargetKind != "agent" && candidate.TargetKind != "skill" && candidate.TargetKind != "autopilot") {
			return errors.New("adoption candidate must belong to a create action and supported target kind")
		}
		if _, err := util.ParseUUID(candidate.TargetID); err != nil || !sha256Pattern.MatchString(candidate.VersionCommitment) {
			return errors.New("adoption candidate has invalid target identity or version commitment")
		}
		expectedKind, ok := materializationTargetKind(action.Ref.Kind)
		if !ok || expectedKind != candidate.TargetKind {
			return errors.New("adoption candidate target kind does not match source object")
		}
	}
	if action.NeedsSecretTransfer && (action.Ref.Kind != "role" || action.Operation == PlanArchiveCandidate || action.AfterDigest == "") {
		return errors.New("secret transfer requirement must identify a target role")
	}
	validRisk := action.Risk == PlanRiskNone || action.Risk == PlanRiskLow || action.Risk == PlanRiskMedium || action.Risk == PlanRiskHigh
	if !validRisk {
		return fmt.Errorf("invalid risk %q", action.Risk)
	}
	switch action.Operation {
	case PlanCreate:
		if action.BeforeDigest != "" || action.AfterDigest == "" || action.ProposedOperation != "" {
			return errors.New("create action has inconsistent digests or proposed operation")
		}
	case PlanUpdate:
		if action.BeforeDigest == "" || action.AfterDigest == "" || action.BeforeDigest == action.AfterDigest || action.ProposedOperation != "" {
			return errors.New("update action has inconsistent digests or proposed operation")
		}
	case PlanUnchanged:
		if action.BeforeDigest == "" || action.BeforeDigest != action.AfterDigest || action.ProposedOperation != "" {
			return errors.New("unchanged action has inconsistent digests or proposed operation")
		}
	case PlanArchiveCandidate:
		if action.BeforeDigest == "" || action.AfterDigest != "" || action.ProposedOperation != "" {
			return errors.New("archive candidate has inconsistent digests or proposed operation")
		}
	case PlanBlocked:
		if action.ProposedOperation != PlanCreate && action.ProposedOperation != PlanUpdate && action.ProposedOperation != PlanUnchanged {
			return errors.New("blocked action requires its proposed operation")
		}
		if len(action.BlockingDiagnostics) == 0 {
			return errors.New("blocked action requires diagnostic codes")
		}
	default:
		return fmt.Errorf("invalid operation %q", action.Operation)
	}
	return nil
}

func materializationTargetKind(sourceKind string) (string, bool) {
	switch sourceKind {
	case "role":
		return "agent", true
	case "skill":
		return "skill", true
	case "automation":
		return "autopilot", true
	default:
		return "", false
	}
}

func validatedSnapshotCopy(snapshot Snapshot) (Snapshot, error) {
	if snapshot.Kind == "" || strings.TrimSpace(snapshot.AdapterVersion) == "" {
		return Snapshot{}, errors.New("snapshot requires adapter kind and version")
	}
	if snapshot.ContractVersion != ContractVersion {
		return Snapshot{}, fmt.Errorf("snapshot contract %q, expected %q", snapshot.ContractVersion, ContractVersion)
	}
	if err := validateSourceEvidence(snapshot.SourceEvidence); err != nil {
		return Snapshot{}, err
	}
	if err := validateDiagnostics(snapshot.Diagnostics); err != nil {
		return Snapshot{}, err
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	var copy Snapshot
	if err := json.Unmarshal(body, &copy); err != nil {
		return Snapshot{}, err
	}
	canonicalizeDiagnostics(copy.Diagnostics)
	if err := validateManifest(&copy.Manifest); err != nil {
		return Snapshot{}, err
	}
	digest, err := digestManifest(copy.Manifest)
	if err != nil {
		return Snapshot{}, err
	}
	if digest != snapshot.ManifestDigest {
		return Snapshot{}, fmt.Errorf("manifest digest mismatch: got %q, computed %q", snapshot.ManifestDigest, digest)
	}
	snapshotDigest, err := digestSnapshot(copy)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshotDigest != snapshot.SnapshotDigest {
		return Snapshot{}, fmt.Errorf("snapshot digest mismatch: got %q, computed %q", snapshot.SnapshotDigest, snapshotDigest)
	}
	return copy, nil
}

func flattenSnapshot(snapshot *Snapshot) (map[string]planObject, error) {
	objects := map[string]planObject{}
	if snapshot == nil {
		return objects, nil
	}
	add := func(ref ObjectRef, displayName, roleID string, value any) error {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		object := planObject{
			ref: ref, displayName: displayName, roleID: roleID,
			digest: "sha256:" + hex.EncodeToString(sum[:]),
		}
		key := objectKey(ref)
		if _, exists := objects[key]; exists {
			return fmt.Errorf("duplicate normalized object %s", key)
		}
		objects[key] = object
		if len(objects) > maxNormalizedObjects {
			return errors.New("normalized object count exceeds hard limit")
		}
		return nil
	}

	for _, capability := range snapshot.Manifest.Capabilities {
		if err := add(ObjectRef{Kind: "capability", ID: capability.ID}, capability.Name, "", capability); err != nil {
			return nil, err
		}
	}
	for _, role := range snapshot.Manifest.Roles {
		roleCore := struct {
			ID           string       `json:"id"`
			DisplayName  string       `json:"display_name"`
			Version      string       `json:"version,omitempty"`
			Lifecycle    string       `json:"lifecycle,omitempty"`
			Instructions ArtifactRef  `json:"instructions"`
			Profile      *ArtifactRef `json:"profile,omitempty"`
		}{role.ID, role.DisplayName, role.Version, role.Lifecycle, role.Instructions, role.Profile}
		if err := add(ObjectRef{Kind: "role", ID: role.ID}, role.DisplayName, role.ID, roleCore); err != nil {
			return nil, err
		}
		roleObject := objects[objectKey(ObjectRef{Kind: "role", ID: role.ID})]
		roleObject.needsSecretTransfer = roleNeedsSecretTransfer(role)
		objects[objectKey(roleObject.ref)] = roleObject
		for _, skill := range role.Skills {
			if err := add(ObjectRef{Kind: "skill", ParentID: role.ID, ID: skill.ID}, skill.Name, role.ID, skill); err != nil {
				return nil, err
			}
		}
		for _, binding := range role.CapabilityBindings {
			id := capabilityBindingObjectID(binding)
			displayName := binding.CapabilityID + "/" + binding.SkillID + "/" + binding.Profile
			if err := add(ObjectRef{Kind: "capability_binding", ParentID: role.ID, ID: id}, displayName, role.ID, binding); err != nil {
				return nil, err
			}
		}
		for _, environment := range role.Environment {
			if err := add(ObjectRef{Kind: "environment", ParentID: role.ID, ID: environment.Name}, environment.Name, role.ID, environment); err != nil {
				return nil, err
			}
		}
		for _, server := range role.MCP {
			if err := add(ObjectRef{Kind: "mcp", ParentID: role.ID, ID: server.ID}, server.ID, role.ID, server); err != nil {
				return nil, err
			}
		}
		for _, automation := range role.Automations {
			if err := add(ObjectRef{Kind: "automation", ParentID: role.ID, ID: automation.ID}, automation.Name, role.ID, automation); err != nil {
				return nil, err
			}
		}
	}
	return objects, nil
}

func capabilityBindingObjectID(binding CapabilityBinding) string {
	digest := sha256.Sum256([]byte(binding.CapabilityID + "\x00" + binding.SkillID + "\x00" + binding.Profile))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func objectKey(ref ObjectRef) string {
	return ref.Kind + "\x00" + ref.ParentID + "\x00" + ref.ID
}

func makePlanAction(before planObject, hadBefore bool, after planObject, hasAfter bool) PlanAction {
	switch {
	case !hadBefore && hasAfter:
		return PlanAction{Ref: after.ref, DisplayName: after.displayName, NeedsSecretTransfer: after.needsSecretTransfer, Operation: PlanCreate, Risk: PlanRiskLow, AfterDigest: after.digest, Reason: "object is new in the target snapshot"}
	case hadBefore && !hasAfter:
		return PlanAction{Ref: before.ref, DisplayName: before.displayName, Operation: PlanArchiveCandidate, Risk: PlanRiskHigh, BeforeDigest: before.digest, Reason: "object was removed from the source; explicit archive policy is required"}
	case before.digest == after.digest:
		return PlanAction{Ref: after.ref, DisplayName: after.displayName, NeedsSecretTransfer: after.needsSecretTransfer, Operation: PlanUnchanged, Risk: PlanRiskNone, BeforeDigest: before.digest, AfterDigest: after.digest, Reason: "normalized object digest is unchanged"}
	default:
		return PlanAction{Ref: after.ref, DisplayName: after.displayName, NeedsSecretTransfer: after.needsSecretTransfer, Operation: PlanUpdate, Risk: PlanRiskMedium, BeforeDigest: before.digest, AfterDigest: after.digest, Reason: "normalized object digest changed"}
	}
}

func blockersFromDiagnostics(diagnostics []Diagnostic) []PlanBlocker {
	blockers := []PlanBlocker{}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != DiagnosticError {
			continue
		}
		blocker := PlanBlocker{Code: diagnostic.Code, Message: diagnostic.Message, Global: true}
		if diagnostic.ObjectKind != "" && diagnostic.ObjectID != "" {
			blocker.Object = ObjectRef{Kind: diagnostic.ObjectKind, ID: diagnostic.ObjectID}
			blocker.Global = diagnostic.ObjectKind != "role"
		}
		blockers = append(blockers, blocker)
	}
	sortPlanBlockers(blockers)
	return blockers
}

func sortPlanBlockers(blockers []PlanBlocker) {
	sort.Slice(blockers, func(i, j int) bool {
		a, b := blockers[i], blockers[j]
		return fmt.Sprintf("%t\x00%s\x00%s\x00%s\x00%s", a.Global, a.Object.Kind, a.Object.ParentID, a.Object.ID, a.Code) <
			fmt.Sprintf("%t\x00%s\x00%s\x00%s\x00%s", b.Global, b.Object.Kind, b.Object.ParentID, b.Object.ID, b.Code)
	})
}

func materializationPlanBlockers(snapshot Snapshot) []PlanBlocker {
	blockers := []PlanBlocker{}
	secretTransferRoles := 0
	capabilities := make(map[string]Capability, len(snapshot.Manifest.Capabilities))
	for _, capability := range snapshot.Manifest.Capabilities {
		capabilities[capability.ID] = capability
	}
	for _, role := range snapshot.Manifest.Roles {
		if roleNeedsSecretTransfer(role) {
			secretTransferRoles++
		}
		if role.Profile != nil {
			blockers = append(blockers, PlanBlocker{
				Code: "role_profile_target_unsupported", Message: "role profile has no safe Multica target contract", Global: false,
				Object: ObjectRef{Kind: "role", ID: role.ID},
			})
		}
		for _, binding := range role.CapabilityBindings {
			ref := ObjectRef{Kind: "capability_binding", ParentID: role.ID, ID: capabilityBindingObjectID(binding)}
			if binding.PermissionMode != "read-only" {
				blockers = append(blockers, PlanBlocker{
					Code: "capability_write_boundary_unavailable", Message: "capability requests write authority without a hard runtime permission boundary", Global: false, Object: ref,
				})
			}
			for _, requirement := range capabilities[binding.CapabilityID].Requirements.Adapters {
				if requirement.Required {
					blockers = append(blockers, PlanBlocker{
						Code: "capability_adapter_unresolved", Message: "capability requires an executable adapter that is not bound to the target runtime", Global: false, Object: ref,
					})
					break
				}
			}
		}
	}
	if secretTransferRoles > maxSecretEnvelopeValues {
		blockers = append(blockers, PlanBlocker{
			Code: "secret_transfer_role_limit", Message: "snapshot exceeds the bounded secret-transfer role limit", Global: true,
		})
	}
	return blockers
}

func blockingCodes(blockers []PlanBlocker, ref ObjectRef, roleID string) []string {
	codes := []string{}
	for _, blocker := range blockers {
		if blocker.Global || (blocker.Object.Kind == "role" && blocker.Object.ID == roleID) || blocker.Object == ref {
			codes = append(codes, blocker.Code)
		}
	}
	sort.Strings(codes)
	return codes
}

func incrementPlanSummary(summary *PlanSummary, operation PlanOperation) {
	switch operation {
	case PlanCreate:
		summary.Create++
	case PlanUpdate:
		summary.Update++
	case PlanUnchanged:
		summary.Unchanged++
	case PlanArchiveCandidate:
		summary.ArchiveCandidate++
	case PlanBlocked:
		summary.Blocked++
	}
}

func digestPlan(plan Plan) (string, error) {
	plan.PlanDigest = ""
	body, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
