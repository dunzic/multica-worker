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
	"github.com/aws/aws-sdk-go-v2/credentials"
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
	ensureLiveS3Bucket(t, store, false)
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

	payloads := []string{
		"multica-role-source-storage-probe-v1:" + uuid.NewString(),
		"multica-role-source-storage-probe-v2:" + uuid.NewString(),
	}
	for index, payload := range payloads {
		if _, err := store.UploadStream(ctx, key, strings.NewReader(payload), int64(len(payload)), "text/plain", "probe.txt"); err != nil {
			t.Fatalf("live S3 streaming upload version %d: %v", index+1, err)
		}
	}
	payload := payloads[len(payloads)-1]
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
	if err := store.DeleteObject(ctx, key); err != nil {
		t.Fatalf("live S3 create pre-existing delete marker: %v", err)
	}
	if versions, err := countS3ObjectVersions(ctx, store, key); err != nil {
		t.Fatalf("live S3 count prepared versions: %v", err)
	} else if versions != 3 {
		t.Fatalf("live S3 prepared inventory = %d, want two versions and one delete marker", versions)
	}
	result, err := store.PurgeObjectWithResult(ctx, key)
	if err != nil {
		t.Fatalf("live S3 permanent purge: %v", err)
	}
	wantBytes := int64(len(payloads[0]) + len(payloads[1]))
	if result.Backend != PermanentPurgeBackendS3 || result.Mode != PermanentPurgeModeVersions ||
		result.VersionsDeleted != 2 || result.DeleteMarkersDeleted < 1 ||
		result.ObservedBytesDeleted != wantBytes || !result.VerifiedAbsent {
		t.Fatalf("live S3 purge result = %+v, want two versions, the prepared marker, %d bytes and verified absence", result, wantBytes)
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

// TestS3StorageLiveRoleSourceLateWriteConverges proves the durable retry
// contract against a real versioned provider: content written after one purge
// is observable and a later reconciliation pass removes it. This is not a
// claim that a single S3 request can prevent future authorized writers.
func TestS3StorageLiveRoleSourceLateWriteConverges(t *testing.T) {
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST")) != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 with isolated S3 credentials to run")
	}
	store := NewS3StorageFromEnv()
	if store == nil {
		t.Fatal("S3_BUCKET is required for the live role-source storage test")
	}
	ensureLiveS3Bucket(t, store, false)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	key := "multica-role-source-validation/" + uuid.NewString() + "/late-put.txt"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := store.PurgeObject(cleanupCtx, key); err != nil {
			t.Logf("live S3 late-write cleanup for %s: %v", key, err)
		}
	})

	firstPayload := "before-first-purge:" + uuid.NewString()
	if _, err := store.UploadStream(ctx, key, strings.NewReader(firstPayload), int64(len(firstPayload)), "text/plain", "late-put.txt"); err != nil {
		t.Fatalf("live S3 initial upload: %v", err)
	}
	firstResult, err := store.PurgeObjectWithResult(ctx, key)
	if err != nil || !firstResult.VerifiedAbsent {
		t.Fatalf("live S3 first purge: result=%+v err=%v", firstResult, err)
	}

	latePayload := "after-first-purge:" + uuid.NewString()
	if _, err := store.UploadStream(ctx, key, strings.NewReader(latePayload), int64(len(latePayload)), "text/plain", "late-put.txt"); err != nil {
		t.Fatalf("live S3 late upload: %v", err)
	}
	if versions, err := countS3ObjectVersions(ctx, store, key); err != nil {
		t.Fatalf("live S3 count late version: %v", err)
	} else if versions != 1 {
		t.Fatalf("live S3 late-write inventory = %d, want one new version", versions)
	}
	secondResult, err := store.PurgeObjectWithResult(ctx, key)
	if err != nil {
		t.Fatalf("live S3 retry purge: %v", err)
	}
	if secondResult.VersionsDeleted != 1 || secondResult.DeleteMarkersDeleted != 1 ||
		secondResult.ObservedBytesDeleted != int64(len(latePayload)) || !secondResult.VerifiedAbsent {
		t.Fatalf("live S3 retry purge result = %+v", secondResult)
	}
	if versions, err := countS3ObjectVersions(ctx, store, key); err != nil {
		t.Fatalf("live S3 verify late-write purge: %v", err)
	} else if versions != 0 {
		t.Fatalf("live S3 retained %d versions/delete markers after retry purge", versions)
	}
}

// TestS3StorageLiveRoleSourceObjectLockFailsClosed proves that a provider-side
// legal hold cannot be mistaken for deletion. The held version must remain
// readable by version ID, VerifiedAbsent must remain false, and deletion may
// succeed only after an authorized operator removes the hold.
func TestS3StorageLiveRoleSourceObjectLockFailsClosed(t *testing.T) {
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST")) != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 with isolated S3 credentials to run")
	}
	lockedBucket := strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_LOCK_BUCKET"))
	if lockedBucket == "" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_LOCK_BUCKET to an isolated Object Lock-enabled bucket")
	}
	baseStore := NewS3StorageFromEnv()
	if baseStore == nil {
		t.Fatal("S3_BUCKET is required for the live role-source storage test")
	}
	storeValue := *baseStore
	storeValue.bucket = lockedBucket
	store := &storeValue
	adminStore := liveS3StoreFromCredentialEnv(t, store, "MULTICA_LIVE_ROLE_SOURCE_ADMIN")
	ensureLiveS3Bucket(t, adminStore, true)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	key := "multica-role-source-validation/" + uuid.NewString() + "/legal-hold.txt"
	versionID := ""
	holdApplied := false
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if holdApplied && versionID != "" {
			_, _ = adminStore.client.PutObjectLegalHold(cleanupCtx, &s3.PutObjectLegalHoldInput{
				Bucket: aws.String(store.bucket), Key: aws.String(key), VersionId: aws.String(versionID),
				LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOff},
			})
		}
		if err := store.PurgeObject(cleanupCtx, key); err != nil {
			t.Logf("live S3 Object Lock cleanup for %s: %v", key, err)
		}
	})

	payload := "legal-hold-content:" + uuid.NewString()
	if _, err := store.UploadStream(ctx, key, strings.NewReader(payload), int64(len(payload)), "text/plain", "legal-hold.txt"); err != nil {
		t.Fatalf("live S3 Object Lock upload: %v", err)
	}
	objects, err := store.listObjectVersionsForPurge(ctx, key, 10)
	if err != nil {
		t.Fatalf("live S3 locate locked version: %v", err)
	}
	for _, object := range objects {
		if !object.DeleteMarker {
			versionID = aws.ToString(object.Identifier.VersionId)
			break
		}
	}
	if versionID == "" {
		t.Fatal("live S3 Object Lock upload returned no retained version ID")
	}
	if _, err := adminStore.client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), VersionId: aws.String(versionID),
		LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOn},
	}); err != nil {
		t.Fatalf("live S3 apply legal hold: %v", err)
	}
	holdApplied = true

	result, purgeErr := store.PurgeObjectWithResult(ctx, key)
	if purgeErr == nil {
		t.Fatalf("live S3 purge reported success while legal hold was active: %+v", result)
	}
	if result.VerifiedAbsent {
		t.Fatalf("live S3 purge claimed verified absence while legal hold was active: %+v", result)
	}
	lockedReader, err := store.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), VersionId: aws.String(versionID),
	})
	if err != nil {
		t.Fatalf("live S3 held version was not retrievable after refused purge: %v", err)
	}
	lockedBody, readErr := io.ReadAll(lockedReader.Body)
	closeErr := lockedReader.Body.Close()
	if readErr != nil || closeErr != nil || string(lockedBody) != payload {
		t.Fatalf("live S3 held-version evidence mismatch: bytes=%d read=%v close=%v", len(lockedBody), readErr, closeErr)
	}

	if _, err := adminStore.client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), VersionId: aws.String(versionID),
		LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOff},
	}); err != nil {
		t.Fatalf("live S3 remove legal hold: %v", err)
	}
	holdApplied = false
	if result, err := store.PurgeObjectWithResult(ctx, key); err != nil || !result.VerifiedAbsent {
		t.Fatalf("live S3 purge after legal-hold removal: result=%+v err=%v", result, err)
	}
	if versions, err := countS3ObjectVersions(ctx, store, key); err != nil {
		t.Fatalf("live S3 verify post-hold purge: %v", err)
	} else if versions != 0 {
		t.Fatalf("live S3 retained %d versions/delete markers after legal-hold removal", versions)
	}
}

// TestS3StorageLiveRoleSourcePermissionFailureFailsClosed proves a deployment
// denied s3:DeleteObjectVersion cannot produce a successful purge receipt.
// A privileged setup identity writes the version; the intentionally restricted
// identity attempts the purge; the original version must remain retrievable.
func TestS3StorageLiveRoleSourcePermissionFailureFailsClosed(t *testing.T) {
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST")) != "1" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 with isolated S3 credentials to run")
	}
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_DENIED_ACCESS_KEY")) == "" {
		t.Skip("set MULTICA_LIVE_ROLE_SOURCE_DENIED_ACCESS_KEY and _SECRET_KEY to a validation identity denied s3:DeleteObjectVersion")
	}
	store := NewS3StorageFromEnv()
	if store == nil {
		t.Fatal("S3_BUCKET is required for the live role-source storage test")
	}
	ensureLiveS3Bucket(t, store, false)
	deniedStore := liveS3StoreFromCredentialEnv(t, store, "MULTICA_LIVE_ROLE_SOURCE_DENIED")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	key := "multica-role-source-validation/" + uuid.NewString() + "/permission-denied.txt"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := store.PurgeObject(cleanupCtx, key); err != nil {
			t.Logf("live S3 permission-denied cleanup for %s: %v", key, err)
		}
	})

	payload := "permission-denied-content:" + uuid.NewString()
	if _, err := store.UploadStream(ctx, key, strings.NewReader(payload), int64(len(payload)), "text/plain", "permission-denied.txt"); err != nil {
		t.Fatalf("live S3 permission-denied setup upload: %v", err)
	}
	objects, err := store.listObjectVersionsForPurge(ctx, key, 10)
	if err != nil {
		t.Fatalf("live S3 locate permission-denied version: %v", err)
	}
	versionID := ""
	for _, object := range objects {
		if !object.DeleteMarker {
			versionID = aws.ToString(object.Identifier.VersionId)
			break
		}
	}
	if versionID == "" {
		t.Fatal("live S3 permission-denied setup returned no retained version ID")
	}
	result, purgeErr := deniedStore.PurgeObjectWithResult(ctx, key)
	if purgeErr == nil {
		t.Fatalf("live S3 purge reported success while s3:DeleteObjectVersion was denied: %+v", result)
	}
	if !strings.Contains(purgeErr.Error(), "AccessDenied") {
		t.Fatalf("live S3 version-delete denial returned %v, want provider AccessDenied", purgeErr)
	}
	if result.VerifiedAbsent {
		t.Fatalf("live S3 purge claimed verified absence after permission failure: %+v", result)
	}
	retained, err := store.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(store.bucket), Key: aws.String(key), VersionId: aws.String(versionID),
	})
	if err != nil {
		t.Fatalf("live S3 version was not retained after permission failure: %v", err)
	}
	retainedBody, readErr := io.ReadAll(retained.Body)
	closeErr := retained.Body.Close()
	if readErr != nil || closeErr != nil || string(retainedBody) != payload {
		t.Fatalf("live S3 retained-version evidence mismatch: bytes=%d read=%v close=%v", len(retainedBody), readErr, closeErr)
	}
}

func liveS3StoreFromCredentialEnv(t *testing.T, base *S3Storage, prefix string) *S3Storage {
	t.Helper()
	accessKey := strings.TrimSpace(os.Getenv(prefix + "_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv(prefix + "_SECRET_KEY"))
	if accessKey == "" && secretKey == "" {
		return base
	}
	if accessKey == "" || secretKey == "" {
		t.Fatalf("%s_ACCESS_KEY and %s_SECRET_KEY must be set together", prefix, prefix)
	}
	clone := *base
	options := s3.Options{
		Region:       base.region,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		UsePathStyle: base.usePathStyle,
	}
	if base.endpointURL != "" {
		options.BaseEndpoint = aws.String(base.endpointURL)
	}
	clone.client = s3.New(options)
	return &clone
}

func ensureLiveS3Bucket(t *testing.T, store *S3Storage, objectLock bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if strings.TrimSpace(os.Getenv("MULTICA_LIVE_ROLE_SOURCE_STORAGE_PROVISION")) == "1" {
		input := &s3.CreateBucketInput{Bucket: aws.String(store.bucket)}
		if objectLock {
			input.ObjectLockEnabledForBucket = aws.Bool(true)
		}
		if _, err := store.client.CreateBucket(ctx, input); err != nil && !isS3BucketAlreadyOwned(err) {
			t.Fatalf("live S3 create validation bucket %q: %v", store.bucket, err)
		}
		if _, err := store.client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket:                  aws.String(store.bucket),
			VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
		}); err != nil {
			t.Fatalf("live S3 enable versioning for %q: %v", store.bucket, err)
		}
	}
	versioning, err := store.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(store.bucket)})
	if err != nil {
		t.Fatalf("live S3 inspect versioning for %q: %v", store.bucket, err)
	}
	if versioning.Status != types.BucketVersioningStatusEnabled {
		t.Fatalf("live S3 bucket %q versioning = %q, want Enabled", store.bucket, versioning.Status)
	}
	if objectLock {
		lockConfig, err := store.client.GetObjectLockConfiguration(ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String(store.bucket)})
		if err != nil {
			t.Fatalf("live S3 inspect Object Lock for %q: %v", store.bucket, err)
		}
		if lockConfig.ObjectLockConfiguration == nil || lockConfig.ObjectLockConfiguration.ObjectLockEnabled != types.ObjectLockEnabledEnabled {
			t.Fatalf("live S3 bucket %q is not Object Lock-enabled", store.bucket)
		}
	}
}

func isS3BucketAlreadyOwned(err error) bool {
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	switch apiError.ErrorCode() {
	case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
		return true
	default:
		return false
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
