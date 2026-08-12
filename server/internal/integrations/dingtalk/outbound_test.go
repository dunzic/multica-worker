package dingtalk

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type outboundTestQueries struct {
	task    db.AgentTaskQueue
	binding db.ChannelChatSessionBinding
	inst    db.ChannelInstallation
}

func (q *outboundTestQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}

func (q *outboundTestQueries) TaskHasChannelIngestedMessages(context.Context, pgtype.UUID) (bool, error) {
	return true, nil
}

func (q *outboundTestQueries) GetChannelChatSessionBindingBySession(context.Context, db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	return q.binding, nil
}

func (q *outboundTestQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return q.inst, nil
}

type outboundDeliveryRecorder struct {
	input     delivery.ClaimInput
	delivered int
}

func (r *outboundDeliveryRecorder) Claim(_ context.Context, input delivery.ClaimInput) (delivery.Claim, error) {
	r.input = input
	return delivery.Claim{ShouldSend: true, Row: db.ChannelDelivery{
		ID: dingtalkTestUUID(20), WorkspaceID: input.WorkspaceID, InstallationID: input.InstallationID,
		TaskID: input.TaskID, ChatSessionID: input.ChatSessionID, ChannelType: string(input.ChannelType),
		ChannelChatID: input.ChannelChatID, OperationKind: input.OperationKind, CorrelationID: dingtalkTestUUID(21),
		PayloadDigest: delivery.PayloadDigest(input.Payload), Status: "pending", AttemptCount: 1, LeaseToken: dingtalkTestUUID(22),
	}}, nil
}

func (r *outboundDeliveryRecorder) MarkDelivered(_ context.Context, claim delivery.Claim, messageID string) (db.ChannelDelivery, error) {
	r.delivered++
	claim.Row.ExternalMessageID = pgtype.Text{String: messageID, Valid: true}
	return claim.Row, nil
}

func (r *outboundDeliveryRecorder) MarkFailed(context.Context, delivery.Claim, string) error {
	return nil
}

func dingtalkTestUUID(n byte) pgtype.UUID {
	var id pgtype.UUID
	id.Bytes[15] = n
	id.Valid = true
	return id
}

type dingtalkRoundTripFunc func(*http.Request) (*http.Response, error)

func (f dingtalkRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEventContent(t *testing.T) {
	cases := []struct {
		name  string
		event events.Event
		want  string
	}{
		{"chat done typed", events.Event{Type: protocol.EventChatDone, Payload: protocol.ChatDonePayload{Content: "reply"}}, "reply"},
		{"map round trip", events.Event{Type: protocol.EventChatDone, Payload: map[string]any{"content": "from map"}}, "from map"},
		{"empty map", events.Event{Type: protocol.EventChatDone, Payload: map[string]any{}}, ""},
		{"nil", events.Event{Type: protocol.EventChatDone}, ""},
		{
			"task failed with error",
			events.Event{Type: protocol.EventTaskFailed, Payload: map[string]any{"error": "task timed out", "retry_pending": false}},
			"⚠️ task timed out",
		},
		{
			// Retry-pending failures stay silent even if a mixed-version
			// publisher accidentally includes an error string.
			"task failed with retry pending",
			events.Event{Type: protocol.EventTaskFailed, Payload: map[string]any{"error": "task timed out", "failure_reason": "timeout", "retry_pending": true}},
			"",
		},
		{
			// Failure broadcasts without an error text have nothing safe to
			// deliver and stay silent.
			"task failed without error",
			events.Event{Type: protocol.EventTaskFailed, Payload: map[string]any{"failure_reason": "timeout", "retry_pending": false}},
			"",
		},
		{
			// task:failed payloads never carry "content"; it must not leak
			// through the chat-done branch.
			"task failed ignores content key",
			events.Event{Type: protocol.EventTaskFailed, Payload: map[string]any{"content": "not for delivery"}},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventContent(tc.event); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutboundUsesSharedDeliveryContract(t *testing.T) {
	client := &http.Client{Transport: dingtalkRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"processQueryKey":"ding-message-1"}`
		if r.URL.Path == accessTokenPath {
			body = `{"accessToken":"token","expireIn":7200}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	secret := base64.StdEncoding.EncodeToString([]byte("secret"))
	taskID := dingtalkTestUUID(3)
	sessionID := dingtalkTestUUID(4)
	q := &outboundTestQueries{
		task:    db.AgentTaskQueue{ID: taskID, ChatInputTaskID: dingtalkTestUUID(5)},
		binding: db.ChannelChatSessionBinding{InstallationID: dingtalkTestUUID(2), ChannelChatID: "cid-1"},
		inst: db.ChannelInstallation{ID: dingtalkTestUUID(2), WorkspaceID: dingtalkTestUUID(1), Status: "active",
			Config: []byte(`{"app_id":"app","robot_code":"robot","app_secret_encrypted":"` + secret + `"}`)},
	}
	recorder := &outboundDeliveryRecorder{}
	outbound := NewOutbound(q, nil, NewClient(client, "https://dingtalk.test"), nil).WithDeliveryRecorder(recorder)
	err := outbound.processEvent(context.Background(), events.Event{
		Type: protocol.EventChatDone, TaskID: "00000000-0000-0000-0000-000000000003", ChatSessionID: "00000000-0000-0000-0000-000000000004",
		Payload: protocol.ChatDonePayload{TaskID: "00000000-0000-0000-0000-000000000003", ChatSessionID: "00000000-0000-0000-0000-000000000004", Content: "audited ding answer"},
	})
	if err != nil {
		t.Fatalf("process event: %v", err)
	}
	if recorder.delivered != 1 || recorder.input.ChannelType != TypeDingTalk || recorder.input.OperationKind != delivery.OperationChatReply {
		t.Fatalf("delivery contract = delivered %d input %+v", recorder.delivered, recorder.input)
	}
	if recorder.input.Payload != "audited ding answer" || recorder.input.TaskID != taskID || recorder.input.ChatSessionID != sessionID {
		t.Fatalf("delivery identity = %+v", recorder.input)
	}
}
