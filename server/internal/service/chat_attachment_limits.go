package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Keep the web/desktop limits in packages/core/constants/upload.ts aligned.
const MaxChatMessageAttachments = 100
const MaxChatMessageAttachmentBytes int64 = 500 << 20
const maxChatAttachmentBytes int64 = 100 << 20

var ErrChatAttachmentCapacity = errors.New("message attachments exceed the 100 file, 100 MiB per file or 500 MiB total limit")

// Validate only rows actually bound under the caller's send transaction. Sizes
// come from persisted upload metadata, not the request, and a refusal rolls
// back message creation, file bindings and task dispatch together.
func ValidateChatMessageAttachments(ctx context.Context, q *db.Queries, workspaceID, messageID pgtype.UUID) error {
	attachments, err := q.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{WorkspaceID: workspaceID, ChatMessageID: messageID})
	if err != nil {
		return fmt.Errorf("read message attachment capacity: %w", err)
	}
	if len(attachments) > MaxChatMessageAttachments {
		return ErrChatAttachmentCapacity
	}
	var total int64
	for _, attachment := range attachments {
		size := attachment.SizeBytes
		if size < 0 || size > maxChatAttachmentBytes || size > MaxChatMessageAttachmentBytes-total {
			return ErrChatAttachmentCapacity
		}
		total += size
	}
	return nil
}
