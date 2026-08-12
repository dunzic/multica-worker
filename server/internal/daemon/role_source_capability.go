package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
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
		if _, duplicate := skills[skill.ID]; duplicate {
			return fmt.Errorf("%w: duplicate bound skill identity %s", errSkillBundleUnavailable, skill.ID)
		}
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
			(pin.PermissionMode != "read-only" && pin.PermissionMode != "local-write" && pin.PermissionMode != "external-write") ||
			(pin.Fallback != "" && pin.Fallback != "continue" && pin.Fallback != "partial" && pin.Fallback != "blocked") {
			return fmt.Errorf("%w: malformed source capability pin", errSkillBundleUnavailable)
		}
		seen[key] = true
		skill, ok := skills[pin.TargetSkillID]
		if !ok {
			return fmt.Errorf("%w: capability %s targets missing skill %s", errSkillBundleUnavailable, pin.CapabilityID, pin.TargetSkillID)
		}
		entrypoint, err := verifyRoleSourceCapabilityBundle(skill, pin)
		if err != nil {
			return err
		}
		fmt.Fprintf(&contract, "- `%s` version `%s`, profile `%s`, permission `%s`, bound skill `%s` (`%s`), required `%t`, fallback `%s`, entrypoint `%s`.\n",
			pin.CapabilityID, pin.ResolvedVersion, pin.Profile, pin.PermissionMode, skill.Name, pin.SkillID, pin.Required, pin.Fallback, entrypoint)
	}
	if strings.TrimSpace(task.Agent.Instructions) != "" {
		task.Agent.Instructions += "\n\n"
	}
	task.Agent.Instructions += contract.String()
	return nil
}

func verifyRoleSourceCapabilityBundle(skill SkillData, pin protocol.RoleSourceCapabilityPin) (string, error) {
	files := make(map[string]SkillFileData, len(skill.Files))
	for _, file := range skill.Files {
		if _, duplicate := files[file.Path]; duplicate {
			return "", fmt.Errorf("%w: bound skill has duplicate file %s", errSkillBundleUnavailable, file.Path)
		}
		files[file.Path] = file
	}
	markerPath := protocol.RoleSourceCapabilityMarkerPath(pin.CapabilityID, pin.SkillID, pin.Profile)
	marker, ok := files[markerPath]
	if !ok {
		return "", fmt.Errorf("%w: capability %s marker is missing", errSkillBundleUnavailable, pin.CapabilityID)
	}
	var bundle protocol.RoleSourceCapabilityBundle
	decoder := json.NewDecoder(strings.NewReader(marker.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return "", fmt.Errorf("%w: capability %s marker is invalid", errSkillBundleUnavailable, pin.CapabilityID)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("%w: capability %s marker has trailing data", errSkillBundleUnavailable, pin.CapabilityID)
	}
	if bundle.ContractVersion != protocol.RoleSourceCapabilityBundleContractV1 ||
		bundle.CapabilityID != pin.CapabilityID || bundle.SourceSkillID != pin.SkillID ||
		bundle.Profile != pin.Profile || bundle.ResolvedVersion != pin.ResolvedVersion ||
		bundle.ObjectDigest != pin.ObjectDigest || bundle.PermissionMode != pin.PermissionMode ||
		bundle.Required != pin.Required || bundle.Fallback != pin.Fallback || len(bundle.Files) == 0 {
		return "", fmt.Errorf("%w: capability %s marker does not match task pin", errSkillBundleUnavailable, pin.CapabilityID)
	}
	prefix := strings.TrimSuffix(markerPath, "manifest.json")
	declared := map[string]bool{markerPath: true}
	entrypointFound := false
	for _, declaredFile := range bundle.Files {
		if declared[declaredFile.TargetPath] || !strings.HasPrefix(declaredFile.TargetPath, prefix+"files/") ||
			path.Clean(declaredFile.TargetPath) != declaredFile.TargetPath ||
			!roleSourceCapabilityDigest.MatchString(declaredFile.Digest) || declaredFile.SizeBytes < 0 {
			return "", fmt.Errorf("%w: capability %s file manifest is malformed", errSkillBundleUnavailable, pin.CapabilityID)
		}
		declared[declaredFile.TargetPath] = true
		file, ok := files[declaredFile.TargetPath]
		if !ok || int64(len([]byte(file.Content))) != declaredFile.SizeBytes {
			return "", fmt.Errorf("%w: capability %s file %s is missing or has wrong size", errSkillBundleUnavailable, pin.CapabilityID, declaredFile.SourcePath)
		}
		digest := sha256.Sum256([]byte(file.Content))
		if "sha256:"+hex.EncodeToString(digest[:]) != declaredFile.Digest {
			return "", fmt.Errorf("%w: capability %s file %s digest mismatch", errSkillBundleUnavailable, pin.CapabilityID, declaredFile.SourcePath)
		}
		if declaredFile.TargetPath == bundle.EntrypointPath {
			entrypointFound = true
		}
	}
	if !entrypointFound {
		return "", fmt.Errorf("%w: capability %s entrypoint is not declared", errSkillBundleUnavailable, pin.CapabilityID)
	}
	for filePath := range files {
		if strings.HasPrefix(filePath, prefix) && !declared[filePath] {
			return "", fmt.Errorf("%w: capability %s bundle has undeclared file", errSkillBundleUnavailable, pin.CapabilityID)
		}
	}
	return bundle.EntrypointPath, nil
}

// Local aliases keep the daemon wire model explicitly tied to the shared
// protocol without duplicating the capability-pin schema.
type RoleSourceCapabilityPin = protocol.RoleSourceCapabilityPin
