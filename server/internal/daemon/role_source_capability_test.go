package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestApplyRoleSourceCapabilityContractRequiresExactDeliveredSkill(t *testing.T) {
	pin := protocol.RoleSourceCapabilityPin{
		CapabilityID: "information-collection", SkillID: "research",
		TargetSkillID: "00000000-0000-4000-8000-000000000099",
		Profile:       "research", VersionConstraint: "^1.0.0", ResolvedVersion: "1.2.3",
		ObjectDigest: "sha256:" + strings.Repeat("a", 64), PermissionMode: "read-only", Required: true,
	}
	task := Task{
		RoleSourcePin: &protocol.RoleSourceTaskPin{CapabilityPins: []protocol.RoleSourceCapabilityPin{pin}},
		Agent:         &AgentData{Instructions: "Base", Skills: []SkillData{capabilityTestSkill(t, pin)}},
	}
	if err := applyRoleSourceCapabilityContract(&task); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Pinned source capabilities", "information-collection", "read-only", "Research"} {
		if !strings.Contains(task.Agent.Instructions, expected) {
			t.Fatalf("capability contract missing %q: %s", expected, task.Agent.Instructions)
		}
	}

	task.Agent.Skills = nil
	if err := applyRoleSourceCapabilityContract(&task); !errors.Is(err, errSkillBundleUnavailable) {
		t.Fatalf("missing target skill error=%v", err)
	}
}

func capabilityTestSkill(t *testing.T, pin protocol.RoleSourceCapabilityPin) SkillData {
	t.Helper()
	markerPath := protocol.RoleSourceCapabilityMarkerPath(pin.CapabilityID, pin.SkillID, pin.Profile)
	targetPath := strings.TrimSuffix(markerPath, "manifest.json") + "files/entrypoint.artifact"
	body := "capability body"
	digest := sha256.Sum256([]byte(body))
	bundle := protocol.RoleSourceCapabilityBundle{
		ContractVersion: protocol.RoleSourceCapabilityBundleContractV1,
		CapabilityID:    pin.CapabilityID, SourceSkillID: pin.SkillID, Profile: pin.Profile,
		ResolvedVersion: pin.ResolvedVersion, ObjectDigest: pin.ObjectDigest,
		PermissionMode: pin.PermissionMode, Required: pin.Required, EntrypointPath: targetPath,
		Fallback: pin.Fallback,
		Files: []protocol.RoleSourceCapabilityBundleFile{{
			SourcePath: "capability/SKILL.md", TargetPath: targetPath,
			Digest: "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(body)), MediaType: "text/markdown",
		}},
	}
	marker, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return SkillData{ID: pin.TargetSkillID, Name: "Research", Files: []SkillFileData{
		{Path: markerPath, Content: string(marker)}, {Path: targetPath, Content: body},
	}}
}

func TestApplyRoleSourceCapabilityContractRejectsTamperedBundle(t *testing.T) {
	pin := protocol.RoleSourceCapabilityPin{
		CapabilityID: "cap", SkillID: "skill", TargetSkillID: "target", Profile: "default",
		ResolvedVersion: "1.0.0", ObjectDigest: "sha256:" + strings.Repeat("b", 64), PermissionMode: "read-only",
	}
	skill := capabilityTestSkill(t, pin)
	skill.Files[1].Content = "tampered"
	task := Task{RoleSourcePin: &protocol.RoleSourceTaskPin{CapabilityPins: []protocol.RoleSourceCapabilityPin{pin}}, Agent: &AgentData{Skills: []SkillData{skill}}}
	if err := applyRoleSourceCapabilityContract(&task); !errors.Is(err, errSkillBundleUnavailable) {
		t.Fatalf("tampered bundle error=%v", err)
	}
}

func TestApplyRoleSourceCapabilityContractRejectsMalformedOrDuplicatePins(t *testing.T) {
	pin := protocol.RoleSourceCapabilityPin{
		CapabilityID: "cap", SkillID: "skill", TargetSkillID: "target", Profile: "default",
		ResolvedVersion: "1.0.0", ObjectDigest: "sha256:" + strings.Repeat("b", 64), PermissionMode: "read-only",
	}
	task := Task{
		RoleSourcePin: &protocol.RoleSourceTaskPin{CapabilityPins: []protocol.RoleSourceCapabilityPin{pin, pin}},
		Agent:         &AgentData{Skills: []SkillData{{ID: "target", Name: "Target"}}},
	}
	if err := applyRoleSourceCapabilityContract(&task); !errors.Is(err, errSkillBundleUnavailable) {
		t.Fatalf("duplicate pin error=%v", err)
	}
}
