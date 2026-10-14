package handler

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

func isDocxPreviewable(contentType, filename string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.Split(contentType, ";")[0]), "application/vnd.openxmlformats-officedocument.wordprocessingml.document") || strings.HasSuffix(strings.ToLower(filename), ".docx")
}

// Extract only document text. Never execute embedded content or follow relationships.
func docxPreviewText(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	if len(zr.File) > 1024 {
		return nil, errors.New("too many entries")
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		if f.Flags&1 != 0 || f.UncompressedSize64 > maxPreviewTextSize {
			return nil, errors.New("document too large or encrypted")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		raw, err := io.ReadAll(io.LimitReader(rc, maxPreviewTextSize+1))
		if err != nil || len(raw) > maxPreviewTextSize {
			return nil, errors.New("cannot read document")
		}
		dec := xml.NewDecoder(bytes.NewReader(raw))
		var out strings.Builder
		inText := false
		for {
			token, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			switch v := token.(type) {
			case xml.StartElement:
				if v.Name.Local == "t" {
					inText = true
				}
				if v.Name.Local == "tab" {
					out.WriteByte('\t')
				}
				if v.Name.Local == "br" {
					out.WriteByte('\n')
				}
			case xml.CharData:
				if inText {
					out.Write(v)
				}
			case xml.EndElement:
				if v.Name.Local == "t" {
					inText = false
				}
				if v.Name.Local == "p" || v.Name.Local == "tr" {
					out.WriteByte('\n')
				}
				if v.Name.Local == "tc" {
					out.WriteByte('\t')
				}
			}
			if out.Len() > maxPreviewTextSize {
				return nil, errors.New("preview too large")
			}
		}
		return []byte(strings.TrimSpace(out.String())), nil
	}
	return nil, errors.New("document.xml missing")
}
