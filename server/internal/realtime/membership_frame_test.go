package realtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestMembershipFrameSourceAuthority(t *testing.T) {
	socket := MembershipIdentity{WorkspaceID: "open", UserID: "reader"}
	for _, tc := range []struct {
		name, frame, workspace, conversation string
		valid                                bool
	}{
		{"group cross workspace", `{"type":"message:sent","payload":{"workspace_id":"source","conversation_id":"private"}}`, "source", "private", true},
		{"group same workspace", `{"type":"message:done","payload":{"workspace_id":"open","conversation_id":"private"}}`, "open", "private", true},
		{"missing workspace", `{"type":"message:sent","payload":{"conversation_id":"private"}}`, "", "", false},
		{"missing conversation", `{"type":"message:done","payload":{"workspace_id":"source"}}`, "", "", false},
		{"malformed group payload", `{"type":"topic:updated","payload":[]}`, "", "", false},
		{"conflicting source", `{"type":"message:sent","workspace_id":"open","payload":{"workspace_id":"source","conversation_id":"private"}}`, "", "", false},
		{"own removal signal", `{"type":"participant:removed","payload":{"workspace_id":"source","conversation_id":"private","principal_type":"member","principal_id":"reader"}}`, "source", "", true},
		{"other removal", `{"type":"participant:removed","payload":{"workspace_id":"source","conversation_id":"private","principal_type":"member","principal_id":"other"}}`, "source", "private", true},
		{"inbox legacy relay", `{"type":"inbox:new","payload":{"item":{"workspace_id":"source","body":"private"}}}`, "source", "", true},
		{"inbox no source", `{"type":"inbox:new","payload":{"item":{"body":"private"}}}`, "", "", false},
		{"session title", `{"type":"chat:session_updated","workspace_id":"source","payload":{"title":"private"}}`, "source", "", true},
		{"legacy title without source", `{"type":"chat:session_updated","payload":{"title":"private"}}`, "", "", false},
		{"legacy private reply without source", `{"type":"chat:done","payload":{"content":"private"}}`, "", "", false},
		{"private reply source", `{"type":"chat:done","workspace_id":"source","payload":{"content":"private"}}`, "source", "", true},
		{"new personal frame", `{"type":"task:updated","workspace_id":"source","payload":{}}`, "source", "", true},
		{"invitation nonmember", `{"type":"invitation:created","workspace_id":"not-joined","payload":{}}`, "open", "", true},
		{"connection control", `{"type":"pong"}`, "open", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := frameMembership([]byte(tc.frame), socket)
			if valid != tc.valid || got.WorkspaceID != tc.workspace || got.ConversationID != tc.conversation {
				t.Fatalf("got %+v valid=%v", got, valid)
			}
		})
	}
}

func TestMembershipFrameTopicAuthority(t *testing.T) {
	socket := MembershipIdentity{WorkspaceID: "open", UserID: "reader"}
	for _, typ := range []string{
		protocol.EventTaskQueued, protocol.EventTaskDispatch, protocol.EventTaskRunning,
		protocol.EventTaskWaitingLocalDirectory, protocol.EventTaskProgress, protocol.EventTaskCompleted,
		protocol.EventTaskFailed, protocol.EventTaskMessage, protocol.EventTaskCancelled,
		"future:topic_content",
	} {
		t.Run(typ, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"type": typ, "workspace_id": "source", "payload": map[string]string{
				"topic_id": "private-topic", "prompt": "private context",
			}})
			got, valid := frameMembership(raw, socket)
			want := MembershipIdentity{WorkspaceID: "source", UserID: "reader", TopicID: "private-topic"}
			if !valid || got != want {
				t.Fatalf("topic authority lost: %+v, valid=%v", got, valid)
			}
		})
	}
	for _, raw := range []string{
		`{"type":"task:dispatch","payload":{"topic_id":"private-topic"}}`,
		`{"type":"task:dispatch","workspace_id":"open","payload":{"workspace_id":"source","topic_id":"private-topic"}}`,
		`{"type":"task:dispatch","workspace_id":"source","payload":{"topic_id":123}}`,
		`{"type":"task:dispatch","workspace_id":"source","payload":[]}`,
	} {
		if got, valid := frameMembership([]byte(raw), socket); valid {
			t.Fatalf("invalid topic frame authorized: %+v from %s", got, raw)
		}
	}
	got, valid := frameMembership([]byte(`{"type":"task:dispatch","workspace_id":"source","payload":{"conversation_id":"group","topic_id":"topic"}}`), socket)
	if !valid || got.ConversationID != "group" || got.TopicID != "topic" {
		t.Fatalf("paired group and topic authority lost: %+v, valid=%v", got, valid)
	}
}

func TestMembershipCleanupCannotCarryContent(t *testing.T) {
	raw := []byte(`{"type":"participant:removed","event_id":"same-event","content":"secret","payload":{"workspace_id":"source","conversation_id":"group","principal_type":"member","principal_id":"reader","content":"secret","attachments":["secret"]}}`)
	clean := membershipCleanupFrame(raw, "reader")
	if strings.Contains(string(clean), "secret") || !strings.Contains(string(clean), "same-event") {
		t.Fatalf("unsafe or unidentifiable cleanup signal: %s", clean)
	}
	if !strings.Contains(string(raw), "secret") {
		t.Fatal("mutated shared frame")
	}
}
