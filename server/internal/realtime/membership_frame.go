package realtime

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Frame authority follows the source resource, not the socket's selected
// workspace. User-scoped delivery can cross workspaces and can be replayed.
func frameMembership(raw []byte, socket MembershipIdentity) (MembershipIdentity, bool) {
	var frame struct {
		Type        string          `json:"type"`
		WorkspaceID string          `json:"workspace_id"`
		Payload     json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return MembershipIdentity{}, false
	}
	// Invitations must reach users who are not members yet. They contain
	// invitation metadata, not workspace content, and are recipient-routed.
	switch frame.Type {
	case protocol.EventInvitationCreated, protocol.EventInvitationRevoked,
		protocol.EventInvitationAccepted, protocol.EventInvitationDeclined:
		return socket, true
	}
	var payload struct {
		WorkspaceID    string `json:"workspace_id"`
		ConversationID string `json:"conversation_id"`
		TopicID        string `json:"topic_id"`
		PrincipalType  string `json:"principal_type"`
		PrincipalID    string `json:"principal_id"`
		Item           struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"item"`
	}
	payloadErr := json.Unmarshal(frame.Payload, &payload)
	identity := MembershipIdentity{WorkspaceID: frame.WorkspaceID, UserID: socket.UserID}
	if identity.WorkspaceID == "" {
		identity.WorkspaceID = payload.WorkspaceID
	}
	if payload.TopicID != "" {
		if payloadErr != nil || identity.WorkspaceID == "" ||
			(frame.WorkspaceID != "" && payload.WorkspaceID != "" && frame.WorkspaceID != payload.WorkspaceID) {
			return MembershipIdentity{}, false
		}
		identity.TopicID = payload.TopicID
		identity.ConversationID = payload.ConversationID
	}
	switch frame.Type {
	case protocol.EventConversationCreated, protocol.EventConversationUpdated, protocol.EventConversationRead,
		protocol.EventParticipantAdded, protocol.EventParticipantRemoved,
		protocol.EventTopicCreated, protocol.EventTopicUpdated, protocol.EventMessageSent, protocol.EventMessageDone:
		if payloadErr != nil || identity.WorkspaceID == "" || payload.ConversationID == "" ||
			(frame.WorkspaceID != "" && payload.WorkspaceID != "" && frame.WorkspaceID != payload.WorkspaceID) {
			return MembershipIdentity{}, false
		}
		identity.ConversationID = payload.ConversationID
		// The removed member needs the existing ID-only cleanup signal. Other
		// members' participant changes and all content require active access.
		if frame.Type == protocol.EventParticipantRemoved && payload.PrincipalType == "member" && payload.PrincipalID == socket.UserID {
			identity.ConversationID = ""
		}
	case protocol.EventInboxNew:
		if identity.WorkspaceID == "" {
			identity.WorkspaceID = payload.Item.WorkspaceID
		}
		if identity.WorkspaceID == "" {
			return MembershipIdentity{}, false
		}
	case protocol.EventChatSessionCreated, protocol.EventChatSessionUpdated,
		protocol.EventChatMessage, protocol.EventChatDone, protocol.EventChatQuickActions,
		protocol.EventChatCancelFinalized, protocol.EventTaskMessage,
		protocol.EventTaskQueued, protocol.EventTaskDispatch, protocol.EventTaskRunning,
		protocol.EventTaskWaitingLocalDirectory, protocol.EventTaskProgress,
		protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled:
		if payloadErr != nil || identity.WorkspaceID == "" ||
			(frame.WorkspaceID != "" && payload.WorkspaceID != "" && frame.WorkspaceID != payload.WorkspaceID) {
			return MembershipIdentity{}, false
		}
	}
	if identity.WorkspaceID == "" {
		// Connection control and existing workspace-scoped frames carry no
		// separate authority. Private group frames never take this path.
		identity.WorkspaceID = socket.WorkspaceID
	}
	return identity, true
}

// A removed member may receive only the ID-based cleanup signal, never an
// arbitrary payload disguised as a removal event. Do not mutate the shared
// frame: other sockets may still be using the original bytes concurrently.
func membershipCleanupFrame(raw []byte, userID string) []byte {
	var frame struct {
		Type        string `json:"type"`
		EventID     string `json:"event_id,omitempty"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		Payload     struct {
			WorkspaceID    string `json:"workspace_id"`
			ConversationID string `json:"conversation_id"`
			PrincipalType  string `json:"principal_type"`
			PrincipalID    string `json:"principal_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Type != protocol.EventParticipantRemoved ||
		frame.Payload.PrincipalType != "member" || frame.Payload.PrincipalID != userID {
		return raw
	}
	clean, _ := json.Marshal(frame)
	return clean
}
