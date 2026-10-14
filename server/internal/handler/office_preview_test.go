package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/officepreview"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestOfficePreviewAttachmentPermissions(t *testing.T) {
	conv, topic, _ := collaborationFixture(t)
	source := docxFixture(t, "word/document.xml", `<document>Test preview</document>`)
	store := &mockStorage{files: map[string][]byte{"office.docx": source}}
	conversions := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conversions++
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-private-preview"))
	}))
	defer upstream.Close()
	h := *testHandler
	h.Storage = store
	h.OfficePreview, _ = officepreview.New(upstream.URL)
	att := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "topic_id": topic, "uploader_type": "member", "uploader_id": testUserID, "filename": "office.docx", "url": "https://cdn.example.com/office.docx", "content_type": "application/octet-stream", "size_bytes": len(source)})
	request := func() *http.Request {
		return testutil.WithURLParams(newRequest("GET", "/api/attachments/"+att+"/office-preview", nil), "id", att)
	}
	r := testutil.Call(t, h.GetAttachmentOfficePreview, request()).Want(http.StatusOK)
	if r.Header().Get("Content-Type") != "application/pdf" || r.Header().Get("Cache-Control") != "no-store" || r.Body.String() != "%PDF-private-preview" {
		t.Fatal("unsafe or incorrect preview response")
	}
	if conversions != 1 {
		t.Fatal("conversion not called")
	}
	// Revoking participation must block re-opening even if conversion succeeded
	// moments ago; there is no public derived attachment or signed preview URL.
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conv, testUserID)
	testutil.Call(t, h.GetAttachmentOfficePreview, request()).Want(http.StatusNotFound)
	if conversions != 1 || store.getReaderCalls != 1 {
		t.Fatal("denied attachment reached storage/converter")
	}
}

func TestOfficePreviewFailureStates(t *testing.T) {
	source := docxFixture(t, "word/document.xml", `<document>Test</document>`)
	att := dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, "uploader_type": "member", "uploader_id": testUserID, "filename": "office.docx", "url": "https://cdn.example.com/office.docx", "content_type": "application/octet-stream", "size_bytes": len(source)})
	h := *testHandler
	h.Storage = &mockStorage{files: map[string][]byte{"office.docx": source}}
	request := func(id string) *http.Request { return testutil.WithURLParams(newRequest("GET", "/", nil), "id", id) }
	var disabled map[string]any
	testutil.Call(t, h.GetAttachmentOfficePreview, request(att)).Want(http.StatusForbidden).JSON(&disabled)
	if disabled["code"] != "office_preview_not_configured" {
		t.Fatalf("missing disabled reason: %v", disabled)
	}
	testutil.Call(t, h.GetAttachmentOfficePreview, request("not-a-uuid")).Want(http.StatusBadRequest)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer upstream.Close()
	h.OfficePreview, _ = officepreview.New(upstream.URL)
	r := testutil.Call(t, h.GetAttachmentOfficePreview, request(att)).Want(http.StatusServiceUnavailable)
	if r.Header().Get("Retry-After") != "5" {
		t.Fatal("missing retry hint")
	}
	dbfx.Exec(t, "UPDATE attachment SET size_bytes=$1 WHERE id=$2", officepreview.MaxInputSize+1, att)
	testutil.Call(t, h.GetAttachmentOfficePreview, request(att)).Want(http.StatusRequestEntityTooLarge)
	dbfx.Exec(t, "UPDATE attachment SET filename='archive.zip' WHERE id=$1", att)
	testutil.Call(t, h.GetAttachmentOfficePreview, request(att)).Want(http.StatusUnsupportedMediaType)
	otherWorkspace := dbfx.Workspace(t, "Office isolation", "office-isolation-tests")
	dbfx.Exec(t, "UPDATE attachment SET workspace_id=$1 WHERE id=$2", otherWorkspace, att)
	testutil.Call(t, h.GetAttachmentOfficePreview, request(att)).Want(http.StatusNotFound)
}
