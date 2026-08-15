package storage

import (
	"context"
	"errors"
	"fmt"
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

// PermanentPurgeError preserves whether a failed call may already have
// changed storage. Callers must persist that ambiguity before retrying: a
// later empty inventory proves absence, but cannot reconstruct version/byte
// counts from a response lost after the provider performed the deletion.
type PermanentPurgeError struct {
	Operation      string
	MayHaveMutated bool
	Err            error
}

func (e *PermanentPurgeError) Error() string {
	if e == nil {
		return "artifact permanent purge failed"
	}
	if e.Operation == "" {
		return fmt.Sprintf("artifact permanent purge failed: %v", e.Err)
	}
	return fmt.Sprintf("artifact permanent purge %s: %v", e.Operation, e.Err)
}

func (e *PermanentPurgeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func PermanentPurgeMayHaveMutated(err error) bool {
	var purgeErr *PermanentPurgeError
	return errors.As(err, &purgeErr) && purgeErr.MayHaveMutated
}

func permanentPurgeFailure(operation string, mayHaveMutated bool, err error) error {
	if err == nil {
		return nil
	}
	return &PermanentPurgeError{Operation: operation, MayHaveMutated: mayHaveMutated, Err: err}
}

type Presigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type DownloadPresigner interface {
	PresignGetWithContentDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error)
}
