package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestChatAttachmentCapacity(t *testing.T) {
	for _, mode := range []string{"group", "direct"} {
		for _, tc := range []struct {
			name   string
			count  int
			size   int64
			status int
		}{
			{"100_files_at_500_MiB", 100, 5 << 20, http.StatusCreated},
			{"101_empty_files", 101, 0, http.StatusBadRequest},
			{"aggregate_overflow", 6, 100 << 20, http.StatusRequestEntityTooLarge},
			{"single_file_overflow", 1, (100 << 20) + 1, http.StatusRequestEntityTooLarge},
			{"invalid_stored_size", 1, -1, http.StatusRequestEntityTooLarge},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var scope, agent, column, param string
				handler := testHandler.SendTopicMessage
				if mode == "group" {
					_, scope, agent = collaborationFixture(t)
					column, param = "topic_id", "topicId"
				} else {
					agent = createHandlerTestAgent(t, "Batch capacity reviewer", []byte("[]"))
					scope = createHandlerTestChatSession(t, agent)
					column, param = "chat_session_id", "sessionId"
					handler = testHandler.SendChatMessage
					t.Cleanup(func() {
						testPool.Exec(context.Background(), "DELETE FROM chat_message WHERE chat_session_id=$1", scope)
						testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE chat_session_id=$1", scope)
					})
				}
				ids := make([]string, tc.count)
				for i := range ids {
					// Persisted metadata exercises size checks without allocating huge files.
					ids[i] = dbfx.Insert(t, "attachment", testutil.Cols{"workspace_id": testWorkspaceID, column: scope, "uploader_type": "member", "uploader_id": testUserID, "filename": fmt.Sprintf("file-%d.txt", i), "url": fmt.Sprintf("https://example.test/%s/%d", scope, i), "content_type": "text/plain", "size_bytes": tc.size})
				}
				req := testutil.WithURLParams(newRequest("POST", "/", map[string]any{"content": fmt.Sprintf("[@Reviewer](mention://agent/%s) Review this batch", agent), "attachment_ids": ids}), param, scope)
				if mode == "direct" {
					req = withChatTestWorkspaceCtx(t, req)
				}
				result := testutil.Call(t, handler, req).Want(tc.status)
				wantMessages, wantTasks, wantBound := 0, 0, 0
				if tc.status == http.StatusCreated {
					wantMessages, wantTasks, wantBound = 1, 1, tc.count
					if mode == "direct" {
						var sent SendChatMessageResponse
						result.JSON(&sent)
						if len(sent.AttachmentIDs) != tc.count {
							t.Fatalf("response bound %d of %d", len(sent.AttachmentIDs), tc.count)
						}
					}
				}
				for _, check := range []struct {
					query string
					want  int
				}{
					{"SELECT count(*) FROM chat_message WHERE " + column + "=$1", wantMessages},
					{"SELECT count(*) FROM agent_task_queue WHERE " + column + "=$1", wantTasks},
					{"SELECT count(*) FROM attachment WHERE " + column + "=$1 AND chat_message_id IS NOT NULL", wantBound},
				} {
					var got int
					if err := testPool.QueryRow(context.Background(), check.query, scope).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != check.want {
						t.Fatalf("%s: got %d, want %d (send must be atomic)", check.query, got, check.want)
					}
				}
			})
		}
	}
}
