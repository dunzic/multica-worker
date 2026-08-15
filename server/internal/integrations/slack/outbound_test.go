package slack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func uid(b byte) pgtype.UUID {
	var u pgtype.UUID
	u.Bytes[0] = b
	u.Valid = true
	return u
}

type fakeOutboundQueries struct {
	binding             db.ChannelChatSessionBinding
	bindingErr          error
	inst                db.ChannelInstallation
	instErr             error
	task                db.AgentTaskQueue
	taskErr             error
	taskChannelIngested bool
}

func (f *fakeOutboundQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return f.task, f.taskErr
}

func (f *fakeOutboundQueries) TaskHasChannelIngestedMessages(context.Context, pgtype.UUID) (bool, error) {
	return f.taskChannelIngested, nil
}

func (f *fakeOutboundQueries) GetChannelChatSessionBindingBySession(context.Context, db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	return f.binding, f.bindingErr
}

func (f *fakeOutboundQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return f.inst, f.instErr
}

type fakeSender struct {
	called int
	got    channel.OutboundMessage
}

type fakeDeliveryRecorder struct {
	input           delivery.ClaimInput
	claimed         int
	delivered       int
	failed          int
	ambiguous       int
	failureCode     string
	ambiguityReason string
}

func (f *fakeDeliveryRecorder) Claim(_ context.Context, input delivery.ClaimInput) (delivery.Claim, error) {
	f.input = input
	f.claimed++
	return delivery.Claim{ShouldSend: true, Row: db.ChannelDelivery{
		ID: uid(20), WorkspaceID: input.WorkspaceID, InstallationID: input.InstallationID,
		TaskID: input.TaskID, ChatSessionID: input.ChatSessionID, ChannelType: string(input.ChannelType),
		ChannelChatID: input.ChannelChatID, OperationKind: input.OperationKind, CorrelationID: uid(21),
		PayloadDigest: delivery.PayloadDigest(input.Payload), Status: "pending", AttemptCount: 1, LeaseToken: uid(22),
	}}, nil
}

func (f *fakeDeliveryRecorder) MarkDelivered(_ context.Context, claim delivery.Claim, messageID string) (db.ChannelDelivery, error) {
	f.delivered++
	claim.Row.Status = "delivered"
	claim.Row.ExternalMessageID = pgtype.Text{String: messageID, Valid: true}
	return claim.Row, nil
}

func (f *fakeDeliveryRecorder) MarkFailed(_ context.Context, _ delivery.Claim, code string) error {
	f.failed++
	f.failureCode = code
	return nil
}

func (f *fakeDeliveryRecorder) MarkAmbiguous(_ context.Context, claim delivery.Claim, reason string, messageID string) (db.ChannelDelivery, error) {
	f.ambiguous++
	f.ambiguityReason = reason
	claim.Row.Status = "ambiguous"
	if messageID != "" {
		claim.Row.ExternalMessageID = pgtype.Text{String: messageID, Valid: true}
	}
	return claim.Row, nil
}

func (f *fakeSender) Send(_ context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	f.called++
	f.got = out
	return channel.SendResult{MessageID: "1.1"}, nil
}

// slackInstallConfigJSON builds an installation config blob with base64 tokens
// (a nil Decrypter treats the decoded bytes as plaintext).
func slackInstallConfigJSON() []byte {
	b, _ := json.Marshal(map[string]string{
		"app_id":              "T1",
		"bot_user_id":         "UBOT",
		"bot_token_encrypted": base64.StdEncoding.EncodeToString([]byte("xoxb-test")),
	})
	return b
}

func newTestOutbound(q outboundQueries, fs *fakeSender) *Outbound {
	o := NewOutbound(q, nil, nil)
	o.newSender = func(credentials) replySender { return fs }
	return o
}

func chatDoneEvent(sessionID string, content string) events.Event {
	return events.Event{
		Type:          protocol.EventChatDone,
		ChatSessionID: sessionID,
		Payload: protocol.ChatDonePayload{
			TaskID:        "00000000-0000-0000-0000-000000000002",
			ChatSessionID: sessionID,
			Content:       content,
		},
	}
}

func TestOutbound_SkipsDirectChatTaskOnBoundSlackSession(t *testing.T) {
	q := &fakeOutboundQueries{
		task: db.AgentTaskQueue{ChatInputTaskID: uid(2)},
		binding: db.ChannelChatSessionBinding{
			InstallationID: uid(1),
			ChannelChatID:  "C123",
			Config:         []byte(`{"channel_id":"C123"}`),
		},
		inst: db.ChannelInstallation{ID: uid(1), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}

	newTestOutbound(q, fs).handleEvent(chatDoneEvent("00000000-0000-0000-0000-000000000001", "private web reply"))

	if fs.called != 0 {
		t.Fatalf("sender called %d times, want 0 for a direct-chat task", fs.called)
	}
}

// A sealed channel task owns an input batch exactly like a direct task; the
// outbound gate must key on channel provenance, not owner presence, or every
// Slack reply is silently dropped.
func TestOutbound_PostsSealedChannelTaskReply(t *testing.T) {
	q := &fakeOutboundQueries{
		task:                db.AgentTaskQueue{ChatInputTaskID: uid(2)},
		taskChannelIngested: true,
		binding: db.ChannelChatSessionBinding{
			InstallationID: uid(1),
			ChannelChatID:  "C123",
			Config:         []byte(`{"channel_id":"C123"}`),
		},
		inst: db.ChannelInstallation{ID: uid(1), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}

	newTestOutbound(q, fs).handleEvent(chatDoneEvent("00000000-0000-0000-0000-000000000001", "channel answer"))

	if fs.called != 1 {
		t.Fatalf("sender called %d times, want 1 for a sealed channel task", fs.called)
	}
}

func TestOutbound_UsesSharedDeliveryContract(t *testing.T) {
	q := &fakeOutboundQueries{
		task: db.AgentTaskQueue{ChatInputTaskID: uid(2)}, taskChannelIngested: true,
		binding: db.ChannelChatSessionBinding{InstallationID: uid(1), ChannelChatID: "C123", Config: []byte(`{"channel_id":"C123"}`)},
		inst:    db.ChannelInstallation{ID: uid(1), WorkspaceID: uid(9), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}
	recorder := &fakeDeliveryRecorder{}
	newTestOutbound(q, fs).WithDeliveryRecorder(recorder).handleEvent(chatDoneEvent("00000000-0000-0000-0000-000000000001", "audited answer"))

	if fs.called != 1 || recorder.claimed != 1 || recorder.delivered != 1 {
		t.Fatalf("send/claim/deliver = %d/%d/%d, want 1/1/1", fs.called, recorder.claimed, recorder.delivered)
	}
	if recorder.input.ChannelType != TypeSlack || recorder.input.OperationKind != delivery.OperationChatReply || recorder.input.Payload != "audited answer" {
		t.Fatalf("delivery input = %+v", recorder.input)
	}
}

func TestOutbound_UsesIndependentAuditedFailureNotice(t *testing.T) {
	q := &fakeOutboundQueries{
		task: db.AgentTaskQueue{ChatInputTaskID: uid(2)}, taskChannelIngested: true,
		binding: db.ChannelChatSessionBinding{InstallationID: uid(1), ChannelChatID: "C123", Config: []byte(`{"channel_id":"C123"}`)},
		inst:    db.ChannelInstallation{ID: uid(1), WorkspaceID: uid(9), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}
	recorder := &fakeDeliveryRecorder{}
	event := events.Event{
		Type: protocol.EventTaskFailed,
		Payload: map[string]any{
			"task_id":         "00000000-0000-0000-0000-000000000002",
			"chat_session_id": "00000000-0000-0000-0000-000000000001",
			"error":           "task timed out", "retry_pending": false,
		},
	}
	newTestOutbound(q, fs).WithDeliveryRecorder(recorder).handleEvent(event)

	if fs.called != 1 || recorder.claimed != 1 || recorder.delivered != 1 {
		t.Fatalf("send/claim/deliver = %d/%d/%d, want 1/1/1", fs.called, recorder.claimed, recorder.delivered)
	}
	if recorder.input.OperationKind != delivery.OperationFailureNotice || recorder.input.Payload != "⚠️ task timed out" || fs.got.Text != "⚠️ task timed out" {
		t.Fatalf("failure delivery input=%+v outbound=%+v", recorder.input, fs.got)
	}
}

func TestOutbound_SuppressesFailureNoticeWhileRetryIsPending(t *testing.T) {
	q := &fakeOutboundQueries{
		task: db.AgentTaskQueue{ChatInputTaskID: uid(2)}, taskChannelIngested: true,
		binding: db.ChannelChatSessionBinding{InstallationID: uid(1), ChannelChatID: "C123", Config: []byte(`{"channel_id":"C123"}`)},
		inst:    db.ChannelInstallation{ID: uid(1), WorkspaceID: uid(9), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}
	recorder := &fakeDeliveryRecorder{}
	event := events.Event{
		Type: protocol.EventTaskFailed,
		Payload: map[string]any{
			"task_id":         "00000000-0000-0000-0000-000000000002",
			"chat_session_id": "00000000-0000-0000-0000-000000000001",
			"error":           "task timed out", "retry_pending": true,
		},
	}
	newTestOutbound(q, fs).WithDeliveryRecorder(recorder).handleEvent(event)

	if fs.called != 0 || recorder.claimed != 0 {
		t.Fatalf("retry-pending failure sent/claimed = %d/%d", fs.called, recorder.claimed)
	}
}

func TestOutbound_PostsReplyToBoundSlackChannel(t *testing.T) {
	q := &fakeOutboundQueries{
		// Composite isolation key; real channel + reply thread come from config /
		// last_thread_id.
		binding: db.ChannelChatSessionBinding{
			InstallationID: uid(1),
			ChannelChatID:  "C123:1111.0",
			Config:         []byte(`{"channel_id":"C123"}`),
			LastThreadID:   pgtype.Text{String: "1111.0", Valid: true},
		},
		inst: db.ChannelInstallation{ID: uid(1), Status: "active", Config: slackInstallConfigJSON()},
	}
	fs := &fakeSender{}
	o := newTestOutbound(q, fs)

	o.handleEvent(chatDoneEvent("00000000-0000-0000-0000-000000000001", "**all done**"))

	if fs.called != 1 {
		t.Fatalf("sender called %d times, want 1", fs.called)
	}
	if fs.got.ChatID != "C123" {
		t.Errorf("ChatID = %q, want the real channel from config (not the composite key)", fs.got.ChatID)
	}
	if fs.got.ThreadID != "1111.0" {
		t.Errorf("ThreadID = %q, want the recorded reply thread", fs.got.ThreadID)
	}
	if fs.got.Text != "**all done**" {
		t.Errorf("Text = %q, want the raw content (Send applies mrkdwn)", fs.got.Text)
	}
}

func TestOutbound_IgnoresNonSlackAndEmptyAndRevoked(t *testing.T) {
	const sid = "00000000-0000-0000-0000-000000000001"
	activeInst := db.ChannelInstallation{ID: uid(1), Status: "active", Config: slackInstallConfigJSON()}
	boundBinding := db.ChannelChatSessionBinding{InstallationID: uid(1), ChannelChatID: "C1", Config: []byte(`{"channel_id":"C1"}`)}

	cases := []struct {
		name string
		q    *fakeOutboundQueries
		evt  events.Event
	}{
		{
			name: "no slack binding (Feishu / web session)",
			q:    &fakeOutboundQueries{bindingErr: pgx.ErrNoRows},
			evt:  chatDoneEvent(sid, "hi"),
		},
		{
			name: "empty completion content",
			q:    &fakeOutboundQueries{binding: boundBinding, inst: activeInst},
			evt:  chatDoneEvent(sid, ""),
		},
		{
			name: "revoked installation",
			q:    &fakeOutboundQueries{binding: boundBinding, inst: db.ChannelInstallation{ID: uid(1), Status: "revoked", Config: slackInstallConfigJSON()}},
			evt:  chatDoneEvent(sid, "hi"),
		},
		{
			name: "non-chat event (no session id)",
			q:    &fakeOutboundQueries{},
			evt:  chatDoneEvent("", "hi"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeSender{}
			newTestOutbound(tc.q, fs).handleEvent(tc.evt)
			if fs.called != 0 {
				t.Errorf("%s: sender must not be called, got %d", tc.name, fs.called)
			}
		})
	}
}
