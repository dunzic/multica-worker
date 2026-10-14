package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTopicConversationIdentity(t *testing.T) {
	topic := TaskContextForEnv{AgentID: "agent-1", TopicID: "conversation-1"}
	dm := TaskContextForEnv{AgentID: "agent-1", ChatSessionID: "conversation-1"}
	issue := TaskContextForEnv{AgentID: "agent-1", IssueID: "conversation-1"}
	key := codexSessionStoreKey("test", topic)
	if key == "" || key == codexSessionStoreKey("test", dm) || key == codexSessionStoreKey("test", issue) {
		t.Fatal("Codex topic store is missing or overlaps another conversation kind")
	}
	other := topic
	other.TopicID = "conversation-2"
	if key == codexSessionStoreKey("test", other) || key == codexSessionStoreKey("other-profile", topic) {
		t.Fatal("Codex topic store ignores the conversation or profile")
	}
	other = topic
	other.AgentID = "other-agent"
	if key == codexSessionStoreKey("test", other) {
		t.Fatal("Codex topic store ignores the agent")
	}
	if hermesConversationSegment(topic) != "topic_conversation-1" ||
		hermesConversationSegment(topic) == hermesConversationSegment(dm) ||
		hermesConversationSegment(topic) == hermesConversationSegment(issue) {
		t.Fatal("Hermes topic store is missing or overlaps another conversation kind")
	}
	branch, owner := localWorktreeConversation(PrepareParams{Task: topic})
	_, dmOwner := localWorktreeConversation(PrepareParams{Task: dm})
	if branch == "" || owner != "topic:conversation-1" || owner == dmOwner {
		t.Fatal("local branch does not retain topic ownership")
	}
}

func TestPrepareTopicWritesOwnershipProofs(t *testing.T) {
	env, err := Prepare(PrepareParams{
		WorkspacesRoot: t.TempDir(), WorkspaceID: "ws-topic",
		TaskID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", AgentName: "HR",
		Task: TaskContextForEnv{AgentID: "agent-1", TopicID: "topic-1"},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)
	prov, err := ReadManagedEnvProvenance(env.RootDir)
	if err != nil || prov.TopicID != "topic-1" || prov.WorkspaceID != "ws-topic" || prov.AgentID != "agent-1" || prov.IssueID != "" || prov.ChatSessionID != "" {
		t.Fatalf("invalid topic provenance: %+v, %v", prov, err)
	}
	data, err := os.ReadFile(filepath.Join(env.WorkDir, TaskContextMarkerRelPath))
	if err != nil {
		t.Fatal(err)
	}
	var marker taskContextMarkerFile
	if err := json.Unmarshal(data, &marker); err != nil || marker.TopicID != "topic-1" || marker.AgentID != "agent-1" || marker.IssueID != "" || marker.ChatSessionID != "" {
		t.Fatalf("invalid topic marker: %+v, %v", marker, err)
	}
}
