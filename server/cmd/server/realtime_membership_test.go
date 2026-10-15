package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type membershipFailCommitPool struct{ pool *pgxpool.Pool }
type membershipFailCommitTx struct{ pgx.Tx }

func (p membershipFailCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return membershipFailCommitTx{tx}, nil
}

func (membershipFailCommitTx) Commit(context.Context) error {
	return errors.New("injected membership commit failure")
}

func realtimeMembershipConn(t *testing.T, server *httptest.Server, userID, workspaceID string) (*websocket.Conn, string) {
	t.Helper()
	token, err := generateTestJWT(userID, "realtime@example.test", "Realtime")
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Cookie": []string{auth.AuthCookieName + "=" + token}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?workspace_id="+workspaceID, headers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.WriteJSON(map[string]string{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	wantRealtimeMembershipFrame(t, conn, "pong")
	return conn, token
}

func wantRealtimeMembershipFrame(t *testing.T, conn *websocket.Conn, typ string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	var frame struct{ Type string }
	if err != nil || json.Unmarshal(raw, &frame) != nil || frame.Type != typ {
		t.Fatalf("want %s, got %s, err=%v", typ, raw, err)
	}
}

func wantRealtimeMembershipClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatalf("revoked socket stayed open: %v", err)
			}
			return
		}
		var frame struct{ Type string }
		if json.Unmarshal(raw, &frame) != nil || frame.Type != protocol.EventMemberRemoved {
			t.Fatalf("revoked socket received workspace content: %s", raw)
		}
	}
}

func TestRealtimeMemberRevocation(t *testing.T) {
	for _, method := range []string{"delete", "leave"} {
		for _, failCommit := range []bool{false, true} {
			name := method
			if failCommit {
				name += "/rollback"
			}
			t.Run(name, func(t *testing.T) {
				fx := workspaceFixture(t)
				wsID := fx.Workspace(t, "Realtime revocation", "realtime-revocation")
				otherWS := fx.Workspace(t, "Other realtime workspace", "realtime-other")
				userID := fx.User(t, "Leaver", "realtime-leaver@example.test")
				fx.Member(t, wsID, testUserID, "owner")
				memberID := fx.Member(t, wsID, userID, "member")
				fx.Member(t, otherWS, userID, "owner")
				hub := realtime.NewHub()
				go hub.Run()
				bus := events.New()
				registerListeners(bus, hub)
				router, h := NewRouterWithOptions(testPool, hub, bus, analytics.NoopClient{}, nil, RouterOptions{})
				server := httptest.NewServer(router)
				t.Cleanup(server.Close)
				old, token := realtimeMembershipConn(t, server, userID, wsID)
				second, _ := realtimeMembershipConn(t, server, userID, wsID)
				other, _ := realtimeMembershipConn(t, server, userID, otherWS)
				current, _ := realtimeMembershipConn(t, server, testUserID, wsID)
				if failCommit {
					h.TxStarter = membershipFailCommitPool{testPool}
				}
				request := httptest.NewRequest(http.MethodDelete, "/", nil)
				actor := testUserID
				if method == "leave" {
					actor = userID
				}
				request.Header.Set("X-User-ID", actor)
				route := chi.NewRouteContext()
				route.URLParams.Add("id", wsID)
				route.URLParams.Add("memberId", memberID)
				request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
				endpoint := h.DeleteMember
				if method == "leave" {
					endpoint = h.LeaveWorkspace
				}
				status := http.StatusNoContent
				if failCommit {
					status = http.StatusInternalServerError
				}
				testutil.Call(t, endpoint, request).Want(status)
				if failCommit {
					if fx.Count(t, `SELECT count(*) FROM member WHERE id=$1`, memberID) != 1 {
						t.Fatal("failed revocation did not restore membership")
					}
				} else {
					wantRealtimeMembershipClosed(t, old)
					wantRealtimeMembershipClosed(t, second)
					wantRealtimeMembershipFrame(t, current, protocol.EventMemberRemoved)
					fresh, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?workspace_id="+wsID,
						http.Header{"Cookie": []string{auth.AuthCookieName + "=" + token}})
					if fresh != nil {
						fresh.Close()
					}
					if resp != nil && resp.Body != nil {
						defer resp.Body.Close()
					}
					if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
						t.Fatalf("revoked reconnect accepted: response=%v, err=%v", resp, err)
					}
				}
				// Use the real event listeners after the removal API returned.
				for _, typ := range []string{protocol.EventCommentCreated, protocol.EventIssueUpdated} {
					bus.Publish(events.Event{Type: typ, WorkspaceID: wsID, Payload: map[string]string{"body": "new-workspace-content"}})
					wantRealtimeMembershipFrame(t, current, typ)
					if failCommit {
						wantRealtimeMembershipFrame(t, old, typ)
						wantRealtimeMembershipFrame(t, second, typ)
					}
				}
				bus.Publish(events.Event{Type: protocol.EventIssueUpdated, WorkspaceID: otherWS, Payload: map[string]string{"title": "other-workspace"}})
				wantRealtimeMembershipFrame(t, other, protocol.EventIssueUpdated)
			})
		}
	}
}

func TestRealtimeBatchMembershipPairs(t *testing.T) {
	fx := workspaceFixture(t)
	firstWS := fx.Workspace(t, "Batch one", "realtime-batch-one")
	secondWS := fx.Workspace(t, "Batch two", "realtime-batch-two")
	firstUser := fx.User(t, "Batch A", "realtime-batch-a@example.test")
	secondUser := fx.User(t, "Batch B", "realtime-batch-b@example.test")
	fx.Member(t, firstWS, firstUser, "owner")
	fx.Member(t, secondWS, secondUser, "owner")
	checker := &membershipChecker{queries: db.New(testPool)}
	// Both users and both workspaces exist, but cross-pair identities must
	// never inherit another pair's membership through a Cartesian product.
	requested := []realtime.MembershipIdentity{
		{WorkspaceID: firstWS, UserID: secondUser},
		{WorkspaceID: secondWS, UserID: firstUser},
		{WorkspaceID: firstWS, UserID: firstUser},
		{WorkspaceID: secondWS, UserID: secondUser},
		{WorkspaceID: strings.ToUpper(firstWS), UserID: strings.ToUpper(firstUser)},
	}
	members, err := checker.Members(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	for i, identity := range requested {
		if members[identity] != (i >= 2) {
			t.Fatalf("incorrect membership for pair %+v", identity)
		}
	}
}

func TestRealtimeBatchTopicMembershipPairs(t *testing.T) {
	fx := workspaceFixture(t)
	sourceWS := fx.Workspace(t, "Topic source", "realtime-topic-source")
	otherWS := fx.Workspace(t, "Other topic workspace", "realtime-topic-other")
	fx.Member(t, sourceWS, testUserID, "owner")
	fx.Member(t, otherWS, testUserID, "owner")
	groups := make([]string, 2)
	for i := range groups {
		groups[i] = fx.Insert(t, "conversation", testutil.Cols{
			"workspace_id": sourceWS, "kind": "group", "created_by": testUserID,
		})
		fx.InsertNoID(t, "conversation_participant", testutil.Cols{
			"conversation_id": groups[i], "principal_type": "member", "principal_id": testUserID, "role": "member",
		}, "conversation_id=$1", groups[i])
	}
	topic := fx.Insert(t, "topic", testutil.Cols{
		"conversation_id": groups[0], "created_by_type": "member", "created_by_id": testUserID,
	})
	requested := []realtime.MembershipIdentity{
		{WorkspaceID: sourceWS, UserID: testUserID, TopicID: topic},
		{WorkspaceID: sourceWS, UserID: testUserID, ConversationID: groups[0], TopicID: topic},
		{WorkspaceID: strings.ToUpper(sourceWS), UserID: strings.ToUpper(testUserID), TopicID: strings.ToUpper(topic)},
		{WorkspaceID: sourceWS, UserID: testUserID, ConversationID: groups[1], TopicID: topic},
		{WorkspaceID: otherWS, UserID: testUserID, TopicID: topic},
		{WorkspaceID: sourceWS, UserID: testUserID, TopicID: "00000000-0000-4000-8000-000000000001"},
	}
	checker := &membershipChecker{queries: db.New(testPool)}
	members, err := checker.Members(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	for i, identity := range requested {
		if members[identity] != (i < 3) {
			t.Fatalf("incorrect topic/group/workspace pairing: %+v", identity)
		}
	}
}

// User-scoped group frames can arrive on a different workspace's socket. A
// delayed relay frame must be authorized against its source, not that socket.
func TestRealtimeGroupFrameRevocationAcrossWorkspaces(t *testing.T) {
	for _, revoke := range []string{"workspace", "group", "deleted_group", "deleted_topic", "wrong_workspace"} {
		t.Run(revoke, func(t *testing.T) {
			fx := workspaceFixture(t)
			sourceWS := fx.Workspace(t, "Group source", "realtime-group-source")
			openWS := fx.Workspace(t, "Open workspace", "realtime-group-open")
			fx.Member(t, sourceWS, testUserID, "owner")
			fx.Member(t, openWS, testUserID, "owner")
			conversation := fx.Insert(t, "conversation", testutil.Cols{
				"workspace_id": sourceWS, "kind": "group", "title": "Private group", "created_by": testUserID,
			})
			fx.InsertNoID(t, "conversation_participant", testutil.Cols{
				"conversation_id": conversation, "principal_type": "member", "principal_id": testUserID, "role": "member",
			}, "conversation_id=$1", conversation)
			topic := fx.Insert(t, "topic", testutil.Cols{
				"conversation_id": conversation, "created_by_type": "member", "created_by_id": testUserID,
			})
			hub := realtime.NewHub()
			go hub.Run()
			bus := events.New()
			registerListeners(bus, hub)
			router, _ := NewRouterWithOptions(testPool, hub, bus, analytics.NoopClient{}, nil, RouterOptions{})
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			conn, _ := realtimeMembershipConn(t, server, testUserID, openWS)
			bus.Publish(events.Event{Type: protocol.EventMessageSent, WorkspaceID: sourceWS, Payload: protocol.MessageSentPayload{
				ConversationID: conversation, Content: "authorized", RecipientUserIDs: []string{testUserID},
			}})
			wantRealtimeMembershipFrame(t, conn, protocol.EventMessageSent)
			// Task lifecycle events contain topic_id without conversation_id,
			// and dispatch can include the full private prompt context.
			bus.Publish(events.Event{Type: protocol.EventTaskDispatch, WorkspaceID: sourceWS,
				RecipientUserIDs: []string{testUserID}, Payload: map[string]string{
					"topic_id": topic, "workspace_id": sourceWS, "prompt": "authorized task context",
				}})
			wantRealtimeMembershipFrame(t, conn, protocol.EventTaskDispatch)
			switch revoke {
			case "workspace":
				fx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", sourceWS, testUserID)
			case "group":
				fx.Exec(t, "UPDATE conversation_participant SET left_at=now() WHERE conversation_id=$1", conversation)
			case "deleted_group":
				fx.Exec(t, "DELETE FROM conversation WHERE id=$1", conversation)
			case "deleted_topic":
				fx.Exec(t, "DELETE FROM topic WHERE id=$1", topic)
			case "wrong_workspace":
				sourceWS = openWS
			}
			for _, typ := range []string{protocol.EventMessageSent, protocol.EventMessageDone, protocol.EventTopicUpdated} {
				if revoke == "deleted_topic" {
					// The group remains readable; only its deleted topic is gone.
					continue
				}
				frame, _ := json.Marshal(map[string]any{"type": typ, "payload": map[string]string{
					"workspace_id": sourceWS, "conversation_id": conversation, "content": "retained private content",
				}})
				hub.SendToUser(testUserID, frame)
			}
			for _, typ := range []string{
				protocol.EventTaskQueued, protocol.EventTaskDispatch, protocol.EventTaskRunning,
				protocol.EventTaskWaitingLocalDirectory, protocol.EventTaskProgress, protocol.EventTaskCompleted,
				protocol.EventTaskFailed, protocol.EventTaskMessage, protocol.EventTaskCancelled,
			} {
				// Use the captured recipient set to model delayed event delivery.
				bus.Publish(events.Event{Type: typ, WorkspaceID: sourceWS,
					RecipientUserIDs: []string{testUserID}, Payload: map[string]string{
						"topic_id": topic, "workspace_id": sourceWS, "prompt": "retained private task context",
					}})
			}
			// A marker behind the retained frames verifies both that they were
			// dropped and that revocation did not close the unrelated socket.
			hub.BroadcastToWorkspace(openWS, []byte(`{"type":"issue:updated"}`))
			wantRealtimeMembershipFrame(t, conn, protocol.EventIssueUpdated)
			if revoke == "group" {
				// Cleanup remains useful after losing group participation, but
				// must not carry any content through its exception.
				frame, _ := json.Marshal(map[string]any{"type": protocol.EventParticipantRemoved, "payload": map[string]string{
					"workspace_id": sourceWS, "conversation_id": conversation,
					"principal_type": "member", "principal_id": testUserID, "content": "private-removal-body",
				}})
				hub.SendToUser(testUserID, frame)
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, raw, err := conn.ReadMessage()
				if err != nil || !strings.Contains(string(raw), protocol.EventParticipantRemoved) || strings.Contains(string(raw), "private-removal-body") {
					t.Fatalf("unsafe cleanup signal: %s, err=%v", raw, err)
				}
			}
		})
	}
}
