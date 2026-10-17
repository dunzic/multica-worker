package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/reportfeedback"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errReportConflict = errors.New("report changed; reload before saving")
var errReportForbidden = errors.New("not authorized to configure this report")
var errReportResponseWritten = errors.New("report authorization response already written")

type reportActor struct {
	kind, name     string
	id             pgtype.UUID
	manage, create bool
}

func (h *Handler) reportActor(w http.ResponseWriter, r *http.Request, att db.Attachment) (reportActor, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return reportActor{}, false
	}
	a := reportActor{kind: "member", id: parseUUID(userID), create: !att.SourceContextID.Valid}
	if isMachineCredentialActor(r) {
		token, verified := middleware.AuthenticatedTaskToken(r.Context())
		if !verified || token.WorkspaceID != att.WorkspaceID {
			writeError(w, http.StatusForbidden, "report access requires a human or verified task credential")
			return a, false
		}
		a.kind, a.id = "agent", token.AgentID
		a.manage = !att.SourceContextID.Valid && att.TaskID.Valid && att.TaskID == token.TaskID && att.UploaderType == "agent" && att.UploaderID == token.AgentID
		a.create = a.manage
		agent, err := h.Queries.GetAgent(r.Context(), a.id)
		if err != nil {
			writeError(w, http.StatusForbidden, "agent unavailable")
			return a, false
		}
		a.name = agent.Name
	} else {
		member, err := h.getWorkspaceMember(r.Context(), userID, uuidToString(att.WorkspaceID))
		if err != nil {
			writeError(w, http.StatusNotFound, "attachment not found")
			return a, false
		}
		a.manage = !att.SourceContextID.Valid && (member.Role == "owner" || member.Role == "admin" || (att.UploaderType == "member" && att.UploaderID == a.id))
		user, err := h.Queries.GetUser(r.Context(), a.id)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user unavailable")
			return a, false
		}
		a.name = user.Name
	}
	return a, true
}

func readReportJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report request")
		return false
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "expected one JSON document")
		return false
	}
	return true
}

func reportJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func reportRowResponse(row db.ReportFeedbackRow) map[string]any {
	return map[string]any{"id": uuidToString(row.ID), "label": row.Label, "source_attachment_id": uuidToString(row.SourceAttachmentID),
		"values": json.RawMessage(row.Values), "revision": row.Revision, "archived": row.ArchivedAt.Valid, "updated_at": row.UpdatedAt.Time}
}

func reportWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errReportResponseWritten):
	case errors.Is(err, errReportConflict):
		writeError(w, http.StatusConflict, errReportConflict.Error())
	case errors.Is(err, errReportForbidden):
		writeError(w, http.StatusForbidden, errReportForbidden.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "report or attachment not found")
	default:
		writeError(w, http.StatusInternalServerError, "could not save report feedback")
	}
}

// Use the same owner-first order as attachment/comment/issue deletion, with
// a workspace fence first so teardown cannot miss newly inserted feedback.
// Recheck permissions after lock waits, not only at the request's entry.
func (h *Handler) withReportWrite(w http.ResponseWriter, r *http.Request, att db.Attachment, write func(*Handler, db.Attachment, reportActor) error) error {
	userID, ok := requireUserID(w, r)
	if !ok {
		return errReportResponseWritten
	}
	for attempt := 0; attempt < attachmentOwnerLockAttempts; attempt++ {
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			return err
		}
		q := h.Queries.WithTx(tx)
		fresh, err := func() (db.Attachment, error) {
			if _, err := q.LockWorkspaceForChatSessionCreate(r.Context(), att.WorkspaceID); err != nil {
				return db.Attachment{}, err
			}
			// Workspace removal takes this lock before it changes grants/tasks.
			if err := q.LockSubscriberWrites(r.Context(), db.LockSubscriberWritesParams{WorkspaceID: att.WorkspaceID, UserID: parseUUID(userID)}); err != nil {
				return db.Attachment{}, err
			}
			if _, err := q.LockReportFeedbackMember(r.Context(), db.LockReportFeedbackMemberParams{WorkspaceID: att.WorkspaceID, UserID: parseUUID(userID)}); err != nil {
				return db.Attachment{}, err
			}
			// Group removals and legacy chat deletion must serialize with writes.
			topicID, sessionID := att.TopicID, att.ChatSessionID
			if att.ChatMessageID.Valid {
				message, err := q.GetChatMessage(r.Context(), att.ChatMessageID)
				if err != nil {
					return db.Attachment{}, err
				}
				if !topicID.Valid {
					topicID = message.TopicID
				}
				if !sessionID.Valid {
					sessionID = message.ChatSessionID
				}
			}
			if topicID.Valid {
				topic, err := q.GetTopic(r.Context(), topicID)
				if err != nil {
					return db.Attachment{}, err
				}
				if _, err := q.LockConversation(r.Context(), topic.ConversationID); err != nil {
					return db.Attachment{}, err
				}
			}
			if sessionID.Valid {
				if _, err := q.LockChatSessionForDelete(r.Context(), sessionID); err != nil {
					return db.Attachment{}, err
				}
			}
			issueID := att.IssueID
			if !issueID.Valid && att.CommentID.Valid {
				comment, err := q.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: att.CommentID, WorkspaceID: att.WorkspaceID})
				if err != nil {
					return db.Attachment{}, err
				}
				issueID = comment.IssueID
			}
			if issueID.Valid {
				if _, err := q.LockIssueForAttachmentWrite(r.Context(), db.LockIssueForAttachmentWriteParams{ID: issueID, WorkspaceID: att.WorkspaceID}); err != nil {
					return db.Attachment{}, err
				}
			}
			if att.CommentID.Valid {
				if _, err := q.LockLiveComment(r.Context(), db.LockLiveCommentParams{ID: att.CommentID, WorkspaceID: att.WorkspaceID}); err != nil {
					return db.Attachment{}, err
				}
			}
			return q.LockAttachmentRow(r.Context(), db.LockAttachmentRowParams{ID: att.ID, WorkspaceID: att.WorkspaceID})
		}()
		if err == nil && (fresh.IssueID != att.IssueID || fresh.CommentID != att.CommentID || fresh.TopicID != att.TopicID || fresh.ChatSessionID != att.ChatSessionID || fresh.ChatMessageID != att.ChatMessageID) {
			_ = tx.Rollback(r.Context())
			att = fresh
			continue
		}
		if err == nil {
			th := *h
			th.Queries = q
			if !th.authorizeAttachmentRequest(w, r, fresh) {
				err = errReportResponseWritten
			} else if actor, ok := th.reportActor(w, r, fresh); !ok {
				err = errReportResponseWritten
			} else {
				err = write(&th, fresh, actor)
			}
		}
		if err == nil {
			err = tx.Commit(r.Context())
		} else {
			_ = tx.Rollback(r.Context())
		}
		return err
	}
	return errReportConflict
}

func reportChange(ctx context.Context, q *db.Queries, att db.Attachment, actor reportActor, rowID pgtype.UUID, kind string, before, after any) error {
	return q.CreateReportFeedbackChange(ctx, db.CreateReportFeedbackChangeParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, RowID: rowID,
		Kind: kind, ActorType: actor.kind, ActorID: actor.id, ActorName: actor.name, BeforeData: reportJSON(before), AfterData: reportJSON(after)})
}

// GetReportFeedback returns a bounded page. No payload is broadcast workspace-
// wide; each collaborator polls this permission-checked endpoint while open.
func (h *Handler) GetReportFeedback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	actor, ok := h.reportActor(w, r, att)
	if !ok {
		return
	}
	var after pgtype.UUID
	if s := r.URL.Query().Get("after"); s != "" {
		after, ok = parseUUIDOrBadRequest(w, s, "after")
		if !ok {
			return
		}
	}
	schema, err := h.Queries.GetReportFeedbackSchema(r.Context(), db.GetReportFeedbackSchemaParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "could not load report")
		return
	}
	fields := []reportfeedback.Field{}
	if err == nil {
		if err = json.Unmarshal(schema.Fields, &fields); err != nil {
			writeError(w, 500, "invalid stored report fields")
			return
		}
	}
	rows, err := h.Queries.ListReportFeedbackRows(r.Context(), db.ListReportFeedbackRowsParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, AfterID: after, Limit: 51})
	if err != nil {
		writeError(w, 500, "could not load report records")
		return
	}
	hasRows, err := h.Queries.HasReportFeedbackRows(r.Context(), db.HasReportFeedbackRowsParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID})
	if err != nil {
		writeError(w, 500, "could not load report record state")
		return
	}
	next := ""
	if len(rows) > 50 {
		rows = rows[:50]
		next = uuidToString(rows[49].ID)
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, reportRowResponse(row))
	}
	writeJSON(w, 200, map[string]any{"fields": fields, "schema_revision": schema.Revision, "rows": items, "next_cursor": next, "has_records": hasRows,
		"can_manage": actor.manage, "can_create": actor.create, "can_edit": actor.kind == "member" && !att.SourceContextID.Valid})
}

func (h *Handler) PutReportFeedbackSchema(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision int64                  `json:"expected_revision"`
		Fields           []reportfeedback.Field `json:"fields"`
	}
	if !readReportJSON(w, r, &req) {
		return
	}
	if req.ExpectedRevision < 0 || reportfeedback.ValidateFields(req.Fields) != nil {
		writeError(w, 400, "invalid report field definitions (1-20 typed fields required)")
		return
	}
	var result db.ReportFeedbackSchema
	err := h.withReportWrite(w, r, att, func(th *Handler, att db.Attachment, actor reportActor) error {
		if !actor.manage {
			return errReportForbidden
		}
		q := th.Queries
		before, err := q.GetReportFeedbackSchema(r.Context(), db.GetReportFeedbackSchemaParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before.Revision != req.ExpectedRevision {
			return errReportConflict
		}
		if actor.kind == "agent" && before.Revision > 0 {
			return errReportForbidden
		}
		oldFields := []reportfeedback.Field{}
		if before.Revision > 0 {
			if err := json.Unmarshal(before.Fields, &oldFields); err != nil {
				return err
			}
		}
		hasRows, err := q.HasReportFeedbackRows(r.Context(), db.HasReportFeedbackRowsParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID})
		if err != nil {
			return err
		}
		if err := reportfeedback.ValidateSchemaChange(oldFields, req.Fields, hasRows); err != nil {
			writeError(w, 400, err.Error())
			return errReportResponseWritten
		}
		if before.Revision == 0 {
			result, err = q.CreateReportFeedbackSchema(r.Context(), db.CreateReportFeedbackSchemaParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, Fields: reportJSON(req.Fields)})
		} else {
			result, err = q.UpdateReportFeedbackSchema(r.Context(), db.UpdateReportFeedbackSchemaParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, Fields: reportJSON(req.Fields), Revision: req.ExpectedRevision})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return errReportConflict
		}
		if err != nil {
			return err
		}
		return reportChange(r.Context(), q, att, actor, pgtype.UUID{}, "schema", map[string]any{"fields": oldFields, "revision": before.Revision}, map[string]any{"fields": req.Fields, "revision": result.Revision})
	})
	if err != nil {
		reportWriteError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"fields": req.Fields, "revision": result.Revision})
}

// Reject links from a narrower conversation into a broader report. Every later
// preview/download still rechecks the linked attachment's own permissions.
func (h *Handler) reportSourceAllowed(w http.ResponseWriter, r *http.Request, att db.Attachment, sourceID pgtype.UUID) bool {
	if !sourceID.Valid {
		return true
	}
	source, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{ID: sourceID, WorkspaceID: att.WorkspaceID})
	if err != nil || !h.authorizeAttachmentRequest(w, r, source) {
		if err != nil {
			writeError(w, 404, "source attachment not found")
		}
		return false
	}
	topicFor := func(a db.Attachment) (pgtype.UUID, error) {
		if a.TopicID.Valid {
			return a.TopicID, nil
		}
		if a.ChatMessageID.Valid {
			m, e := h.Queries.GetMessageV2(r.Context(), a.ChatMessageID)
			if e == nil {
				return m.TopicID, nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return pgtype.UUID{}, e
			}
		}
		return pgtype.UUID{}, nil
	}
	sourceTopic, e1 := topicFor(source)
	reportTopic, e2 := topicFor(att)
	if e1 != nil || e2 != nil {
		writeError(w, 500, "could not verify source scope")
		return false
	}
	if sourceTopic.Valid {
		if !reportTopic.Valid {
			writeError(w, 400, "source attachment has a narrower audience")
			return false
		}
		a, e1 := h.Queries.GetTopic(r.Context(), sourceTopic)
		b, e2 := h.Queries.GetTopic(r.Context(), reportTopic)
		if e1 != nil || e2 != nil || a.ConversationID != b.ConversationID {
			writeError(w, 400, "source attachment belongs to another conversation")
			return false
		}
	}
	return true
}

func (h *Handler) PutReportFeedbackRow(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	rowID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "rowID"), "row id")
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision   int64                      `json:"expected_revision"`
		SchemaRevision     int64                      `json:"schema_revision"`
		Label              string                     `json:"label"`
		SourceAttachmentID string                     `json:"source_attachment_id"`
		Values             map[string]json.RawMessage `json:"values"`
	}
	if !readReportJSON(w, r, &req) {
		return
	}
	if req.ExpectedRevision < 0 || req.SchemaRevision < 1 || strings.TrimSpace(req.Label) == "" || utf8.RuneCountInString(req.Label) > 200 || strings.ContainsRune(req.Label, 0) {
		writeError(w, 400, "invalid record label or revision")
		return
	}
	var source pgtype.UUID
	if req.SourceAttachmentID != "" {
		source, ok = parseUUIDOrBadRequest(w, req.SourceAttachmentID, "source attachment id")
		if !ok {
			return
		}
	}
	var result db.ReportFeedbackRow
	err := h.withReportWrite(w, r, att, func(th *Handler, att db.Attachment, actor reportActor) error {
		if !actor.create || (actor.kind == "agent" && req.ExpectedRevision != 0) {
			return errReportForbidden
		}
		q := th.Queries
		schema, err := q.GetReportFeedbackSchema(r.Context(), db.GetReportFeedbackSchemaParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID})
		if err != nil {
			return err
		}
		if schema.Revision != req.SchemaRevision {
			return errReportConflict
		}
		var fields []reportfeedback.Field
		if err = json.Unmarshal(schema.Fields, &fields); err != nil {
			return err
		}
		if err := reportfeedback.ValidateValues(fields, req.Values); err != nil {
			writeError(w, 400, err.Error())
			return errReportResponseWritten
		}
		if !th.reportSourceAllowed(w, r, att, source) {
			return errReportResponseWritten
		}
		before, err := q.GetReportFeedbackRow(r.Context(), db.GetReportFeedbackRowParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, ID: rowID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before.Revision != req.ExpectedRevision || before.ArchivedAt.Valid {
			return errReportConflict
		}
		kind := "create"
		var previous any
		if before.Revision == 0 {
			result, err = q.CreateReportFeedbackRow(r.Context(), db.CreateReportFeedbackRowParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, ID: rowID, Label: req.Label, SourceAttachmentID: source, Values: reportJSON(req.Values)})
		} else {
			kind = "update"
			previous = reportRowResponse(before)
			result, err = q.UpdateReportFeedbackRow(r.Context(), db.UpdateReportFeedbackRowParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, ID: rowID, Label: req.Label, SourceAttachmentID: source, Values: reportJSON(req.Values), Revision: req.ExpectedRevision})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return errReportConflict
		}
		if err != nil {
			return err
		}
		return reportChange(r.Context(), q, att, actor, rowID, kind, previous, reportRowResponse(result))
	})
	if err != nil {
		reportWriteError(w, err)
		return
	}
	writeJSON(w, 200, reportRowResponse(result))
}

func (h *Handler) ArchiveReportFeedbackRow(w http.ResponseWriter, r *http.Request) {
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	rowID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "rowID"), "row id")
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !readReportJSON(w, r, &req) {
		return
	}
	if req.ExpectedRevision < 1 {
		writeError(w, 400, "revision is required")
		return
	}
	err := h.withReportWrite(w, r, att, func(th *Handler, att db.Attachment, actor reportActor) error {
		if !actor.manage || actor.kind != "member" {
			return errReportForbidden
		}
		q := th.Queries
		before, err := q.GetReportFeedbackRow(r.Context(), db.GetReportFeedbackRowParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, ID: rowID})
		if err != nil {
			return err
		}
		after, err := q.ArchiveReportFeedbackRow(r.Context(), db.ArchiveReportFeedbackRowParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, ID: rowID, Revision: req.ExpectedRevision})
		if errors.Is(err, pgx.ErrNoRows) {
			return errReportConflict
		}
		if err != nil {
			return err
		}
		return reportChange(r.Context(), q, att, actor, rowID, "archive", reportRowResponse(before), reportRowResponse(after))
	})
	if err != nil {
		reportWriteError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"archived": true})
}

func (h *Handler) ListReportFeedbackHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	att, ok := h.loadAttachmentForRequest(w, r)
	if !ok {
		return
	}
	if _, ok := h.reportActor(w, r, att); !ok {
		return
	}
	var before int64
	if s := r.URL.Query().Get("before"); s != "" {
		var err error
		before, err = strconv.ParseInt(s, 10, 64)
		if err != nil || before < 1 {
			writeError(w, 400, "invalid history cursor")
			return
		}
	}
	changes, err := h.Queries.ListReportFeedbackChanges(r.Context(), db.ListReportFeedbackChangesParams{WorkspaceID: att.WorkspaceID, AttachmentID: att.ID, BeforeID: before, Limit: 51})
	if err != nil {
		writeError(w, 500, "could not load report history")
		return
	}
	next := ""
	if len(changes) > 50 {
		changes = changes[:50]
		next = strconv.FormatInt(changes[49].ID, 10)
	}
	items := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		items = append(items, map[string]any{"id": strconv.FormatInt(change.ID, 10), "row_id": uuidToString(change.RowID), "kind": change.Kind, "actor_type": change.ActorType,
			"actor_id": uuidToString(change.ActorID), "actor_name": change.ActorName, "before": json.RawMessage(change.BeforeData), "after": json.RawMessage(change.AfterData), "created_at": change.CreatedAt.Time})
	}
	writeJSON(w, 200, map[string]any{"changes": items, "next_cursor": next})
}
