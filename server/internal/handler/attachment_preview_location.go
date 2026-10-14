package handler

import "net/http"

// ResolveAttachmentPreview finds the attachment's actual workspace before the
// client requests metadata or bytes. Copied links carry no reliable workspace
// context, so reuse the download permission gate, not the caller's active tab.
func (h *Handler) ResolveAttachmentPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Machine credentials must not inherit their owner's cross-workspace access.
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	att, ok := h.loadAttachmentForDownload(w, r)
	if !ok {
		return
	}
	workspace, err := h.Queries.GetWorkspace(r.Context(), att.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"workspace_id":   uuidToString(workspace.ID),
		"workspace_slug": workspace.Slug,
	})
}
