// Package officepreview converts stored Office attachments using an isolated,
// operator-owned Gotenberg service. It never sends source URLs or credentials.
package officepreview

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	MaxInputSize    = 20 << 20
	MaxOutputSize   = 40 << 20
	Timeout         = 60 * time.Second
	maxExpandedSize = 100 << 20
)

var (
	ErrUnsupported     = errors.New("unsupported Office format")
	ErrTooLarge        = errors.New("Office preview size limit exceeded")
	ErrBusy            = errors.New("Office preview service is busy")
	ErrUnavailable     = errors.New("Office preview service unavailable")
	ErrInvalidDocument = errors.New("cannot convert Office document")
)

var extensions = map[string]string{
	"application/msword": ".doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.ms-powerpoint":                                             ".ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
}

// Extension is deliberately narrower than LibreOffice's input allowlist.
// Macro-enabled formats, arbitrary HTML and executable inputs are not accepted.
func Extension(filename, contentType string) string {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(filename, "\\", "/")))
	for _, supported := range extensions {
		if ext == supported {
			return ext
		}
	}
	if ext != "" {
		return ""
	}
	return extensions[strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))]
}

type Client struct {
	endpoint string
	http     *http.Client
	slots    chan struct{}
}

// New returns nil when disabled. The URL is operator configuration only;
// attachment metadata and request input must never select the destination.
func New(rawURL string) (*Client, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, nil
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("MULTICA_OFFICE_PREVIEW_URL must be an http(s) origin without credentials, path or query")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{
		endpoint: strings.TrimRight(u.String(), "/") + "/forms/libreoffice/convert",
		http: &http.Client{
			Transport:     transport,
			Timeout:       Timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots: make(chan struct{}, 2),
	}, nil
}

func (c *Client) Convert(ctx context.Context, filename, contentType string, reader io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrUnavailable
	}
	ext := Extension(filename, contentType)
	if ext == "" {
		return nil, ErrUnsupported
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	source, err := io.ReadAll(io.LimitReader(reader, MaxInputSize+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, ErrInvalidDocument
	}
	if len(source) > MaxInputSize {
		return nil, ErrTooLarge
	}
	if err := validateSource(source, ext); err != nil {
		return nil, err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("files", "document"+ext)
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(source); err != nil {
		return nil, err
	}
	// Disable interactive forms and index refresh. Full-sheet export avoids
	// silently omitting spreadsheet cells outside the author's print ranges.
	fields := map[string]string{"exportFormFields": "false", "updateIndexes": "false"}
	if ext == ".xls" || ext == ".xlsx" {
		fields["singlePageSheets"] = "true"
	}
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		// Do not expose the internal service URL or upstream body to clients.
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return nil, ErrInvalidDocument
	case http.StatusRequestEntityTooLarge:
		return nil, ErrTooLarge
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, ErrBusy
	case http.StatusGatewayTimeout:
		return nil, context.DeadlineExceeded
	default:
		return nil, ErrUnavailable
	}
	if res.ContentLength > MaxOutputSize {
		return nil, ErrTooLarge
	}
	pdf, err := io.ReadAll(io.LimitReader(res.Body, MaxOutputSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrUnavailable
	}
	if len(pdf) > MaxOutputSize {
		return nil, ErrTooLarge
	}
	if !strings.EqualFold(strings.TrimSpace(strings.SplitN(res.Header.Get("Content-Type"), ";", 2)[0]), "application/pdf") || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, ErrInvalidDocument
	}
	return pdf, nil
}

func validateSource(source []byte, ext string) error {
	if ext == ".doc" || ext == ".xls" || ext == ".ppt" {
		if !bytes.HasPrefix(source, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
			return ErrInvalidDocument
		}
		return nil
	}
	z, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil || len(z.File) > 4096 {
		return ErrInvalidDocument
	}
	var total uint64
	var hasDocument bool
	mainPart := map[string]string{".docx": "word/document.xml", ".xlsx": "xl/workbook.xml", ".pptx": "ppt/presentation.xml"}[ext]
	for _, file := range z.File {
		clean := path.Clean(file.Name)
		if file.Flags&1 != 0 || strings.Contains(file.Name, "\\") || path.IsAbs(file.Name) || clean == ".." || strings.HasPrefix(clean, "../") {
			return ErrInvalidDocument
		}
		if file.UncompressedSize64 > maxExpandedSize || total > maxExpandedSize-file.UncompressedSize64 {
			return ErrTooLarge
		}
		total += file.UncompressedSize64
		hasDocument = hasDocument || file.Name == mainPart
	}
	if !hasDocument {
		return ErrInvalidDocument
	}
	return nil
}
