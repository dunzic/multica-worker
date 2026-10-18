package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestConversationSummaryIsBoundedAndMembershipScoped(t *testing.T) {
	conv, topic, _ := collaborationFixture(t)
	content := strings.Repeat("质量交付", 100)
	dbfx.Insert(t, "chat_message", testutil.Cols{"topic_id": topic, "sender_type": "member", "sender_id": testUserID, "role": "user", "content": content})
	var conversations []ConversationWire
	testutil.Call(t, testHandler.ListConversations, newRequest("GET", "/api/v2/conversations", nil)).Want(http.StatusOK).JSON(&conversations)
	found := false
	for _, c := range conversations {
		if c.ID == conv {
			found = true
			if c.LastMessage == nil || len([]rune(c.LastMessage.Content)) != 240 || c.LastMessage.CreatedAt == "" {
				t.Fatalf("missing or unbounded latest summary: %+v", c.LastMessage)
			}
		}
	}
	if !found {
		t.Fatal("member cannot find their group")
	}
	if _, err := testPool.Exec(context.Background(), "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_type='member' AND principal_id=$2", conv, testUserID); err != nil {
		t.Fatal(err)
	}
	testutil.Call(t, testHandler.ListConversations, newRequest("GET", "/api/v2/conversations", nil)).Want(http.StatusOK).JSON(&conversations)
	for _, c := range conversations {
		if c.ID == conv {
			t.Fatal("removed member received group or message summary")
		}
	}
}
