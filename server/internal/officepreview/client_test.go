package officepreview

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func archive(t *testing.T, part string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	f, err := z.Create(part)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("x"), size)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtension(t *testing.T) {
	for _, ext := range []string{"doc", "docx", "xls", "xlsx", "ppt", "pptx"} {
		if got := Extension("文件."+strings.ToUpper(ext), "application/octet-stream"); got != "."+ext {
			t.Errorf("extension=%s", got)
		}
	}
	for mime, ext := range extensions {
		if got := Extension("document", mime+"; charset=utf-8"); got != ext {
			t.Errorf("MIME %s=%s", mime, got)
		}
	}
	for _, filename := range []string{"file.docm", "file.xlsm", "file.pptm", "file.zip", "file.exe", "file.pdf"} {
		if got := Extension(filename, "application/msword"); got != "" {
			t.Errorf("accepted %s", filename)
		}
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	for _, raw := range []string{"file:///tmp/a", "https://user:password@example.test", "https://example.test/?token=x", "http://example.test/path", "//example.test", "bad"} {
		if _, err := New(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if client, err := New(""); err != nil || client != nil {
		t.Fatal("empty URL should disable conversion")
	}
}

func TestConvertStoredBytesWithoutCredentialsOrSourceURL(t *testing.T) {
	source := archive(t, "xl/workbook.xml", 20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/forms/libreoffice/convert" {
			t.Errorf("wrong request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("forwarded user credentials")
		}
		if err := r.ParseMultipartForm(MaxInputSize); err != nil {
			t.Error(err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, h, err := r.FormFile("files")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		body, _ := io.ReadAll(f)
		if h.Filename != "document.xlsx" || !bytes.Equal(body, source) {
			t.Error("wrong input")
		}
		if r.FormValue("singlePageSheets") != "true" || r.FormValue("exportFormFields") != "false" || r.FormValue("downloadFrom") != "" {
			t.Error("unsafe conversion fields")
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-test"))
	}))
	defer upstream.Close()
	client, _ := New(upstream.URL)
	pdf, err := client.Convert(context.Background(), "secret-client.xlsx", "", bytes.NewReader(source))
	if err != nil || string(pdf) != "%PDF-test" {
		t.Fatalf("conversion=%q, %v", pdf, err)
	}
}

func TestConvertRejectsInvalidAndOversizedDocumentsBeforeCallingService(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(500) }))
	defer upstream.Close()
	client, _ := New(upstream.URL)
	for _, tc := range []struct {
		name, filename string
		input          []byte
		want           error
	}{
		{"renamed HTML", "a.docx", []byte("<html>hostile</html>"), ErrInvalidDocument},
		{"wrong archive", "a.docx", archive(t, "xl/workbook.xml", 8), ErrInvalidDocument},
		{"traversal", "a.docx", archive(t, "../word/document.xml", 8), ErrInvalidDocument},
		{"parent entry", "a.docx", archive(t, "..", 8), ErrInvalidDocument},
		{"too large", "a.doc", bytes.Repeat([]byte("x"), MaxInputSize+1), ErrTooLarge},
		{"decompression bomb", "a.docx", archive(t, "word/document.xml", maxExpandedSize+1), ErrTooLarge},
		{"renamed legacy", "a.xls", []byte("<script>"), ErrInvalidDocument},
		{"macro extension", "a.docm", []byte("anything"), ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Convert(context.Background(), tc.filename, "", bytes.NewReader(tc.input))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid inputs reached converter: %d", calls)
	}
}

func TestConvertResponseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name               string
		status             int
		mime, body, length string
		want               error
	}{
		{"invalid", 400, "text/plain", "private diagnostic", "", ErrInvalidDocument},
		{"busy", 503, "text/plain", "internal URL", "", ErrBusy},
		{"timeout", 504, "text/plain", "internal diagnostic", "", context.DeadlineExceeded},
		{"upstream error", 500, "text/plain", "secret", "", ErrUnavailable},
		{"HTML instead of PDF", 200, "text/html", "%PDF-fake", "", ErrInvalidDocument},
		{"bad PDF", 200, "application/pdf", "not a PDF", "", ErrInvalidDocument},
		{"oversized response", 200, "application/pdf", "", "41943041", ErrTooLarge},
		{"redirect", 302, "text/html", "", "", ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				w.Header().Set("Location", "/must-not-follow")
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			client, _ := New(upstream.URL)
			_, err := client.Convert(context.Background(), "a.docx", "", bytes.NewReader(archive(t, "word/document.xml", 1)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestConvertConcurrencyAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, _ := New(upstream.URL)
	source := archive(t, "word/document.xml", 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 2)
	for range 2 {
		go func() { _, err := client.Convert(ctx, "a.docx", "", bytes.NewReader(source)); finished <- err }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("conversion did not start")
		}
	}
	if _, err := client.Convert(ctx, "a.docx", "", bytes.NewReader(source)); !errors.Is(err, ErrBusy) {
		t.Fatalf("unbounded conversions: %v", err)
	}
	cancel()
	for range 2 {
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	}
	if len(client.slots) != 0 {
		t.Fatal("conversion slots leaked")
	}
	if _, err := client.Convert(ctx, "a.docx", "", strings.NewReader("invalid")); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled conversion consumed input: %v", err)
	}
}

func TestConvertResponseBodyTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client, _ := New(upstream.URL)
	client.http.Timeout = 50 * time.Millisecond
	_, err := client.Convert(context.Background(), "a.docx", "", bytes.NewReader(archive(t, "word/document.xml", 1)))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out PDF must not become a retryable failure: %v", err)
	}
}
