package handler

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTopicTranscriptRejectsNonparticipant(t *testing.T) {
	conv, topic, agent := collaborationFixture(t)
	dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", agent)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": agent, "target_type": "workspace", "target_id": testWorkspaceID})
	var sent SendMessageResponse
	req := testutil.WithURLParams(newRequest("POST", "/", SendMessageRequest{Content: fmt.Sprintf("[@HR](mention://agent/%s) review confidential batch", agent)}), "topicId", topic)
	testutil.Call(t, testHandler.SendTopicMessage, req).Want(http.StatusCreated).JSON(&sent)
	outsider := dbfx.User(t, "Group outsider", "group-outsider@example.test")
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": outsider, "role": "member"})
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(outsider)})
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "INSERT INTO task_message(task_id,seq,type,content) VALUES($1,1,'text','CONFIDENTIAL_RESUME_REVIEW')", *sent.TaskID)
	t.Cleanup(func() { testPool.Exec(context.Background(), "DELETE FROM task_message WHERE task_id=$1", *sent.TaskID) })
	asOutsider := func(path, key, value string) *http.Request {
		r := testutil.WithURLParams(newRequestAs(outsider, "GET", path, nil), key, value)
		return r.WithContext(middleware.SetMemberContext(r.Context(), testWorkspaceID, member))
	}
	testutil.Call(t, testHandler.ListTopicRuns, asOutsider("/api/v2/topics/"+topic+"/runs", "topicId", topic)).Want(http.StatusNotFound)
	history := httptest.NewRecorder()
	testHandler.ListAgentTasks(history, asOutsider("/api/agents/"+agent+"/tasks", "id", agent))
	if history.Code != http.StatusOK {
		t.Fatalf("agent history=%d %s", history.Code, history.Body.String())
	}
	if strings.Contains(history.Body.String(), *sent.TaskID) {
		t.Errorf("nonparticipant can discover private group task ID in public agent history")
	}
	snapshot := httptest.NewRecorder()
	testHandler.ListWorkspaceAgentTaskSnapshot(snapshot, asOutsider("/api/agent-tasks/snapshot", "", ""))
	if snapshot.Code != http.StatusOK || strings.Contains(snapshot.Body.String(), *sent.TaskID) {
		t.Errorf("nonparticipant snapshot leaked topic task or failed: status=%d", snapshot.Code)
	}
	visible := dbfx.Task(t, agent, testutil.Cols{"status": "completed", "runtime_id": handlerTestRuntimeID(t)})
	dbfx.Exec(t, "UPDATE agent_task_queue SET created_at=now()-interval '1 day', completed_at=now()-interval '1 day' WHERE id=$1", visible)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now(), result=jsonb_build_object('output','CONFIDENTIAL_RESUME_REVIEW') WHERE id=$1", *sent.TaskID)
	page := httptest.NewRecorder()
	testHandler.ListAgentTasks(page, asOutsider("/api/agents/"+agent+"/tasks?limit=1", "id", agent))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), visible) || strings.Contains(page.Body.String(), *sent.TaskID) {
		t.Fatal("private rows must be filtered before pagination, not leave an empty page")
	}
	snapshot = httptest.NewRecorder()
	testHandler.ListWorkspaceAgentTaskSnapshot(snapshot, asOutsider("/api/agent-tasks/snapshot", "", ""))
	if snapshot.Code != http.StatusOK || !strings.Contains(snapshot.Body.String(), visible) || strings.Contains(snapshot.Body.String(), "CONFIDENTIAL_RESUME_REVIEW") {
		t.Fatal("latest outcome must select the most recent visible run")
	}
	transcript := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(transcript, asOutsider("/api/tasks/"+*sent.TaskID+"/messages", "taskId", *sent.TaskID))
	if transcript.Code != http.StatusNotFound {
		t.Errorf("nonparticipant transcript response=%d exposes confidential content=%v", transcript.Code, strings.Contains(transcript.Body.String(), "CONFIDENTIAL_RESUME_REVIEW"))
	}
	testutil.Call(t, testHandler.ListTaskMessages, asOutsider("/", "taskId", *sent.TaskID)).Want(http.StatusNotFound)
	participantReq := func() *http.Request {
		r := testutil.WithURLParams(newRequest("GET", "/", nil), "taskId", *sent.TaskID)
		owner, err := testHandler.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID)})
		if err != nil {
			t.Fatal(err)
		}
		return r.WithContext(middleware.SetMemberContext(r.Context(), testWorkspaceID, owner))
	}
	// The owner can read the run; losing group participation revokes both
	// regular and PAT-fallback daemon routes without waiting for cache expiry.
	allowed := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(allowed, participantReq())
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), "CONFIDENTIAL_RESUME_REVIEW") {
		t.Fatal("participant could not read their task transcript")
	}
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conv, testUserID)
	testutil.Call(t, testHandler.ListTaskMessagesByUser, participantReq()).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.ListTaskMessages, participantReq()).Want(http.StatusNotFound)
}

func TestLocalGroupFilesRejectAlternatePaths(t *testing.T) {
	_, topic, _ := collaborationFixture(t)
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("cannot initialize test storage")
	}
	key := "workspaces/" + testWorkspaceID + "/review-private.pdf"
	body := "%PDF-CONFIDENTIAL_RESUME"
	link, err := local.Upload(context.Background(), key, []byte(body), "application/pdf", "resume.pdf")
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID, "filename": "resume.pdf", "url": link, "content_type": "application/pdf", "size_bytes": len(body)})
	h := *testHandler
	h.Storage = local
	router := chi.NewRouter()
	router.Get("/uploads/*", h.ServeLocalUpload)
	for _, url := range []string{
		link,
		strings.ToUpper(link),
		"/uploads/workspaces/" + strings.ToUpper(testWorkspaceID) + "/REVIEW-PRIVATE.PDF",
		"/uploads/workspaces/" + testWorkspaceID + "//review-private.pdf",
		"/uploads//workspaces/" + testWorkspaceID + "/review-private.pdf",
		"/uploads/workspaces/" + testWorkspaceID + "/./review-private.pdf",
		"/uploads/workspaces/" + testWorkspaceID + "/child/../review-private.pdf",
		"/uploads/workspaces/" + testWorkspaceID + "/%2freview-private.pdf",
		"/uploads/workspaces/" + testWorkspaceID + "/%5creview-private.pdf",
	} {
		t.Run(url, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("anonymous private object response=%d exposes bytes=%v", rec.Code, rec.Body.String() == body)
			}
		})
	}
	// Changing the public base URL must not make existing private bytes public.
	dbfx.Exec(t, "UPDATE attachment SET url=$1 WHERE url=$2", "https://previous-origin.example"+link, link)
	if _, err := h.Queries.GetTopicAttachmentByStorageURL(context.Background(), strings.ToUpper(link)); err != nil {
		t.Fatalf("case and origin aliases must resolve the protected attachment: %v", err)
	}
	privateRec := httptest.NewRecorder()
	router.ServeHTTP(privateRec, httptest.NewRequest("GET", link, nil))
	if privateRec.Code != http.StatusNotFound {
		t.Fatal("old public origin must not bypass the topic ACL")
	}
	publicURL, err := local.Upload(context.Background(), "public.pdf", []byte("public"), "application/pdf", "public.pdf")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", publicURL, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "public" {
		t.Fatal("ordinary non-topic uploads must remain available")
	}
}
