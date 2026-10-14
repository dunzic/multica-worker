package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func previewLocationRequest(id string) *http.Request {
	return testutil.WithURLParams(newRequest("GET", "/api/attachments/"+id+"/preview-location?workspace_slug=wrong", nil), "id", id)
}

func TestResolveAttachmentPreviewCrossWorkspace(t *testing.T) {
	ws := dbfx.Workspace(t, "Preview owner", "preview-location-owner")
	member := dbfx.Member(t, ws, testUserID, "member")
	att := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": ws, "uploader_type": "member", "uploader_id": testUserID,
		"filename": "private.docx", "url": "https://storage.invalid/private",
		"content_type": "application/octet-stream", "size_bytes": 10,
	})
	// The header and query both name the wrong workspace. Ownership comes only
	// from the stored attachment after checking membership in its workspace.
	var location map[string]string
	response := testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest(att)).Want(http.StatusOK).JSON(&location)
	if location["workspace_id"] != ws || location["workspace_slug"] != "preview-location-owner" || len(location) != 2 {
		t.Fatalf("unexpected location or leaked file metadata: %v", location)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview location must not be cached")
	}
	dbfx.Exec(t, "DELETE FROM member WHERE id=$1", member)
	testHandler.MembershipCache.Invalidate(t.Context(), testUserID, ws)
	testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest(att)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest("01a11279-2c56-7781-8cf6-166f9315aae7")).Want(http.StatusNotFound)
}

func TestResolveAttachmentPreviewTopicPermission(t *testing.T) {
	conv, topic, _ := collaborationFixture(t)
	att := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID,
		"filename": "private.docx", "url": "https://storage.invalid/private",
		"content_type": "application/octet-stream", "size_bytes": 10,
	})
	testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest(att)).Want(http.StatusOK)
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conv, testUserID)
	testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest(att)).Want(http.StatusNotFound)
}

func TestResolveAttachmentPreviewRejectsMachineActorsAndMalformedID(t *testing.T) {
	for _, actor := range []string{"task_token", "cloud_pat"} {
		t.Run(actor, func(t *testing.T) {
			req := previewLocationRequest("01a11279-2c56-7781-8cf6-166f9315aae7")
			req.Header.Set("X-Actor-Source", actor)
			testutil.Call(t, testHandler.ResolveAttachmentPreview, req).Want(http.StatusForbidden)
		})
	}
	testutil.Call(t, testHandler.ResolveAttachmentPreview, previewLocationRequest("not-a-uuid")).Want(http.StatusBadRequest)
}
