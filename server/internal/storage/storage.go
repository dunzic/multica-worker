package storage

import (
	"context"
	"io"
	"time"
)

type Storage interface {
	Upload(ctx context.Context, key string, data []byte, contentType string, filename string) (string, error)
	Delete(ctx context.Context, key string)
	// DeleteObject is Delete with the error surfaced — the channel-media
	// reconciler schedules retries on failure instead of assuming success.
	DeleteObject(ctx context.Context, key string) error
	DeleteKeys(ctx context.Context, keys []string)
	KeyFromURL(rawURL string) string
	// ObjectURL is the URL a successful Upload of key would return — a pure
	// function of configuration, so the media intent ledger can persist it
	// BEFORE the upload.
	ObjectURL(key string) string
	CdnDomain() string
	// GetReader streams an object back to the caller. Used by the attachment
	// preview proxy (GET /api/attachments/{id}/content) to bypass CloudFront
	// CORS and the inline/attachment Content-Disposition decision. Caller
	// must Close the returned reader.
	GetReader(ctx context.Context, key string) (io.ReadCloser, error)
}

const (
	PermanentPurgeBackendLocal = "local"
	PermanentPurgeBackendS3    = "s3"
	PermanentPurgeModeCurrent  = "current_object"
	PermanentPurgeModeVersions = "all_versions"
)

// PermanentPurgeResult is storage-provider evidence, not a billing receipt.
// VerifiedAbsent means the implementation performed a provider/filesystem
// read-after-delete and found no current object, retained version or delete
// marker for the exact key. ObservedBytesDeleted counts version bytes that the
// purge inventory actually saw; a retry after an earlier successful delete may
// legitimately report zero while still verifying absence.
type PermanentPurgeResult struct {
	Backend              string
	Mode                 string
	VersionsDeleted      int64
	DeleteMarkersDeleted int64
	ObservedBytesDeleted int64
	VerifiedAbsent       bool
}

type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type DownloadPresigner interface {
	PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
}
