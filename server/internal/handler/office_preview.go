package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/multica-ai/multica/server/internal/officepreview"
)

// GetAttachmentOfficePreview renders existing and newly uploaded Office files
// on demand. There is no derived attachment with independent permissions: every
// read reuses the original's workspace and topic ACL before touching storage.
func (h *Handler) GetAttachmentOfficePreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	if officepreview.Extension(att.Filename, att.ContentType) == "" {
		writeError(w, http.StatusUnsupportedMediaType, "preview not supported for this file type")
		return
	}
	if h.OfficePreview == nil {
		writeFeatureDisabled(w, "office_preview_not_configured", "Office preview is not configured")
		return
	}
	if att.SizeBytes > officepreview.MaxInputSize {
		writeError(w, http.StatusRequestEntityTooLarge, "Office file too large for preview")
		return
	}
	if h.Storage == nil {
		writeFeatureDisabled(w, "storage_not_configured", "storage not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), officepreview.Timeout)
	defer cancel()
	reader, err := h.Storage.GetReader(ctx, h.Storage.KeyFromURL(att.Url))
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment object not found")
		return
	}
	defer reader.Close()
	pdf, err := h.OfficePreview.Convert(ctx, att.Filename, att.ContentType, reader)
	if err != nil {
		switch {
		case errors.Is(err, officepreview.ErrTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "Office preview size limit exceeded")
		case errors.Is(err, officepreview.ErrInvalidDocument):
			writeError(w, http.StatusUnprocessableEntity, "cannot preview this Office document")
		case errors.Is(err, officepreview.ErrBusy):
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "Office preview service is busy; try again")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "Office preview timed out")
		default:
			writeError(w, http.StatusBadGateway, "Office preview service unavailable")
		}
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="preview.pdf"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)
	w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
	_, _ = w.Write(pdf)
}
