// Participant-scoped collaboration event listener.
//
// Responsibilities:
//  1. Tell the workspace-broadcast SubscribeAll in listeners.go to SKIP
//     every v2 event — they're not workspace-wide; they're
//     per-recipient via Broadcaster.SendToUser.
//  2. Register the v2 SubscribeAll handler that does the per-recipient
//     fan-out by reading recipient_user_ids off each payload.
package main

import (
	"encoding/json"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// chatV2EventTypes lists every event the v2 surface publishes. Used
// both to opt out of workspace broadcast in listeners.go AND to gate
// the v2 fan-out subscriber below.
var chatV2EventTypes = map[string]bool{
	protocol.EventConversationCreated: true,
	protocol.EventConversationUpdated: true,
	protocol.EventConversationRead:    true,
	protocol.EventParticipantAdded:    true,
	protocol.EventParticipantRemoved:  true,
	protocol.EventTopicCreated:        true,
	protocol.EventTopicUpdated:        true,
	protocol.EventMessageSent:         true,
	protocol.EventMessageDone:         true,
}

// markChatV2EventsNonBroadcast extends the personalEvents map (passed in
// by listeners.go) so the upstream workspace-broadcast SubscribeAll
// skips v2 types. Per-recipient delivery happens in
// registerChatV2BusListener below.
func markChatV2EventsNonBroadcast(personalEvents map[string]bool) {
	for t := range chatV2EventTypes {
		personalEvents[t] = true
	}
}

// registerChatV2BusListener attaches the v2 fan-out subscriber.
//
// Strategy: every v2 payload carries recipient_user_ids at the top
// level. We read it via a JSON round-trip so we don't need a type
// switch over every v2 payload shape (the set is bounded but grows;
// this scales to new event types for free as long as they keep the
// field). Per-recipient delivery via Broadcaster.SendToUser keeps
// DMs / private groups scoped to actual participants.
func registerChatV2BusListener(bus *events.Bus, hub realtime.Broadcaster) {
	bus.SubscribeAll(func(e events.Event) {
		if !chatV2EventTypes[e.Type] {
			return
		}
		if e.WorkspaceID == "" {
			return
		}
		payloadBytes, err := json.Marshal(e.Payload)
		if err != nil {
			return
		}
		var scopedPayload map[string]any
		if json.Unmarshal(payloadBytes, &scopedPayload) != nil {
			return
		}
		scopedPayload["workspace_id"] = e.WorkspaceID
		data, err := json.Marshal(map[string]any{
			"type": e.Type, "payload": scopedPayload, "actor_id": e.ActorID, "actor_type": e.ActorType,
		})
		if err != nil {
			slog.Error("chat v2 marshal failed", "event_type", e.Type, "error", err)
			return
		}
		var env struct {
			Payload struct {
				RecipientUserIDs []string `json:"recipient_user_ids"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Error("chat v2 unmarshal failed", "event_type", e.Type, "error", err)
			return
		}
		for _, uid := range env.Payload.RecipientUserIDs {
			if uid != "" {
				hub.SendToUser(uid, data)
			}
		}
	})
}
