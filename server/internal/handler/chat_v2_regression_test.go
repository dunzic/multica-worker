package handler

import (
	"bytes"
	"context"
	"fmt"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func collaborationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	agentID := dbfx.Agent(t, "HR batch reviewer", handlerTestRuntimeID(t))
	conv := dbfx.Insert(t, "conversation", testutil.Cols{"workspace_id": testWorkspaceID, "kind": "group", "title": "HR résumé batch", "created_by": testUserID})
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "member", "principal_id": testUserID, "role": "admin"}, "conversation_id=$1 AND principal_type='member'", conv)
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "agent", "principal_id": agentID}, "conversation_id=$1 AND principal_type='agent'", conv)
	topic := dbfx.Insert(t, "topic", testutil.Cols{"conversation_id": conv, "created_by_type": "member", "created_by_id": testUserID})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM chat_message WHERE topic_id=$1", topic)
		testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE topic_id=$1", topic)
	})
	return conv, topic, agentID
}

func TestCollaborationBatchSendAndPinnedClaim(t *testing.T) {
	_, topic, agentID := collaborationFixture(t)
	ids := []string{}
	for i := 0; i < 3; i++ {
		id := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID, "filename": fmt.Sprintf("candidate-%d.pdf", i), "url": fmt.Sprintf("https://example.test/resume-%d.pdf", i), "content_type": "application/pdf", "size_bytes": 12})
		ids = append(ids, id)
	}
	content := fmt.Sprintf("[@HR](mention://agent/%s) Screen these files using the JD", agentID)
	req := testutil.WithURLParams(newRequest("POST", "/api/v2/topics/"+topic+"/messages", map[string]any{"content": content, "attachment_ids": ids}), "topicId", topic)
	var sent SendMessageResponse
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	if len(sent.TaskIDs) != 1 || sent.TaskID == nil {
		t.Fatalf("run ids = %#v", sent)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(*sent.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	if task.TriggerEvidenceRefID != parseUUID(sent.Message.ID) || task.InitiatorUserID != parseUUID(testUserID) {
		t.Fatal("run did not retain sender/message provenance")
	}
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM attachment WHERE chat_message_id=$1", sent.Message.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("bound %d of 3 résumés", count)
	}
	if _, err := testHandler.Queries.CreateMessageV2(context.Background(), db.CreateMessageV2Params{TopicID: parseUUID(topic), SenderType: strToText("member"), SenderID: parseUUID(testUserID), Content: "A later, unrelated request"}); err != nil {
		t.Fatal(err)
	}
	var claim AgentTaskResponse
	if err := testHandler.enrichTopicClaim(context.Background(), task, &claim); err != nil {
		t.Fatal(err)
	}
	if len(claim.ChatMessageAttachments) != 3 {
		t.Fatalf("claim attachments = %d", len(claim.ChatMessageAttachments))
	}
	if !strings.Contains(claim.ChatMessage, "Screen these files") || strings.Contains(claim.ChatMessage, "A later, unrelated request") {
		t.Fatalf("claim used wrong input: %q", claim.ChatMessage)
	}
}

func TestCollaborationBuildsCompleteDaemonClaim(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	var sent SendMessageResponse
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) Review pinned batch", agent)}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(*sent.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	claimReq := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "collaboration-claim")
	claimReq.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityGroupTopicsV1)
	resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(claimReq, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
	if failure != nil {
		t.Fatalf("full daemon claim failed: %+v", failure)
	}
	if resp.TopicID != topic || resp.WorkspaceID != testWorkspaceID || resp.ChatType != "group" || !strings.Contains(resp.ChatMessage, "Review pinned batch") {
		t.Fatalf("wrong full claim: topic=%q workspace=%q type=%q input=%q", resp.TopicID, resp.WorkspaceID, resp.ChatType, resp.ChatMessage)
	}
}

func TestCollaborationUploadScopeAndPrivateStorage(t *testing.T) {
	_, topic, _ := collaborationFixture(t)
	h := *testHandler
	upload := func() *http.Request {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "candidate.pdf")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("%PDF-test")); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteField("topic_id", topic); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/upload-file", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		return req
	}
	public := &mockStorage{}
	h.Storage = public
	testutil.Call(t, h.UploadFile, upload()).Want(http.StatusServiceUnavailable)
	if len(public.files) != 0 {
		t.Fatal("sensitive file uploaded to a public CDN")
	}
	h.Storage = &mockStorageNoCdn{}
	for i := 0; i < 3; i++ {
		var att AttachmentResponse
		testutil.Call(t, h.UploadFile, upload()).Want(http.StatusOK).JSON(&att)
		t.Cleanup(func() { testPool.Exec(context.Background(), "DELETE FROM attachment WHERE id=$1", att.ID) })
		var storedTopic string
		dbfx.QueryRow(t, "SELECT topic_id FROM attachment WHERE id=$1", att.ID).Scan(&storedTopic)
		if storedTopic != topic || !strings.Contains(att.URL, "/api/attachments/") || strings.Contains(att.URL, "cdn.example") {
			t.Fatalf("unprotected or mis-scoped upload: %+v", att)
		}
	}
}

func TestCollaborationOldDaemonCannotReceiveGroupTask(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	var sent SendMessageResponse
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) Review", agent)}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", *sent.TaskID)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(*sent.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	claimReq := newDaemonTokenRequest("POST", "/claim", nil, testWorkspaceID, "old-daemon")
	_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(claimReq, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
	if failure == nil || failure.status != http.StatusConflict || failure.outcome != "error_group_topics_unsupported" {
		t.Fatalf("old daemon accepted: %+v", failure)
	}
	stored, err := testHandler.Queries.GetAgentTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.Error.String, "Upgrade") {
		t.Fatalf("missing upgrade guidance: status=%s error=%q", stored.Status, stored.Error.String)
	}
}

func TestCollaborationConcurrentRemovalRejectsWaitingSend(t *testing.T) {
	conv, topic, agent := collaborationFixture(t)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT id FROM conversation WHERE id=$1 FOR UPDATE", conv); err != nil {
		t.Fatal(err)
	}
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) Review", agent)}), "topicId", topic)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { testHandler.SendTopicMessage(recorder, req); close(done) }()
	// Wait for the send to reach its transaction lock rather than guessing from elapsed time.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		err := testPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM conversation WHERE id=%FOR UPDATE%')").Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("send did not reach the conversation lock")
	}
	if _, err := tx.Exec(ctx, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_type='member' AND principal_id=$2", conv, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send remained blocked")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("removed sender response=%d %s", recorder.Code, recorder.Body.String())
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM chat_message WHERE topic_id=$1", topic).Scan(&count)
	if count != 0 {
		t.Fatalf("%d messages persisted after sender removal", count)
	}
}

func TestCollaborationSendRollsBackInvalidAttachment(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	req := testutil.WithURLParams(newRequest("POST", "/", map[string]any{"content": fmt.Sprintf("[@HR](mention://agent/%s) review", agent), "attachment_ids": []string{"00000000-0000-0000-0000-000000000001"}}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusBadRequest)
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM chat_message WHERE topic_id=$1", topic).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d messages persisted despite invalid attachment", count)
	}
}

func TestCollaborationMalformedUUIDsAndAtomicCreation(t *testing.T) {
	req := testutil.WithURLParams(newRequest("GET", "/", nil), "id", "not-a-uuid")
	testutil.Call(t, testHandler.GetConversation, req).Want(http.StatusBadRequest)
	var before, after int
	testPool.QueryRow(context.Background(), "SELECT count(*) FROM conversation WHERE workspace_id=$1", testWorkspaceID).Scan(&before)
	request := newRequest("POST", "/", CreateConversationRequest{Kind: "group", Title: "invalid group", Participants: []Principal{{Type: "member", ID: "invalid"}}})
	testutil.Call(t, testHandler.CreateConversation, request).Want(http.StatusBadRequest)
	testPool.QueryRow(context.Background(), "SELECT count(*) FROM conversation WHERE workspace_id=$1", testWorkspaceID).Scan(&after)
	if after != before {
		t.Fatal("invalid group left a partial conversation")
	}
}

func TestCollaborationDuplicateCreatorCannotLoseAdminRole(t *testing.T) {
	agent := dbfx.Agent(t, "Duplicate principal reviewer", handlerTestRuntimeID(t))
	var created ConversationWire
	req := newRequest("POST", "/", CreateConversationRequest{Kind: "group", Title: "Duplicate creator", Participants: []Principal{{Type: "member", ID: strings.ToUpper(testUserID)}, {Type: "agent", ID: agent}}})
	testutil.Call(t, testHandler.CreateConversation, req).Want(http.StatusCreated).JSON(&created)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM topic WHERE conversation_id=$1", created.ID)
		testPool.Exec(context.Background(), "DELETE FROM conversation_participant WHERE conversation_id=$1", created.ID)
		testPool.Exec(context.Background(), "DELETE FROM conversation WHERE id=$1", created.ID)
	})
	if len(created.Participants) != 2 {
		t.Fatalf("duplicate principal retained: %v", created.Participants)
	}
	for _, p := range created.Participants {
		if p.PrincipalType == "member" && p.PrincipalID == testUserID && p.Role != "admin" {
			t.Fatal("duplicate creator removed last admin")
		}
	}
}

func TestCollaborationAttachmentRequiresParticipation(t *testing.T) {
	_, topic, _ := collaborationFixture(t)
	outsider := dbfx.User(t, "Outside recruiter", "outsider-collaboration@example.test")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": outsider, "role": "member"})
	attID := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID, "filename": "resume.pdf", "url": "https://example.test/private-resume.pdf", "content_type": "application/pdf", "size_bytes": 1})
	req := testutil.WithURLParams(newRequest("GET", "/", nil), "id", attID)
	req.Header.Set("X-User-ID", outsider)
	testutil.Call(t, testHandler.GetAttachmentByID, req).Want(http.StatusNotFound)
	req.Header.Set("X-User-ID", testUserID)
	testutil.Call(t, testHandler.GetAttachmentByID, req).Want(http.StatusOK)
}

func TestCollaborationCompletionPersistsReplyExactlyOnce(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	req := testutil.WithURLParams(newRequest("POST", "/", map[string]any{"content": fmt.Sprintf("[@HR](mention://agent/%s) review", agent)}), "topicId", topic)
	var sent SendMessageResponse
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1", *sent.TaskID)
	result := []byte(`{"output":"Candidate reviewed; original file and follow-up links retained."}`)
	for i := 0; i < 2; i++ {
		if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(*sent.TaskID), result, "session", "work", "", false, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := testPool.QueryRow(context.Background(), "SELECT count(*) FROM chat_message WHERE topic_id=$1 AND task_id=$2", topic, *sent.TaskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("completion wrote %d replies", count)
	}
}

func TestCollaborationMultipleMentionedAgents(t *testing.T) {
	conv, topic, first := collaborationFixture(t)
	second := dbfx.Agent(t, "Second reviewer", handlerTestRuntimeID(t))
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "agent", "principal_id": second}, "conversation_id=$1 AND principal_id=$2", conv, second)
	content := fmt.Sprintf("[@First](mention://agent/%s) [@First](mention://agent/%s) [@Second](mention://agent/%s)", first, first, second)
	var result SendMessageResponse
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: content}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&result)
	if len(result.TaskIDs) != 2 {
		t.Fatalf("expected two independent runs, got %v", result.TaskIDs)
	}
}

func TestCollaborationAdminHandoffAndLeaveCancelsRuns(t *testing.T) {
	conv, topic, agent := collaborationFixture(t)
	leave := testutil.WithURLParams(newRequest("DELETE", "/", nil), "id", conv)
	testutil.Call(t, testHandler.LeaveConversation, leave).Want(http.StatusConflict)
	peer := dbfx.User(t, "Recruiter", "group-admin-handoff@example.test")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": peer, "role": "member"})
	add := testutil.WithURLParams(newRequest("POST", "/", AddParticipantRequest{Type: "member", ID: peer, Role: "admin"}), "id", conv)
	testutil.Call(t, testHandler.AddConversationParticipant, add).Want(http.StatusCreated)
	var sent SendMessageResponse
	send := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) review", agent)}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, send).Want(http.StatusCreated).JSON(&sent)
	testutil.Call(t, testHandler.LeaveConversation, leave).Want(http.StatusNoContent)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(*sent.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "cancelled" {
		t.Fatalf("leaver's run status = %s", task.Status)
	}
	get := testutil.WithURLParams(newRequest("GET", "/", nil), "id", conv)
	testutil.Call(t, testHandler.GetConversation, get).Want(http.StatusNotFound)
}

func TestCollaborationRunStatusIsParticipantScoped(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) review", agent)}), "topicId", topic)
	var sent SendMessageResponse
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	var runs []TopicRunWire
	get := testutil.WithURLParams(newRequest("GET", "/", nil), "topicId", topic)
	testutil.Call(t, testHandler.ListTopicRuns, get).Want(http.StatusOK).JSON(&runs)
	if len(runs) != 1 || runs[0].Status != "queued" || runs[0].MessageID != sent.Message.ID {
		t.Fatalf("unexpected run state %#v", runs)
	}
	outsider := dbfx.User(t, "Other", "group-run-outsider@example.test")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": outsider, "role": "member"})
	get.Header.Set("X-User-ID", outsider)
	testutil.Call(t, testHandler.ListTopicRuns, get).Want(http.StatusNotFound)
}

func TestCollaborationRecipientsExcludeDepartedWorkspaceMembers(t *testing.T) {
	conv, _, _ := collaborationFixture(t)
	peer := dbfx.User(t, "Former recruiter", "collaboration-former-recruiter@example.test")
	membership := dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": peer, "role": "member"})
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "member", "principal_id": peer}, "conversation_id=$1 AND principal_id=$2", conv, peer)
	before := testHandler.activeMemberRecipients(context.Background(), parseUUID(conv))
	if len(before) != 2 {
		t.Fatalf("recipients before workspace exit: %v", before)
	}
	dbfx.Exec(t, "DELETE FROM member WHERE id=$1", membership)
	after := testHandler.activeMemberRecipients(context.Background(), parseUUID(conv))
	if len(after) != 1 || after[0] != testUserID {
		t.Fatalf("departed member still receives group content: %v", after)
	}
}

func TestCollaborationWorkspaceTeardownPreservesOtherGroups(t *testing.T) {
	control, _, _ := collaborationFixture(t)
	ws := dbfx.Workspace(t, "Disposable group workspace", "collaboration-delete-workspace")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": ws, "user_id": testUserID, "role": "owner"})
	agent := dbfx.Agent(t, "Disposable reviewer", handlerTestRuntimeID(t), testutil.Cols{"workspace_id": ws})
	conv := dbfx.Insert(t, "conversation", testutil.Cols{"workspace_id": ws, "kind": "group", "created_by": testUserID})
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "member", "principal_id": testUserID, "role": "admin"}, "conversation_id=$1", conv)
	topic := dbfx.Insert(t, "topic", testutil.Cols{"conversation_id": conv, "created_by_type": "member", "created_by_id": testUserID})
	dbfx.Insert(t, "chat_message", testutil.Cols{"topic_id": topic, "sender_type": "member", "sender_id": testUserID, "role": "user", "content": "Disposable batch"})
	dbfx.Task(t, agent, testutil.Cols{"topic_id": topic, "runtime_id": handlerTestRuntimeID(t)})
	dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": ws, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID, "filename": "disposable.pdf", "url": "/uploads/disposable.pdf", "content_type": "application/pdf", "size_bytes": 1})
	req := testutil.WithURLParams(newRequest("DELETE", "/", nil), "id", ws)
	testutil.Call(t, testHandler.DeleteWorkspace, req).Want(http.StatusNoContent)
	for _, table := range []string{"conversation_participant", "topic"} {
		var count int
		dbfx.QueryRow(t, "SELECT count(*) FROM "+table+" WHERE conversation_id=$1", conv).Scan(&count)
		if count != 0 {
			t.Fatalf("%d rows remain in %s", count, table)
		}
	}
	for _, table := range []string{"chat_message", "agent_task_queue", "attachment"} {
		var count int
		dbfx.QueryRow(t, "SELECT count(*) FROM "+table+" WHERE topic_id=$1", topic).Scan(&count)
		if count != 0 {
			t.Fatalf("%d rows remain in %s", count, table)
		}
	}
	if _, err := testHandler.Queries.GetConversation(context.Background(), db.GetConversationParams{ID: parseUUID(control), WorkspaceID: parseUUID(testWorkspaceID)}); err != nil {
		t.Fatalf("other workspace group was removed: %v", err)
	}
}
