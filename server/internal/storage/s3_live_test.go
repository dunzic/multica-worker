package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
)

// TestS3StorageLiveRoleSourceRoundTrip is an opt-in production-readiness
// probe for the exact S3-compatible backend configured for a deployment. It
// writes only beneath a unique validation prefix and always attempts cleanup.
// This test is intentionally excluded from ordinary CI because it requires
// real credentials and performs external I/O.
func TestS3StorageLiveRoleSourceRoundTrip(t *testing.T) {
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST")) != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 with isolated S3 credentials to run")
	}
	store := NewS3StorageFromEnv()
	if store == nil {
		t.Fatal("S3_BUCKET is required for the live role-source storage test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	key := "multica-role-source-validation/" + uuid.NewString() + "/probe.txt"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := store.PurgeObject(cleanupCtx, key); err != nil {
			t.Logf("live S3 probe cleanup for %s: %v", key, err)
		}
	})

	payload := "multica-role-source-storage-probe:" + uuid.NewString()
	if _, err := store.UploadStream(ctx, key, strings.NewReader(payload), int64(len(payload)), "text/plain", "probe.txt"); err != nil {
		t.Fatalf("live S3 streaming upload: %v", err)
	}
	reader, err := store.GetReader(ctx, key)
	if err != nil {
		t.Fatalf("live S3 readback: %v", err)
	}
	body, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("live S3 read body: read=%v close=%v", readErr, closeErr)
	}
	if string(body) != payload {
		t.Fatalf("live S3 readback body mismatch: got %d bytes, want %d", len(body), len(payload))
	}
	if err := store.PurgeObject(ctx, key); err != nil {
		t.Fatalf("live S3 permanent purge: %v", err)
	}
	if versions, err := countS3ObjectVersions(ctx, store, key); err != nil {
		t.Fatalf("live S3 verify retained versions: %v", err)
	} else if versions != 0 {
		t.Fatalf("live S3 retained %d versions/delete markers after purge", versions)
	}
	if reader, err := store.GetReader(ctx, key); err == nil {
		_ = reader.Close()
		t.Fatal("live S3 object remained readable after delete")
	} else if !isS3ObjectNotFound(err) {
		t.Fatalf("live S3 post-delete read returned a non-not-found error: %v", err)
	}
}

func countS3ObjectVersions(ctx context.Context, store *S3Storage, key string) (int, error) {
	paginator := s3.NewListObjectVersionsPaginator(store.client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(store.bucket), Prefix: aws.String(key),
	})
	count := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		for _, version := range page.Versions {
			if aws.ToString(version.Key) == key {
				count++
			}
		}
		for _, marker := range page.DeleteMarkers {
			if aws.ToString(marker.Key) == key {
				count++
			}
		}
	}
	return count, nil
}

func isS3ObjectNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
