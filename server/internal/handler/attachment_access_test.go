package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAttachmentAccess_GroupRemovalRevokesEveryEntryPoint(t *testing.T) {
	store := installProxyModeStorage(t)
	conv, topic, _ := collaborationFixture(t)
	store.files = map[string][]byte{"private.txt": []byte("private")}
	id := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "topic_id": topic,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "private.txt", "url": "https://cdn.example.com/private.txt",
		"content_type": "text/plain", "size_bytes": 7,
	})
	request := func() *http.Request {
		return testutil.WithURLParams(newRequest("GET", "/api/attachments/"+id, nil), "id", id)
	}
	var metadata AttachmentResponse
	testutil.Call(t, testHandler.GetAttachmentByID, request()).Want(http.StatusOK).JSON(&metadata)
	for _, handler := range []http.HandlerFunc{testHandler.DownloadAttachment, testHandler.GetAttachmentContent} {
		testutil.Call(t, handler, request()).Want(http.StatusOK)
	}
	for _, path := range []string{metadata.DownloadURL, metadata.AttachmentDownloadURL} {
		req := testutil.WithURLParams(httptest.NewRequest("GET", path, nil), "id", id)
		testutil.Call(t, testHandler.DownloadAttachmentWithCapability, req).Want(http.StatusOK)
	}
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_type='member' AND principal_id=$2", conv, testUserID)
	if _, err := testHandler.getWorkspaceMember(context.Background(), testUserID, testWorkspaceID); err != nil {
		t.Fatal("fixture must remain a workspace member", err)
	}
	before, _ := store.streamCopyCalls()
	for _, handler := range []http.HandlerFunc{
		testHandler.GetAttachmentByID, testHandler.GetAttachmentForDownload, testHandler.ResolveAttachmentPreview,
		testHandler.DownloadAttachment, testHandler.GetAttachmentContent, testHandler.GetAttachmentOfficePreview,
	} {
		testutil.Call(t, handler, request()).Want(http.StatusNotFound)
	}
	for _, path := range []string{metadata.DownloadURL, metadata.AttachmentDownloadURL} {
		req := testutil.WithURLParams(httptest.NewRequest("GET", path, nil), "id", id)
		testutil.WithHeaders(req, "X-User-ID", testUserID, "X-Agent-ID", "spoofed", "X-Actor-Source", "task_token")
		testutil.Call(t, testHandler.DownloadAttachmentWithCapability, req).Want(http.StatusNotFound)
	}
	if after, _ := store.streamCopyCalls(); before != after {
		t.Fatal("revoked group access opened storage")
	}
}

func TestAttachmentAccess_HistoricalChatBindings(t *testing.T) {
	for _, mode := range []string{"v1_session", "v2_message_only", "missing_v2_topic"} {
		t.Run(mode, func(t *testing.T) {
			store := installProxyModeStorage(t)
			conversation, topic, agent := collaborationFixture(t)
			store.files = map[string][]byte{"history.txt": []byte("history")}
			messageCols := testutil.Cols{"role": "user", "content": "historical message"}
			if mode == "v1_session" {
				messageCols["chat_session_id"] = dbfx.ChatSession(t, agent)
			} else {
				messageCols["topic_id"] = topic
				messageCols["sender_type"] = "member"
				messageCols["sender_id"] = testUserID
			}
			message := dbfx.Insert(t, "chat_message", messageCols)
			cols := testutil.Cols{
				"workspace_id": testWorkspaceID, "chat_message_id": message,
				"uploader_type": "member", "uploader_id": testUserID,
				"filename": "history.txt", "url": "https://cdn.example.com/history.txt",
				"content_type": "text/plain", "size_bytes": 7,
			}
			if mode == "missing_v2_topic" {
				cols["topic_id"] = topic
			}
			id := dbfx.Insert(t, "attachment", cols)
			req := func() *http.Request {
				return testutil.WithURLParams(newRequest("GET", "/api/attachments/"+id+"/download", nil), "id", id)
			}
			testutil.Call(t, testHandler.DownloadAttachment, req()).Want(http.StatusOK)
			if mode == "missing_v2_topic" {
				dbfx.Exec(t, "DELETE FROM topic WHERE id=$1", topic)
			} else {
				dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conversation, testUserID)
			}
			want := http.StatusNotFound
			if mode == "v1_session" {
				// Legacy single-chat attachments retain their original workspace
				// visibility; removing an unrelated group must not break them.
				want = http.StatusOK
			}
			testutil.Call(t, testHandler.DownloadAttachment, req()).Want(want)
		})
	}
}

func TestAttachmentAccess_IssueCommentAndReplyLinksIgnoreStaleMembershipCache(t *testing.T) {
	store := installProxyModeStorage(t)
	store.files = map[string][]byte{"report.txt": []byte("report")}
	userID := dbfx.User(t, "Former collaborator", "former-collaborator@example.test")
	member := dbfx.Member(t, testWorkspaceID, userID, "member")
	issue := dbfx.Issue(t, "Embedded attachment revocation")
	comment := dbfx.Comment(t, issue, "Attachment in comment")
	reply := dbfx.Comment(t, issue, "Attachment in reply", testutil.Cols{"parent_id": comment})
	ids := make([]string, 0, 3)
	for _, commentID := range []string{"", comment, reply} {
		cols := testutil.Cols{
			"workspace_id": testWorkspaceID, "issue_id": issue,
			"uploader_type": "member", "uploader_id": testUserID,
			"filename": "report.txt", "url": "https://cdn.example.com/report.txt",
			"content_type": "text/plain", "size_bytes": 6,
		}
		if commentID != "" {
			cols["comment_id"] = commentID
		}
		id := dbfx.Insert(t, "attachment", cols)
		ids = append(ids, id)
		link := "/api/attachments/" + id + "/download"
		if commentID == "" {
			dbfx.Exec(t, "UPDATE issue SET description=$1 WHERE id=$2", "[report]("+link+")", issue)
		} else {
			dbfx.Exec(t, "UPDATE comment SET content=$1 WHERE id=$2", "[report]("+link+")", commentID)
		}
		req := testutil.WithURLParams(newRequestAs(userID, "GET", link, nil), "id", id)
		testutil.Call(t, testHandler.DownloadAttachment, req).Want(http.StatusOK)
	}
	cache := testHandler.MembershipCache
	testHandler.MembershipCache = auth.NewMembershipCache(positiveMembershipRedis{})
	t.Cleanup(func() { testHandler.MembershipCache = cache })
	dbfx.Exec(t, "DELETE FROM member WHERE id=$1", member)
	for _, id := range ids {
		for _, handler := range []http.HandlerFunc{testHandler.DownloadAttachment, testHandler.GetAttachmentByID, testHandler.GetAttachmentForDownload, testHandler.ResolveAttachmentPreview, testHandler.GetAttachmentContent, testHandler.GetAttachmentOfficePreview} {
			req := testutil.WithURLParams(newRequestAs(userID, "GET", "/api/attachments/"+id+"/download", nil), "id", id)
			req.Header.Set("Range", "bytes=0-2")
			testutil.Call(t, handler, req).Want(http.StatusNotFound)
		}
		// The remaining member still reads the same stable link.
		req := testutil.WithURLParams(newRequest("GET", "/api/attachments/"+id+"/download", nil), "id", id)
		testutil.Call(t, testHandler.DownloadAttachment, req).Want(http.StatusOK)
	}
}

func TestAttachmentAccess_TaskCredentialBinding(t *testing.T) {
	for _, revoke := range []string{"token_deleted", "token_expired", "group_agent_removed", "workspace_mismatch"} {
		t.Run(revoke, func(t *testing.T) {
			store := installProxyModeStorage(t)
			conv, topic, agent := collaborationFixture(t)
			task := dbfx.Task(t, agent, testutil.Cols{"topic_id": topic, "runtime_id": handlerTestRuntimeID(t)})
			secret := "mat_attachment_test_" + task
			tokenID := dbfx.Insert(t, "task_token", testutil.Cols{
				"token_hash": auth.HashToken(secret), "task_id": task, "agent_id": agent,
				"workspace_id": testWorkspaceID, "user_id": testUserID, "expires_at": time.Now().Add(time.Hour),
			})
			id := dbfx.Insert(t, "attachment", testutil.Cols{
				"workspace_id": testWorkspaceID, "topic_id": topic,
				"uploader_type": "member", "uploader_id": testUserID,
				"filename": "agent.txt", "url": "https://cdn.example.com/agent.txt",
				"content_type": "text/plain", "size_bytes": 5,
			})
			store.files = map[string][]byte{"agent.txt": []byte("agent")}
			router := chi.NewRouter()
			authenticate := middleware.Auth(testHandler.Queries, nil, nil, nil)
			router.With(authenticate).Get("/api/attachments/{id}/download-metadata", testHandler.GetAttachmentForDownload)
			router.With(authenticate).Get("/api/attachments/{id}/download", testHandler.DownloadAttachment)
			router.Get("/api/attachments/{id}/signed-download", testHandler.DownloadAttachmentWithCapability)
			request := func(suffix string) *http.Request {
				return testutil.WithHeaders(httptest.NewRequest("GET", "/api/attachments/"+id+suffix, nil), "Authorization", "Bearer "+secret, "X-Agent-ID", testUserID)
			}
			var metadata AttachmentResponse
			testutil.Call(t, router.ServeHTTP, request("/download-metadata")).Want(http.StatusOK).JSON(&metadata)
			for _, path := range []string{metadata.DownloadURL, metadata.AttachmentDownloadURL} {
				parsed, err := url.Parse(path)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Query().Get("tid") != tokenID || strings.Contains(path, secret) {
					t.Fatalf("must bind token identity without disclosing its secret: %s", path)
				}
				testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", path, nil)).Want(http.StatusOK)
				q := parsed.Query()
				q.Del("tid")
				testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", parsed.Path+"?"+q.Encode(), nil)).Want(http.StatusForbidden)
			}
			testutil.Call(t, router.ServeHTTP, request("/download")).Want(http.StatusOK)
			wantAuth := http.StatusNotFound
			switch revoke {
			case "token_deleted":
				dbfx.Exec(t, "DELETE FROM task_token WHERE id=$1", tokenID)
				wantAuth = http.StatusUnauthorized
			case "token_expired":
				dbfx.Exec(t, "UPDATE task_token SET expires_at=now()-interval '1 second' WHERE id=$1", tokenID)
				wantAuth = http.StatusUnauthorized
			case "group_agent_removed":
				dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_type='agent' AND principal_id=$2", conv, agent)
			case "workspace_mismatch":
				other := dbfx.Workspace(t, "Other credential workspace", "other-credential-workspace")
				dbfx.Exec(t, "UPDATE task_token SET workspace_id=$1 WHERE id=$2", other, tokenID)
			}
			for _, path := range []string{metadata.DownloadURL, metadata.AttachmentDownloadURL} {
				testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", path, nil)).Want(http.StatusNotFound)
			}
			testutil.Call(t, router.ServeHTTP, request("/download-metadata")).Want(wantAuth)
			testutil.Call(t, router.ServeHTTP, request("/download")).Want(wantAuth)
			// Token ownership cannot inherit the human participant's group access.
			testutil.Call(t, testHandler.DownloadAttachment, testutil.WithURLParams(newRequest("GET", "/", nil), "id", id)).Want(http.StatusOK)
		})
	}
}

func TestAttachmentAccess_LocalIssueAttachmentHasNoPublicBypass(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	local := storage.NewLocalStorageFromEnv()
	link, err := local.Upload(context.Background(), "workspaces/"+testWorkspaceID+"/issue-private.txt", []byte("private"), "text/plain", "issue-private.txt")
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": dbfx.Issue(t, "Local attachment"),
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "issue-private.txt", "url": link, "content_type": "text/plain", "size_bytes": 7,
	})
	h := *testHandler
	h.Storage = local
	router := chi.NewRouter()
	router.Get("/uploads/*", h.ServeLocalUpload)
	for _, path := range []string{link, strings.ToUpper(link), strings.Replace(link, "/issue-private", "/./issue-private", 1)} {
		testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", path, nil)).Want(http.StatusNotFound)
	}
	testutil.Call(t, h.DownloadAttachment, testutil.WithURLParams(newRequest("GET", "/api/attachments/"+id+"/download", nil), "id", id)).Want(http.StatusOK)
	// A failed storage deletion must not publish a previously private object.
	dbfx.Exec(t, "DELETE FROM attachment WHERE id=$1", id)
	testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", link, nil)).Want(http.StatusNotFound)
}

func TestAttachmentAccess_PublicAvatarCannotBecomeTopicFileBypass(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	local := storage.NewLocalStorageFromEnv()
	id := uuid.NewString()
	key := "workspaces/" + testWorkspaceID + "/" + id + ".png"
	link, err := local.Upload(context.Background(), key, []byte("image"), "image/png", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "attachment", testutil.Cols{
		"id": id, "workspace_id": testWorkspaceID, "uploader_type": "member", "uploader_id": testUserID,
		"filename": "avatar.png", "url": link, "content_type": "image/png", "size_bytes": 5,
	})
	h := *testHandler
	h.Storage = local
	router := chi.NewRouter()
	router.Get("/uploads/*", h.ServeLocalUpload)
	router.Get("/api/avatars/{sig}/*", h.ServeAvatar)
	testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", link, nil)).Want(http.StatusOK)
	att := loadTestAttachment(t, id)
	if got := h.attachmentToResponse(att, attachmentURLModeSigned).URL; got != link {
		t.Fatal("standalone avatar setter lost its durable object identity")
	}
	_, topic, _ := collaborationFixture(t)
	dbfx.Exec(t, "UPDATE attachment SET topic_id=$1 WHERE id=$2", topic, id)
	att = loadTestAttachment(t, id)
	if got := h.attachmentToResponse(att, attachmentURLModeSigned).URL; got == link {
		t.Fatal("topic draft must not publish a storage URL")
	}
	for _, path := range []string{link, strings.ToUpper(link), avatarURLPath(key)} {
		testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", path, nil)).Want(http.StatusNotFound)
	}
	// Source-context snapshots are immutable private files, not public avatars.
	dbfx.Exec(t, "UPDATE attachment SET topic_id=NULL, source_context_id=$1 WHERE id=$2", uuid.NewString(), id)
	testutil.Call(t, router.ServeHTTP, httptest.NewRequest("GET", link, nil)).Want(http.StatusNotFound)
}
