package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type topicWakeupCheck func(runtimeID, taskID string)

func (f topicWakeupCheck) NotifyTaskAvailable(runtimeID, taskID string) { f(runtimeID, taskID) }

func TestTopicSendWakesDaemonAfterCommit(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	previous := testHandler.TaskService.Wakeup
	t.Cleanup(func() { testHandler.TaskService.Wakeup = previous })
	var notified []string
	testHandler.TaskService.Wakeup = topicWakeupCheck(func(runtimeID, taskID string) {
		// Read through the independent pool: an uncommitted insert is invisible.
		task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(taskID))
		if err != nil || uuidToString(task.TopicID) != topic || uuidToString(task.RuntimeID) != runtimeID {
			t.Fatalf("wakeup before topic task committed: %v", err)
		}
		notified = append(notified, taskID)
	})
	send := func(attachments []string) *http.Request {
		return testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{
			Content: fmt.Sprintf("[@HR](mention://agent/%s) review the batch", agent), AttachmentIDs: attachments,
		}), "topicId", topic)
	}
	// Invalid attachment binding rolls back the message; no daemon hint escapes.
	testutil.Call(t, testHandler.SendTopicMessage, send([]string{"11111111-1111-4111-8111-111111111111"})).Want(http.StatusBadRequest)
	if len(notified) != 0 {
		t.Fatal("rolled-back send woke a daemon")
	}
	var result SendMessageResponse
	testutil.Call(t, testHandler.SendTopicMessage, send(nil)).Want(http.StatusCreated).JSON(&result)
	if len(notified) != 1 || len(result.TaskIDs) != 1 || notified[0] != result.TaskIDs[0] {
		t.Fatalf("committed task was not woken exactly once: notifications=%v tasks=%v", notified, result.TaskIDs)
	}
}
