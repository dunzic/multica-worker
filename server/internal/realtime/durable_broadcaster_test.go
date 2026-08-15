package realtime

import (
	"errors"
	"testing"
)

type durableRecordingRelay struct {
	calls int
	id    string
	err   error
}

func (r *durableRecordingRelay) PublishWithID(_, _, _ string, _ []byte, id string) error {
	r.calls++
	r.id = id
	return r.err
}

func TestDualWriteDurablePublishReturnsRelayFailureAndKeepsStableID(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	relay := &durableRecordingRelay{err: wantErr}
	hub := NewHub()
	client := attachRealtimeTestClient(hub, ScopeWorkspace, "workspace-1")
	b := NewDualWriteBroadcaster(hub, relay)
	err := b.PublishDurable(ScopeWorkspace, "workspace-1", "", []byte(`{"type":"role_source:applied"}`), "event-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("PublishDurable error=%v, want relay error", err)
	}
	if relay.calls != 1 || relay.id != "event-1" {
		t.Fatalf("relay calls=%d id=%q, want one stable event id", relay.calls, relay.id)
	}
	select {
	case frame := <-client.send:
		t.Fatalf("redis rejection must not create local-only visibility: %s", frame)
	default:
	}
}
