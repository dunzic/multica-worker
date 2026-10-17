export const MAX_FILE_SIZE = 100 * 1024 * 1024; // 100 MB
// Mirrors server/internal/service/chat_attachment_limits.go.
export const CHAT_ATTACHMENT_LIMITS = {
  maxFiles: 100,
  maxBytes: 500 * 1024 * 1024,
} as const;
export const MAX_CONCURRENT_UPLOADS = 3;
