package daemon

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

var (
	roleSourceCapabilityIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	roleSourceCapabilityDigest    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	roleSourceCapabilityVersion   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

// applyRoleSourceCapabilityContract proves that every immutable capability pin
// still targets a skill bundle actually delivered for this task, then adds a
// source-neutral execution contract to the agent instructions. Capability
// package files must be materialized into that exact workspace skill before
// this contract is advertised; the pin and server-side target-state digest
// prevent a mutable bundle substitution.
func applyRoleSourceCapabilityContract(task *Task) error {
	if task == nil || task.RoleSourcePin == nil || len(task.RoleSourcePin.CapabilityPins) == 0 {
		return nil
	}
	if task.Agent == nil {
		return fmt.Errorf("%w: source capability task has no agent payload", errSkillBundleUnavailable)
	}
	skills := make(map[string]SkillData, len(task.Agent.Skills))
	for _, skill := range task.Agent.Skills {
		skills[skill.ID] = skill
	}
	pins := append([]RoleSourceCapabilityPin(nil), task.RoleSourcePin.CapabilityPins...)
	sort.Slice(pins, func(i, j int) bool {
		return pins[i].CapabilityID+"\x00"+pins[i].SkillID+"\x00"+pins[i].Profile < pins[j].CapabilityID+"\x00"+pins[j].SkillID+"\x00"+pins[j].Profile
	})
	seen := make(map[string]bool, len(pins))
	var contract strings.Builder
	contract.WriteString("## Pinned source capabilities\n\n")
	contract.WriteString("These capability contracts are part of this task's immutable source version. Use only the named bound skill and do not exceed its permission mode.\n\n")
	for _, pin := range pins {
		key := pin.CapabilityID + "\x00" + pin.SkillID + "\x00" + pin.Profile
		if seen[key] || !roleSourceCapabilityIDPattern.MatchString(pin.CapabilityID) ||
			!roleSourceCapabilityIDPattern.MatchString(pin.SkillID) ||
			!roleSourceCapabilityIDPattern.MatchString(pin.TargetSkillID) ||
			!roleSourceCapabilityIDPattern.MatchString(pin.Profile) ||
			!roleSourceCapabilityVersion.MatchString(pin.ResolvedVersion) ||
			!roleSourceCapabilityDigest.MatchString(pin.ObjectDigest) ||
			(pin.PermissionMode != "read-only" && pin.PermissionMode != "local-write" && pin.PermissionMode != "external-write") {
			return fmt.Errorf("%w: malformed source capability pin", errSkillBundleUnavailable)
		}
		seen[key] = true
		skill, ok := skills[pin.TargetSkillID]
		if !ok {
			return fmt.Errorf("%w: capability %s targets missing skill %s", errSkillBundleUnavailable, pin.CapabilityID, pin.TargetSkillID)
		}
		fmt.Fprintf(&contract, "- `%s` version `%s`, profile `%s`, permission `%s`, bound skill `%s` (`%s`), required `%t`.\n",
			pin.CapabilityID, pin.ResolvedVersion, pin.Profile, pin.PermissionMode, skill.Name, pin.SkillID, pin.Required)
	}
	if strings.TrimSpace(task.Agent.Instructions) != "" {
		task.Agent.Instructions += "\n\n"
	}
	task.Agent.Instructions += contract.String()
	return nil
}

// Local aliases keep the daemon wire model explicitly tied to the shared
// protocol without duplicating the capability-pin schema.
type RoleSourceCapabilityPin = protocol.RoleSourceCapabilityPin
