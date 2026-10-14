package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroupFollowupResumesProviderSession(t *testing.T) {
	d, argsFile, cleanup := newLeaderReuseTestDaemon(t)
	defer cleanup()
	first := leaderReuseTestTask("group-first")
	first.IsLeaderTask = false
	first.IssueID = ""
	first.TopicID = "group-topic"
	first.ChatType = "group"
	first.ChatMessage = "Review first batch"
	result, err := d.runTask(context.Background(), first, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || result.WorkDir == "" {
		t.Fatalf("no resume state: %+v", result)
	}
	second := first
	second.ID = "group-second"
	second.ChatMessage = "Continue the previous batch"
	second.PriorSessionID = result.SessionID
	second.PriorWorkDir = result.WorkDir
	next, err := d.runTask(context.Background(), second, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, next.WorkDir, result.WorkDir) {
		t.Errorf("group followup starts a new workdir despite valid prior workdir")
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--resume\nsession-leader-reuse\n") {
		t.Errorf("second invocation omitted --resume despite valid prior session")
	}
	for _, field := range []string{"topic", "workspace", "agent", "dm", "issue"} {
		t.Run("reject mismatched "+field, func(t *testing.T) {
			wrong := second
			switch field {
			case "topic":
				wrong.TopicID = "other-topic"
			case "workspace":
				wrong.WorkspaceID = "other-workspace"
			case "agent":
				wrong.AgentID = "other-agent"
			case "dm":
				wrong.ChatSessionID = "group-topic"
			case "issue":
				wrong.IssueID = "group-topic"
			}
			// Derive the managed root from the first Prepare result, not a
			// user-supplied directory.
			root := filepath.Dir(filepath.Dir(result.EnvRoot))
			if _, ok := shouldReusePriorWorkdir(wrong, nil, root); ok {
				t.Fatal("accepted a workdir belonging to another conversation owner")
			}
		})
	}
}
