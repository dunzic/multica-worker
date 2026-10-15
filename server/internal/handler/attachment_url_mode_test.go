package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The existing capability header remains parseable, but revocable access now
// requires stable bulk URLs for every caller, including older clients.

func loadTestAttachment(t *testing.T, id string) db.Attachment {
	t.Helper()
	att, err := testHandler.Queries.GetAttachment(context.Background(), db.GetAttachmentParams{
		ID:          parseUUID(id),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("GetAttachment: %v", err)
	}
	return att
}

// withCloudFrontSigner exercises a configured CDN deployment without issuing
// protected storage credentials.
func withCloudFrontSigner(t *testing.T) {
	t.Helper()
	orig := testHandler.CFSigner
	testHandler.CFSigner = testCloudFrontSigner(t)
	t.Cleanup(func() { testHandler.CFSigner = orig })
}

func requestWithCapabilities(caps string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/issues/x/comments", nil)
	if caps != "" {
		r.Header.Set("X-Client-Capabilities", caps)
	}
	return r
}

// Preserve request parsing; neither selection may bypass current permissions.
func TestAttachmentURLMode_DefaultsToSignedWithoutCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps string
	}{
		{"no header at all", ""},
		{"unrelated capabilities", "chat_draft_restore,something_else"},
		{"near-miss token", "stable_attachment_url"},
		{"empty header", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := attachmentURLModeFromRequest(requestWithCapabilities(tc.caps)); got != attachmentURLModeSigned {
				t.Errorf("caps %q resolved to stable mode; the server default must never move", tc.caps)
			}
		})
	}
	// A nil request has no declaration to read either.
	if got := attachmentURLModeFromRequest(nil); got != attachmentURLModeSigned {
		t.Errorf("nil request must resolve to signed mode")
	}
}

func TestAttachmentURLMode_HonorsAdvertisedCapability(t *testing.T) {
	for _, caps := range []string{
		ClientCapabilityStableAttachmentURLs,
		"chat_draft_restore," + ClientCapabilityStableAttachmentURLs,
		ClientCapabilityStableAttachmentURLs + ",other",
		"  " + ClientCapabilityStableAttachmentURLs + "  ",
	} {
		if got := attachmentURLModeFromRequest(requestWithCapabilities(caps)); got != attachmentURLModeStable {
			t.Errorf("caps %q did not resolve to stable mode", caps)
		}
	}
}

func TestAttachmentToResponse_AllBulkModesUseProtectedURLs(t *testing.T) {
	withCloudFrontSigner(t)

	id := seedAttachmentURL(t, "https://static.example.test/ws/a.png", "a.png", "image/png", 1234)
	att := loadTestAttachment(t, id)

	signed := testHandler.attachmentToResponse(att, attachmentURLModeSigned)
	stable := testHandler.attachmentToResponse(att, attachmentURLModeStable)

	want := "/api/attachments/" + id + "/download"
	if stable.DownloadURL != want || signed.DownloadURL != want {
		t.Fatalf("both list modes must reauthorize at %q: %+v %+v", want, stable, signed)
	}

	// Only download_url may move. url and markdown_url carry different
	// contracts (raw storage identity / persistable reference) and clients
	// index markdown bodies on markdown_url, so drift there would corrupt
	// already-persisted content.
	if stable.URL != signed.URL {
		t.Errorf("url must not change with mode: %q vs %q", stable.URL, signed.URL)
	}
	if stable.MarkdownURL != signed.MarkdownURL {
		t.Errorf("markdown_url must not change with mode: %q vs %q", stable.MarkdownURL, signed.MarkdownURL)
	}
	if stable.ID != signed.ID || stable.Filename != signed.Filename ||
		stable.ContentType != signed.ContentType || stable.SizeBytes != signed.SizeBytes {
		t.Errorf("stable mode altered identity/metadata fields")
	}
}

func TestAttachmentToResponse_BulkURLsNeverRotateOrExposeStorage(t *testing.T) {
	withCloudFrontSigner(t)

	id := seedAttachmentURL(t, "https://static.example.test/ws/b.png", "b.png", "image/png", 10)
	att := loadTestAttachment(t, id)

	att.IssueID = parseUUID(capabilityTestAttachmentID)
	stableA := testHandler.attachmentToResponse(att, attachmentURLModeStable)
	stableB := testHandler.attachmentToResponse(att, attachmentURLModeStable)
	if stableA.DownloadURL != stableB.DownloadURL {
		t.Errorf("stable download_url must be deterministic: %q vs %q", stableA.DownloadURL, stableB.DownloadURL)
	}
	if strings.Contains(stableA.DownloadURL, "Policy=") || strings.Contains(stableA.DownloadURL, "Signature=") {
		t.Errorf("stable download_url leaked signature params: %q", stableA.DownloadURL)
	}

	signed := testHandler.attachmentToResponse(att, attachmentURLModeSigned)
	if signed.DownloadURL != stableA.DownloadURL || signed.URL == att.Url || signed.AttachmentDownloadURL != "" {
		t.Fatalf("bulk response must contain stable protected paths only: %+v", signed)
	}
}

// TestAttachmentToResponse_StableModeIsNoOpWithoutSigner pins the presign /
// proxy deployments: they never took the signing branch, so the capability
// changes nothing for them.
func TestAttachmentToResponse_StableModeIsNoOpWithoutSigner(t *testing.T) {
	orig := testHandler.CFSigner
	testHandler.CFSigner = nil
	t.Cleanup(func() { testHandler.CFSigner = orig })

	id := seedAttachmentURL(t, "http://rustfs:9000/test-bucket/c.txt", "c.txt", "text/plain", 5)
	att := loadTestAttachment(t, id)

	signed := testHandler.attachmentToResponse(att, attachmentURLModeSigned)
	stable := testHandler.attachmentToResponse(att, attachmentURLModeStable)
	if signed.DownloadURL != stable.DownloadURL {
		t.Errorf("without a signer both modes must agree: %q vs %q", signed.DownloadURL, stable.DownloadURL)
	}
}

// TestGetAttachmentByID_IgnoresStableCapability is the load-bearing carve-out.
// Stable-mode callers exchange the stable path for a loadable URL here —
// `multica attachment download <id>` reads download_url off this endpoint — so
// honoring the capability would break the flow that makes stable mode safe.
func TestGetAttachmentByID_IgnoresStableCapability(t *testing.T) {
	withCloudFrontSigner(t)

	id := seedAttachmentURL(t, "https://static.example.test/ws/d.png", "d.png", "image/png", 7)

	req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+id, nil)
	req.Header.Set("X-Client-Capabilities", ClientCapabilityStableAttachmentURLs)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	testHandler.GetAttachmentByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/attachments/{id} = %d, body %s", w.Code, w.Body.String())
	}
	var resp AttachmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.DownloadURL, "/signed-download?") || !strings.Contains(resp.DownloadURL, "sig=") {
		t.Errorf("single-attachment endpoint must mint a revocable capability, even for stable-mode callers; got %q", resp.DownloadURL)
	}
}
