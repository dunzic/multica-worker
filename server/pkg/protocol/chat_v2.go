package protocol

// ConversationSummary is pushed on conversation:created / conversation:updated.
// Subset of handler.ConversationWire (participant list is not duplicated
// here — clients refetch the conversation if they need full participants).
type ConversationSummary struct {
	ConversationID   string   `json:"conversation_id"`
	WorkspaceID      string   `json:"workspace_id"`
	Kind             string   `json:"kind"`
	Title            *string  `json:"title,omitempty"`
	ArchivedAt       *string  `json:"archived_at,omitempty"`
	UpdatedAt        string   `json:"updated_at"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

// ParticipantChangePayload covers participant:added and participant:removed.
// On removal, Role may be empty.
type ParticipantChangePayload struct {
	ConversationID   string   `json:"conversation_id"`
	PrincipalType    string   `json:"principal_type"`
	PrincipalID      string   `json:"principal_id"`
	Role             string   `json:"role,omitempty"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

// TopicChangePayload covers topic:created and topic:updated.
type TopicChangePayload struct {
	ConversationID   string   `json:"conversation_id"`
	TopicID          string   `json:"topic_id"`
	Title            *string  `json:"title,omitempty"`
	ArchivedAt       *string  `json:"archived_at,omitempty"`
	LastMessageAt    string   `json:"last_message_at,omitempty"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

// MessageSentPayload broadcasts a user-sent message in a topic.
type MessageSentPayload struct {
	ConversationID   string   `json:"conversation_id"`
	TopicID          string   `json:"topic_id"`
	MessageID        string   `json:"message_id"`
	SenderType       string   `json:"sender_type"`
	SenderID         string   `json:"sender_id"`
	Content          string   `json:"content"`
	TaskID           *string  `json:"task_id,omitempty"`
	CreatedAt        string   `json:"created_at"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

// MessageDonePayload is the v2 parallel of ChatDonePayload. Fired by the
// task-completion path once the assistant message row is persisted.
type MessageDonePayload struct {
	ConversationID   string   `json:"conversation_id"`
	TopicID          string   `json:"topic_id"`
	MessageID        string   `json:"message_id"`
	TaskID           string   `json:"task_id"`
	AgentID          string   `json:"agent_id"`
	Content          string   `json:"content"`
	CreatedAt        string   `json:"created_at"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

// ConversationReadPayload syncs the read-state watermark across the same
// user's other tabs.
type ConversationReadPayload struct {
	ConversationID   string   `json:"conversation_id"`
	UserID           string   `json:"user_id"`
	LastReadTopicID  *string  `json:"last_read_topic_id,omitempty"`
	RecipientUserIDs []string `json:"recipient_user_ids"`
}

const (
	EventConversationCreated = "conversation:created"
	EventConversationUpdated = "conversation:updated"
	EventConversationRead    = "conversation:read"
	EventParticipantAdded    = "participant:added"
	EventParticipantRemoved  = "participant:removed"
	EventTopicCreated        = "topic:created"
	EventTopicUpdated        = "topic:updated"
	EventMessageSent         = "message:sent"
	EventMessageDone         = "message:done"
)
