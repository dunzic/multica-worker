package engine

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type readbackRecorder struct {
	installationID pgtype.UUID
	externalID     string
	inboundID      string
	calls          int
}

func (r *readbackRecorder) MarkReadback(_ context.Context, installationID pgtype.UUID, externalID, inboundID string) (db.ChannelDelivery, error) {
	r.installationID = installationID
	r.externalID = externalID
	r.inboundID = inboundID
	r.calls++
	return db.ChannelDelivery{}, nil
}

func TestRecordDeliveryReadbackRequiresExplicitReply(t *testing.T) {
	recorder := &readbackRecorder{}
	router := NewRouter(nil, nil, nil, RouterConfig{Readbacks: recorder})
	installationID := pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}
	router.recordDeliveryReadback(context.Background(), ResolvedInstallation{ID: installationID}, channel.InboundMessage{
		MessageID: "human-message", ReplyTo: &channel.ReplyCtx{MessageID: "provider-message"},
		Source: channel.Source{ChannelType: channel.Type("slack")},
	})
	if recorder.calls != 1 || recorder.installationID != installationID || recorder.externalID != "provider-message" || recorder.inboundID != "human-message" {
		t.Fatalf("readback call = %+v", recorder)
	}

	router.recordDeliveryReadback(context.Background(), ResolvedInstallation{ID: installationID}, channel.InboundMessage{MessageID: "plain-message"})
	if recorder.calls != 1 {
		t.Fatalf("plain message created readback; calls = %d", recorder.calls)
	}
}
