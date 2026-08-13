package handler

import (
	"os"
	"strings"
	"testing"
)

func TestAutopilotWritesHonorRoleSourceTitleClaims(t *testing.T) {
	body, err := os.ReadFile("autopilot.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	assertOrder := func(functionName string, nextFunctionName string, required ...string) {
		t.Helper()
		start := strings.Index(text, "func (h *Handler) "+functionName)
		if start < 0 {
			t.Fatalf("%s is missing", functionName)
		}
		section := text[start:]
		if end := strings.Index(section[1:], "\nfunc (h *Handler) "+nextFunctionName); end >= 0 {
			section = section[:end+1]
		}
		position := -1
		for _, fragment := range required {
			next := strings.Index(section, fragment)
			if next < 0 || next <= position {
				t.Fatalf("%s must contain %q after the prior title-policy step", functionName, fragment)
			}
			position = next
		}
	}
	assertOrder("CreateAutopilot", "UpdateAutopilot",
		"autopilotlock.LockTitles", "autopilotlock.HasTitleConflict", "qtx.CreateAutopilot")
	assertOrder("UpdateAutopilot", "DeleteAutopilot",
		"autopilotlock.LockTitles", "autopilotlock.HasTitleConflict", "qtx.LockAutopilotForUpdate", "qtx.UpdateAutopilot")
	if !strings.Contains(text, "prev.WorkspaceID, prev.Title, nextTitle") {
		t.Fatal("rename must lock both old and new title namespaces in canonical order")
	}
}
