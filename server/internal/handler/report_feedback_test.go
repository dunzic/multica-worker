package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/reportfeedback"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func reportFixture(t *testing.T, extra testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{"workspace_id": testWorkspaceID, "uploader_type": "member", "uploader_id": testUserID, "filename": "quality-report.html", "url": "reports/quality.html", "content_type": "text/html", "size_bytes": 20}
	for key, value := range extra {
		cols[key] = value
	}
	id := dbfx.Insert(t, "attachment", cols)
	t.Cleanup(func() {
		if err := testHandler.Queries.DeleteReportFeedbackForAttachment(context.Background(), db.DeleteReportFeedbackForAttachmentParams{WorkspaceID: parseUUID(testWorkspaceID), AttachmentID: parseUUID(id)}); err != nil {
			t.Error(err)
		}
	})
	return id
}

func reportRequest(method, id, row string, body any) *http.Request {
	return testutil.WithURLParams(newRequest(method, "/api/attachments/"+id+"/report-feedback", body), "id", id, "rowID", row)
}

func reportFields() []reportfeedback.Field {
	return []reportfeedback.Field{
		{ID: "status", Label: "Status", Type: "select", Options: []string{"pending", "done"}},
		{ID: "note", Label: "Feedback", Type: "text"},
		{ID: "score", Label: "Score", Type: "number"},
		{ID: "approved", Label: "Approved", Type: "boolean"},
		{ID: "date", Label: "Date", Type: "date"},
		{ID: "tags", Label: "Tags", Type: "multi_select", Options: []string{"first", "second"}},
	}
}

func configureReport(t *testing.T, id string) {
	t.Helper()
	testutil.Call(t, testHandler.PutReportFeedbackSchema, reportRequest("PUT", id, "", map[string]any{"expected_revision": 0, "fields": reportFields()})).Want(200)
}

func reportRowBody(revision int, note string) map[string]any {
	return map[string]any{"expected_revision": revision, "schema_revision": 1, "label": "Item A", "source_attachment_id": "", "values": map[string]any{"status": "pending", "note": note, "score": 0, "approved": false, "date": "2026-10-17", "tags": []string{"first", "second"}}}
}

func TestReportFeedback_SaveReadAuditAndConflict(t *testing.T) {
	id := reportFixture(t, nil)
	configureReport(t, id)
	row := uuid.NewString()
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(0, "original"))).Want(200)
	var first map[string]any
	testutil.Call(t, testHandler.GetReportFeedback, reportRequest("GET", id, "", nil)).Want(200).JSON(&first)
	if first["schema_revision"] != float64(1) || len(first["rows"].([]any)) != 1 {
		t.Fatalf("readback=%v", first)
	}
	user := dbfx.User(t, "Collaborator", "report-"+uuid.NewString()+"@test.local")
	dbfx.Member(t, testWorkspaceID, user, "member")
	req := reportRequest("PUT", id, row, reportRowBody(1, "human feedback"))
	req.Header.Set("X-User-ID", user)
	testutil.Call(t, testHandler.PutReportFeedbackRow, req).Want(200)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(1, "stale"))).Want(409)
	var history struct {
		Changes []struct {
			Kind    string
			ActorID string `json:"actor_id"`
			Before  map[string]any
			After   map[string]any
		}
	}
	testutil.Call(t, testHandler.ListReportFeedbackHistory, reportRequest("GET", id, "", nil)).Want(200).JSON(&history)
	if len(history.Changes) != 3 || history.Changes[0].ActorID != user || history.Changes[0].Kind != "update" {
		t.Fatalf("audit=%+v", history)
	}
	if history.Changes[0].Before["values"].(map[string]any)["note"] != "original" || history.Changes[0].After["values"].(map[string]any)["note"] != "human feedback" {
		t.Fatal("audit lost old or new value")
	}
	var rows struct {
		Rows []struct {
			Values   map[string]any
			Revision int
		}
	}
	testutil.Call(t, testHandler.GetReportFeedback, reportRequest("GET", id, "", nil)).Want(200).JSON(&rows)
	if rows.Rows[0].Values["score"] != float64(0) || rows.Rows[0].Values["approved"] != false || rows.Rows[0].Revision != 2 {
		t.Fatalf("typed readback=%+v", rows)
	}
	testutil.Call(t, testHandler.ArchiveReportFeedbackRow, reportRequest("POST", id, row, map[string]any{"expected_revision": 2})).Want(200)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(3, "resurrect"))).Want(409)
	var archived struct {
		Rows       []any
		HasRecords bool `json:"has_records"`
	}
	testutil.Call(t, testHandler.GetReportFeedback, reportRequest("GET", id, "", nil)).Want(200).JSON(&archived)
	if len(archived.Rows) != 0 || !archived.HasRecords {
		t.Fatal("archive must preserve schema protection and history")
	}
}

func TestReportFeedback_ConcurrentWriters(t *testing.T) {
	id := reportFixture(t, nil)
	configureReport(t, id)
	row := uuid.NewString()
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(0, "base"))).Want(200)
	responses := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			testHandler.PutReportFeedbackRow(responses[i], reportRequest("PUT", id, row, reportRowBody(1, fmt.Sprintf("writer %d", i))))
		}(i)
	}
	group.Wait()
	if !((responses[0].Code == 200 && responses[1].Code == 409) || (responses[0].Code == 409 && responses[1].Code == 200)) {
		t.Fatalf("statuses=%d,%d", responses[0].Code, responses[1].Code)
	}
	var changes int
	dbfx.QueryRow(t, "SELECT count(*) FROM report_feedback_change WHERE attachment_id=$1", id).Scan(&changes)
	if changes != 3 {
		t.Fatalf("conflicted write produced audit: %d", changes)
	}
	// Independent rows use independent revisions; no report-wide lost updates.
	for i := 0; i < 2; i++ {
		testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, uuid.NewString(), reportRowBody(0, "independent"))).Want(200)
	}
}

type reportFailAuditStarter struct{ txStarter }
type reportFailAuditTx struct{ pgx.Tx }

func (s reportFailAuditStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return reportFailAuditTx{tx}, nil
}
func (tx reportFailAuditTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "-- name: CreateReportFeedbackChange") {
		return pgconn.CommandTag{}, errors.New("injected audit failure")
	}
	return tx.Tx.Exec(ctx, query, args...)
}

func TestReportFeedback_AuditFailureRollsBackValues(t *testing.T) {
	id := reportFixture(t, nil)
	configureReport(t, id)
	row := uuid.NewString()
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(0, "before"))).Want(200)
	h := *testHandler
	h.TxStarter = reportFailAuditStarter{testHandler.TxStarter}
	testutil.Call(t, h.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(1, "should roll back"))).Want(500)
	testutil.Call(t, h.PutReportFeedbackRow, reportRequest("PUT", id, uuid.NewString(), reportRowBody(0, "should not exist"))).Want(500)
	stored, err := testHandler.Queries.GetReportFeedbackRow(context.Background(), db.GetReportFeedbackRowParams{WorkspaceID: parseUUID(testWorkspaceID), AttachmentID: parseUUID(id), ID: parseUUID(row)})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != 1 || !strings.Contains(string(stored.Values), "before") {
		t.Fatalf("partial update survived audit failure: %+v", stored)
	}
	var rows, changes int
	dbfx.QueryRow(t, "SELECT count(*) FROM report_feedback_row WHERE attachment_id=$1", id).Scan(&rows)
	dbfx.QueryRow(t, "SELECT count(*) FROM report_feedback_change WHERE attachment_id=$1", id).Scan(&changes)
	if rows != 1 || changes != 2 {
		t.Fatalf("partial writes rows=%d changes=%d", rows, changes)
	}
}

func TestReportFeedback_Pagination(t *testing.T) {
	id := reportFixture(t, nil)
	configureReport(t, id)
	for i := 0; i < 51; i++ {
		testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, uuid.NewString(), reportRowBody(0, fmt.Sprint(i)))).Want(200)
	}
	var first, second struct {
		Rows []map[string]any
		Next string `json:"next_cursor"`
	}
	testutil.Call(t, testHandler.GetReportFeedback, reportRequest("GET", id, "", nil)).Want(200).JSON(&first)
	if len(first.Rows) != 50 || first.Next == "" {
		t.Fatalf("first page=%+v", first)
	}
	req := reportRequest("GET", id, "", nil)
	req.URL.RawQuery = "after=" + first.Next
	testutil.Call(t, testHandler.GetReportFeedback, req).Want(200).JSON(&second)
	if len(second.Rows) != 1 || second.Next != "" {
		t.Fatalf("second page=%+v", second)
	}
	seen := map[string]bool{}
	for _, r := range first.Rows {
		seen[r["id"].(string)] = true
	}
	if seen[second.Rows[0]["id"].(string)] {
		t.Fatal("duplicate paginated row")
	}
	var history struct {
		Changes []map[string]any
		Next    string `json:"next_cursor"`
	}
	testutil.Call(t, testHandler.ListReportFeedbackHistory, reportRequest("GET", id, "", nil)).Want(200).JSON(&history)
	if len(history.Changes) != 50 || history.Next == "" {
		t.Fatal("unbounded history page")
	}
	req = reportRequest("GET", id, "", nil)
	req.URL.RawQuery = "before=" + history.Next
	testutil.Call(t, testHandler.ListReportFeedbackHistory, req).Want(200).JSON(&history)
	if len(history.Changes) != 2 || history.Next != "" {
		t.Fatal("history cursor skipped records")
	}
}

func TestReportFeedback_ValidationAndSchemaEvolution(t *testing.T) {
	id := reportFixture(t, nil)
	configureReport(t, id)
	row := uuid.NewString()
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, "bad-id", reportRowBody(0, "bad"))).Want(400)
	invalid := reportRowBody(0, "bad")
	invalid["actor_id"] = testUserID
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, invalid)).Want(400)
	invalid = reportRowBody(0, "bad")
	invalid["values"] = map[string]any{"score": "100"}
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, invalid)).Want(400)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(0, "valid"))).Want(200)
	testutil.Call(t, testHandler.PutReportFeedbackSchema, reportRequest("PUT", id, "", map[string]any{"expected_revision": 1, "fields": reportFields()[1:]})).Want(400)
	fields := append(reportFields(), reportfeedback.Field{ID: "extra", Label: "Extra", Type: "text"})
	testutil.Call(t, testHandler.PutReportFeedbackSchema, reportRequest("PUT", id, "", map[string]any{"expected_revision": 1, "fields": fields})).Want(200)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(1, "old schema"))).Want(409)
	invalid = reportRowBody(1, "fresh schema")
	invalid["schema_revision"] = 2
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, invalid)).Want(200)
	for len(fields) < 21 {
		fields = append(fields, reportfeedback.Field{ID: fmt.Sprintf("field_%d", len(fields)), Label: "Extra", Type: "text"})
	}
	testutil.Call(t, testHandler.PutReportFeedbackSchema, reportRequest("PUT", id, "", map[string]any{"expected_revision": 2, "fields": fields})).Want(400)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM report_feedback_change WHERE attachment_id=$1", id).Scan(&count)
	if count != 4 {
		t.Fatalf("invalid operations persisted: %d", count)
	}
}

func TestReportFeedback_GroupAndWorkspaceIsolation(t *testing.T) {
	conv, topic, _ := collaborationFixture(t)
	id := reportFixture(t, testutil.Cols{"topic_id": topic})
	configureReport(t, id)
	user := dbfx.User(t, "Reader", "reader-"+uuid.NewString()+"@test.local")
	member := dbfx.Member(t, testWorkspaceID, user, "member")
	check := func(want int) {
		t.Helper()
		for _, h := range []http.HandlerFunc{testHandler.GetReportFeedback, testHandler.ListReportFeedbackHistory, testHandler.PutReportFeedbackSchema, testHandler.PutReportFeedbackRow, testHandler.ArchiveReportFeedbackRow} {
			req := reportRequest("GET", id, uuid.NewString(), nil)
			req.Header.Set("X-User-ID", user)
			testutil.Call(t, h, req).Want(want)
		}
	}
	check(404)
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": conv, "principal_type": "member", "principal_id": user}, "conversation_id=$1 AND principal_id=$2", conv, user)
	req := reportRequest("GET", id, "", nil)
	req.Header.Set("X-User-ID", user)
	var page map[string]any
	testutil.Call(t, testHandler.GetReportFeedback, req).Want(200).JSON(&page)
	if page["can_manage"] != false || page["can_edit"] != true {
		t.Fatalf("permissions=%v", page)
	}
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conv, user)
	check(404)
	dbfx.Exec(t, "UPDATE conversation_participant SET left_at=NULL WHERE conversation_id=$1 AND principal_id=$2", conv, user)
	dbfx.Exec(t, "DELETE FROM member WHERE id=$1", member)
	check(404)
	other := dbfx.Workspace(t, "Other", "report-"+uuid.NewString())
	dbfx.Member(t, other, testUserID, "admin")
	cross := reportRequest("GET", id, "", nil)
	cross.Header.Set("X-Workspace-ID", other)
	testutil.Call(t, testHandler.GetReportFeedback, cross).Want(404)
}

func TestReportFeedback_SourceScope(t *testing.T) {
	_, topic, _ := collaborationFixture(t)
	source := reportFixture(t, testutil.Cols{"topic_id": topic})
	broad := reportFixture(t, nil)
	configureReport(t, broad)
	body := reportRowBody(0, "private source")
	body["source_attachment_id"] = source
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", broad, uuid.NewString(), body)).Want(400)
	same := reportFixture(t, testutil.Cols{"topic_id": topic})
	configureReport(t, same)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", same, uuid.NewString(), body)).Want(200)
	otherConv := dbfx.Insert(t, "conversation", testutil.Cols{"workspace_id": testWorkspaceID, "kind": "group", "title": "Other report group", "created_by": testUserID})
	dbfx.InsertNoID(t, "conversation_participant", testutil.Cols{"conversation_id": otherConv, "principal_type": "member", "principal_id": testUserID, "role": "admin"}, "conversation_id=$1", otherConv)
	otherTopic := dbfx.Insert(t, "topic", testutil.Cols{"conversation_id": otherConv, "created_by_type": "member", "created_by_id": testUserID})
	other := reportFixture(t, testutil.Cols{"topic_id": otherTopic})
	configureReport(t, other)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", other, uuid.NewString(), body)).Want(400)
}

func TestReportFeedback_TaskCredentialCannotOverwriteHumanData(t *testing.T) {
	_, topic, agent := collaborationFixture(t)
	task := dbfx.Task(t, agent, testutil.Cols{"topic_id": topic, "runtime_id": handlerTestRuntimeID(t)})
	secret := "mat_report_" + task
	dbfx.Insert(t, "task_token", testutil.Cols{"token_hash": auth.HashToken(secret), "task_id": task, "agent_id": agent, "workspace_id": testWorkspaceID, "user_id": testUserID, "expires_at": time.Now().Add(time.Hour)})
	id := reportFixture(t, testutil.Cols{"topic_id": topic, "task_id": task, "uploader_type": "agent", "uploader_id": agent})
	router := chi.NewRouter()
	authenticate := middleware.Auth(testHandler.Queries, nil, nil, nil)
	router.With(authenticate).Put("/api/attachments/{id}/report-feedback/schema", testHandler.PutReportFeedbackSchema)
	router.With(authenticate).Put("/api/attachments/{id}/report-feedback/rows/{rowID}", testHandler.PutReportFeedbackRow)
	call := func(path string, body any, want int) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("PUT", "/api/attachments/"+id+"/report-feedback/"+path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		testutil.Call(t, router.ServeHTTP, req).Want(want)
	}
	call("schema", map[string]any{"expected_revision": 0, "fields": reportFields()}, 200)
	row := uuid.NewString()
	call("rows/"+row, reportRowBody(0, "seed only"), 200)
	testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, row, reportRowBody(1, "human decision"))).Want(200)
	call("rows/"+row, reportRowBody(2, "overwrite human"), 403)
	call("schema", map[string]any{"expected_revision": 1, "fields": reportFields()}, 403)
	foreign := reportFixture(t, testutil.Cols{"topic_id": topic})
	id = foreign
	call("schema", map[string]any{"expected_revision": 0, "fields": reportFields()}, 403)
}

func TestReportFeedback_RemovalWhileWaitingToWrite(t *testing.T) {
	conv, topic, _ := collaborationFixture(t)
	id := reportFixture(t, testutil.Cols{"topic_id": topic})
	configureReport(t, id)
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM conversation WHERE id=$1 FOR UPDATE", conv); err != nil {
		t.Fatal(err)
	}
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		testHandler.PutReportFeedbackRow(out, reportRequest("PUT", id, uuid.NewString(), reportRowBody(0, "late save")))
		close(done)
	}()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err = testPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM conversation WHERE id=%FOR UPDATE%')").Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("write did not reach conversation lock")
	}
	if _, err = tx.Exec(ctx, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1 AND principal_id=$2", conv, testUserID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("write remained blocked")
	}
	if out.Code != 404 {
		t.Fatalf("revoked write=%d %s", out.Code, out.Body.String())
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM report_feedback_row WHERE attachment_id=$1", id).Scan(&count)
	if count != 0 {
		t.Fatal("revoked write persisted")
	}
}

func TestReportFeedback_AttachmentDeletionCleanup(t *testing.T) {
	for _, kind := range []string{"attachment", "issue_comment", "comment", "chat_session", "user_message", "system_agent", "runtime_agents"} {
		t.Run(kind, func(t *testing.T) {
			cols := testutil.Cols{}
			issue, comment, session := "", "", ""
			agent, runtime, task := "", "", ""
			if kind == "issue_comment" || kind == "comment" {
				issue = dbfx.Issue(t, "Report owner")
				comment = dbfx.Comment(t, issue, "Report")
				cols["comment_id"] = comment
			}
			if kind == "chat_session" || kind == "user_message" || kind == "system_agent" || kind == "runtime_agents" {
				runtime = dbfx.Runtime(t, "Report cleanup runtime")
				agentCols := testutil.Cols{}
				if kind == "system_agent" || kind == "runtime_agents" {
					agentCols["kind"], agentCols["system_key"] = "system", "agent_builder:"+uuid.NewString()
				}
				agent = dbfx.Agent(t, "Report carrier", runtime, agentCols)
				session = dbfx.ChatSession(t, agent)
				cols["chat_session_id"] = session
			}
			if kind == "user_message" {
				task = dbfx.Task(t, agent, testutil.Cols{"chat_session_id": session, "runtime_id": runtime})
				message := dbfx.Insert(t, "chat_message", testutil.Cols{"chat_session_id": session, "role": "user", "content": "Report", "task_id": task})
				delete(cols, "chat_session_id")
				cols["chat_message_id"] = message
			}
			id := reportFixture(t, cols)
			configureReport(t, id)
			testutil.Call(t, testHandler.PutReportFeedbackRow, reportRequest("PUT", id, uuid.NewString(), reportRowBody(0, "owned"))).Want(200)
			var err error
			switch kind {
			case "attachment":
				testutil.Call(t, testHandler.DeleteAttachment, reportRequest("DELETE", id, "", nil)).Want(204)
			case "issue_comment":
				err = testHandler.Queries.DeleteIssue(context.Background(), db.DeleteIssueParams{ID: parseUUID(issue), WorkspaceID: parseUUID(testWorkspaceID)})
			case "comment":
				_, err = testHandler.Queries.DeleteCommentAttachments(context.Background(), db.DeleteCommentAttachmentsParams{CommentID: parseUUID(comment), WorkspaceID: parseUUID(testWorkspaceID)})
			case "chat_session":
				err = testHandler.Queries.DeleteChatSession(context.Background(), db.DeleteChatSessionParams{ID: parseUUID(session), WorkspaceID: parseUUID(testWorkspaceID)})
			case "user_message":
				_, err = testHandler.Queries.DeleteUserChatMessageByTask(context.Background(), parseUUID(task))
			case "system_agent":
				err = testHandler.Queries.DeleteSystemAgentByID(context.Background(), parseUUID(agent))
			case "runtime_agents":
				err = testHandler.Queries.DeleteSystemAgentsByRuntime(context.Background(), parseUUID(runtime))
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"report_feedback_schema", "report_feedback_row", "report_feedback_change"} {
				var count int
				dbfx.QueryRow(t, "SELECT count(*) FROM "+table+" WHERE attachment_id=$1", id).Scan(&count)
				if count != 0 {
					t.Fatalf("%s left %d rows in %s", kind, count, table)
				}
			}
		})
	}
}
