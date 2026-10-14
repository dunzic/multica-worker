package handler

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func docxFixture(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestDocxPreviewText(t *testing.T) {
	body := `<w:document xmlns:w="urn:word"><w:p><w:r><w:t>Candidate &amp; evidence</w:t></w:r></w:p><w:p><w:r><w:t>&lt;script&gt;</w:t></w:r></w:p></w:document>`
	result, err := docxPreviewText(docxFixture(t, "word/document.xml", body))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "Candidate & evidence\n<script>" {
		t.Fatalf("unexpected text %q", result)
	}
}
func TestDocxPreviewRejectsUnsupportedArchives(t *testing.T) {
	for name, data := range map[string][]byte{
		"invalid":   []byte("not a zip"),
		"missing":   docxFixture(t, "../document.xml", "<p/>"),
		"oversized": docxFixture(t, "word/document.xml", strings.Repeat("x", maxPreviewTextSize+1)),
		"entity":    docxFixture(t, "word/document.xml", `<w:t xmlns:w="urn:word">&external;</w:t>`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := docxPreviewText(data); err == nil {
				t.Fatal("unsafe document accepted")
			}
		})
	}
}
