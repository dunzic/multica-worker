package slack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/delivery"
)

type slackRoundTripFunc func(*http.Request) (*http.Response, error)

func (f slackRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChunkMessage(t *testing.T) {
	if got := chunkMessage("short", 100); len(got) != 1 || got[0] != "short" {
		t.Errorf("short message should be one chunk: %v", got)
	}
	long := make([]rune, 250)
	for i := range long {
		long[i] = 'a'
	}
	chunks := chunkMessage(string(long), 100)
	if len(chunks) != 3 {
		t.Fatalf("250 runes / 100 = 3 chunks, got %d", len(chunks))
	}
	if len([]rune(chunks[0])) != 100 || len([]rune(chunks[2])) != 50 {
		t.Errorf("chunk sizes wrong: %d / %d", len([]rune(chunks[0])), len([]rune(chunks[2])))
	}
}

func TestSend(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1700000000.111111"}`))
	}))
	defer srv.Close()

	api := slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/"))
	c := newSlackSender(credentials{TeamID: "T1"}, api, nil)

	res, err := c.Send(context.Background(), channel.OutboundMessage{
		ChatID:   "C123",
		Text:     "reply body",
		ThreadID: "1700000000.000400",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.MessageID != "1700000000.111111" {
		t.Errorf("MessageID = %q", res.MessageID)
	}
	if gotForm.Get("channel") != "C123" || gotForm.Get("text") != "reply body" {
		t.Errorf("posted channel/text = %q / %q", gotForm.Get("channel"), gotForm.Get("text"))
	}
	if gotForm.Get("thread_ts") != "1700000000.000400" {
		t.Errorf("thread_ts = %q, want the inbound thread", gotForm.Get("thread_ts"))
	}
}

func TestSendExplicitProviderRejectionRemainsRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer srv.Close()

	recorder := &fakeDeliveryRecorder{}
	sender := newSlackSender(credentials{TeamID: "T1"}, slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/")), nil)
	_, err := delivery.Send(context.Background(), recorder, delivery.ClaimInput{
		WorkspaceID: uid(1), InstallationID: uid(2), TaskID: uid(3), ChatSessionID: uid(4),
		ChannelType: TypeSlack, ChannelChatID: "C1", OperationKind: delivery.OperationChatReply, Payload: "hello",
	}, func(ctx context.Context) (channel.SendResult, error) {
		return sender.Send(ctx, channel.OutboundMessage{ChatID: "C1", Text: "hello"})
	})
	if err == nil || recorder.failed != 1 || recorder.failureCode != "authorization" || recorder.ambiguous != 0 {
		t.Fatalf("err=%v recorder=%+v", err, recorder)
	}
}

func TestSendTransportLossAfterFirstChunkFreezesPartialDelivery(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: slackRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"channel":"C1","ts":"1.1"}`)),
			}, nil
		}
		return nil, errors.New("connection reset after request write")
	})}
	recorder := &fakeDeliveryRecorder{}
	sender := newSlackSender(credentials{TeamID: "T1"}, slack.New("xoxb-test", slack.OptionHTTPClient(client)), nil)
	result, err := delivery.Send(context.Background(), recorder, delivery.ClaimInput{
		WorkspaceID: uid(1), InstallationID: uid(2), TaskID: uid(3), ChatSessionID: uid(4),
		ChannelType: TypeSlack, ChannelChatID: "C1", OperationKind: delivery.OperationChatReply, Payload: "long reply",
	}, func(ctx context.Context) (channel.SendResult, error) {
		return sender.Send(ctx, channel.OutboundMessage{ChatID: "C1", Text: strings.Repeat("x", maxMessageRunes+1)})
	})
	if result.MessageID != "1.1" || !errors.Is(err, delivery.ErrAmbiguous) || recorder.ambiguous != 1 || recorder.ambiguityReason != delivery.AmbiguityPartialDelivery || recorder.failed != 0 {
		t.Fatalf("result=%+v err=%v recorder=%+v calls=%d", result, err, recorder, calls)
	}
}

// TestSend_AppliesMrkdwn guards the wiring: Send must run the agent's Markdown
// through formatMrkdwn before posting, so Slack renders it instead of showing
// literal markup. (The converter itself is covered in mrkdwn_test.go.)
func TestSend_AppliesMrkdwn(t *testing.T) {
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotText = r.PostForm.Get("text")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.1"}`))
	}))
	defer srv.Close()

	api := slack.New("xoxb-test", slack.OptionAPIURL(srv.URL+"/"))
	c := newSlackSender(credentials{TeamID: "T1"}, api, nil)

	if _, err := c.Send(context.Background(), channel.OutboundMessage{
		ChatID: "C1",
		Text:   "**bold** see [docs](http://x.com)",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotText != "*bold* see <http://x.com|docs>" {
		t.Errorf("Send must convert Markdown to mrkdwn before posting, got %q", gotText)
	}
}

func TestOutboundThreadTS(t *testing.T) {
	if got := outboundThreadTS(channel.OutboundMessage{ReplyTo: "111.1", ThreadID: "222.2"}); got != "111.1" {
		t.Errorf("explicit ReplyTo should win: %q", got)
	}
	if got := outboundThreadTS(channel.OutboundMessage{ThreadID: "222.2"}); got != "222.2" {
		t.Errorf("thread fallback: %q", got)
	}
	if got := outboundThreadTS(channel.OutboundMessage{}); got != "" {
		t.Errorf("top-level send has no thread: %q", got)
	}
}
