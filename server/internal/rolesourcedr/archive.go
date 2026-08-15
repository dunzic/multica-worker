package rolesourcedr

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
)

type streamUploader interface {
	UploadStream(context.Context, string, io.Reader, int64, string, string) (string, error)
}

type objectNotFoundClassifier interface {
	IsObjectNotFound(error) bool
}

const maxArtifactArchiveBytes int64 = 1 << 40 // 1 TiB safety ceiling per bundle

// WriteArtifactArchive writes a deterministic, uncompressed tar stream. Each
// member name is derived only from workspace UUID and SHA-256 digest; storage
// keys, filenames and source paths are not exported.
func WriteArtifactArchive(ctx context.Context, destination string, records []ArtifactRecord, store storage.Storage, createdAt time.Time) (digest string, err error) {
	if err := validateArtifactInventory(records); err != nil {
		return "", err
	}
	if len(records) > 0 && store == nil {
		return "", fmt.Errorf("artifact storage is unavailable")
	}
	file, err := openExclusive(destination)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if !committed {
			_ = os.Remove(destination)
		}
	}()
	h := sha256.New()
	tarWriter := tar.NewWriter(io.MultiWriter(file, h))
	for _, record := range records {
		member := filepath.ToSlash(filepath.Join(record.WorkspaceID, strings.TrimPrefix(record.Digest, "sha256:")))
		header := &tar.Header{Name: member, Mode: 0o400, Size: record.SizeBytes, ModTime: createdAt.UTC(), Format: tar.FormatPAX}
		if err = tarWriter.WriteHeader(header); err != nil {
			return "", fmt.Errorf("write artifact archive header: %w", err)
		}
		reader, openErr := store.GetReader(ctx, record.StorageKey)
		if openErr != nil {
			return "", fmt.Errorf("open artifact: %w", openErr)
		}
		bodyHash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(tarWriter, bodyHash), io.LimitReader(reader, record.SizeBytes+1))
		closeErr := reader.Close()
		if copyErr != nil || closeErr != nil || written != record.SizeBytes || "sha256:"+hex.EncodeToString(bodyHash.Sum(nil)) != record.Digest {
			return "", fmt.Errorf("artifact failed byte verification")
		}
	}
	if err = tarWriter.Close(); err != nil {
		return "", fmt.Errorf("close artifact archive: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("sync artifact archive: %w", err)
	}
	committed = true
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// RestoreArtifactArchive idempotently uploads the immutable bodies described
// by the restored database ledger. It verifies the signed archive and every
// member before the first provider mutation, then streams directly to storage
// without creating plaintext spool files. A retry skips exact bodies that a
// previous interrupted run already committed. It refuses extra, missing,
// duplicate or digest-invalid members and never mutates PostgreSQL.
func RestoreArtifactArchive(ctx context.Context, source, expectedArchiveDigest string, records []ArtifactRecord, store storage.Storage) error {
	if err := validateArtifactInventory(records); err != nil {
		return err
	}
	if len(records) > 0 && store == nil {
		return fmt.Errorf("artifact storage is unavailable")
	}
	if !sha256Pattern.MatchString(expectedArchiveDigest) {
		return fmt.Errorf("invalid expected artifact archive digest")
	}
	uploader, streamSupported := store.(streamUploader)
	requiresStream := false
	for _, record := range records {
		requiresStream = requiresStream || record.SizeBytes > 0
	}
	if requiresStream && !streamSupported {
		return fmt.Errorf("artifact storage does not support fixed-length streaming restore")
	}
	expected := make(map[string]ArtifactRecord, len(records))
	for _, record := range records {
		member := record.WorkspaceID + "/" + strings.TrimPrefix(record.Digest, "sha256:")
		expected[member] = record
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open artifact archive: %w", err)
	}
	defer file.Close()
	if err := preflightArtifactArchive(ctx, file, expected, expectedArchiveDigest); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind artifact archive: %w", err)
	}
	tarReader := tar.NewReader(file)
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("restore artifact archive interrupted: %w", err)
		}
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read artifact archive: %w", err)
		}
		record, ok := expected[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != record.SizeBytes {
			return fmt.Errorf("artifact archive inventory does not match restored ledger")
		}
		seen[header.Name] = true
		present, err := artifactAlreadyValid(ctx, store, record)
		if err != nil {
			return err
		}
		if present {
			bodyHash := sha256.New()
			written, copyErr := io.Copy(bodyHash, contextReader{ctx: ctx, reader: tarReader})
			if copyErr != nil || written != record.SizeBytes || "sha256:"+hex.EncodeToString(bodyHash.Sum(nil)) != record.Digest {
				return fmt.Errorf("read existing artifact archive member")
			}
			continue
		}

		bodyHash := sha256.New()
		counter := &countingWriter{writer: bodyHash}
		body := io.TeeReader(contextReader{ctx: ctx, reader: tarReader}, counter)
		var uploadErr error
		if record.SizeBytes == 0 {
			_, uploadErr = store.Upload(ctx, record.StorageKey, nil, "application/octet-stream", "")
		} else {
			_, uploadErr = uploader.UploadStream(ctx, record.StorageKey, body, record.SizeBytes, "application/octet-stream", "")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("restore artifact archive interrupted: %w", ctxErr)
		}
		if counter.written != record.SizeBytes || "sha256:"+hex.EncodeToString(bodyHash.Sum(nil)) != record.Digest {
			return fmt.Errorf("artifact restore upload did not consume the verified member")
		}
		verified, verifyErr := artifactAlreadyValid(ctx, store, record)
		if verified {
			// A provider may commit the immutable object and then lose the
			// response. Exact readback resolves that ambiguity as success.
			continue
		}
		if verifyErr != nil {
			return verifyErr
		}
		if uploadErr != nil {
			return fmt.Errorf("restore artifact upload failed")
		}
		return fmt.Errorf("restored artifact failed exact provider readback")
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("artifact archive is missing restored ledger members")
	}
	return nil
}

func preflightArtifactArchive(ctx context.Context, file *os.File, expected map[string]ArtifactRecord, expectedArchiveDigest string) error {
	archiveHash := sha256.New()
	tarReader := tar.NewReader(io.TeeReader(file, archiveHash))
	seen := make(map[string]bool, len(expected))
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("preflight artifact archive interrupted: %w", err)
		}
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read artifact archive: %w", err)
		}
		record, ok := expected[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != record.SizeBytes {
			return fmt.Errorf("artifact archive inventory does not match restored ledger")
		}
		seen[header.Name] = true
		bodyHash := sha256.New()
		written, copyErr := io.Copy(bodyHash, contextReader{ctx: ctx, reader: tarReader})
		if copyErr != nil || written != record.SizeBytes || "sha256:"+hex.EncodeToString(bodyHash.Sum(nil)) != record.Digest {
			return fmt.Errorf("artifact archive member failed digest verification")
		}
	}
	// tar.Reader stops at the end markers; include any signed trailing bytes in
	// the file commitment rather than accepting a digest of only the tar view.
	if _, err := io.Copy(archiveHash, contextReader{ctx: ctx, reader: file}); err != nil {
		return fmt.Errorf("hash artifact archive: %w", err)
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("artifact archive is missing restored ledger members")
	}
	if "sha256:"+hex.EncodeToString(archiveHash.Sum(nil)) != expectedArchiveDigest {
		return fmt.Errorf("artifact archive digest does not match manifest")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(body []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(body)
}

type countingWriter struct {
	writer  io.Writer
	written int64
}

func (w *countingWriter) Write(body []byte) (int, error) {
	n, err := w.writer.Write(body)
	w.written += int64(n)
	return n, err
}

func artifactAlreadyValid(ctx context.Context, store storage.Storage, record ArtifactRecord) (bool, error) {
	reader, err := store.GetReader(ctx, record.StorageKey)
	if err != nil {
		classifier, ok := store.(objectNotFoundClassifier)
		if ok && classifier.IsObjectNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("read existing artifact during restore")
	}
	h := sha256.New()
	read, copyErr := io.Copy(h, io.LimitReader(reader, record.SizeBytes+1))
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return false, fmt.Errorf("read existing artifact during restore")
	}
	if read == record.SizeBytes && "sha256:"+hex.EncodeToString(h.Sum(nil)) == record.Digest {
		return true, nil
	}
	return false, fmt.Errorf("existing artifact conflicts with restored digest ledger")
}

func validateArtifactInventory(records []ArtifactRecord) error {
	seen := map[string]bool{}
	var total int64
	for _, record := range records {
		if len(record.WorkspaceID) != 36 || !sha256Pattern.MatchString(record.Digest) || record.SizeBytes < 0 || record.SizeBytes > 1<<30 ||
			seen[record.WorkspaceID+"\x00"+record.Digest] || total > maxArtifactArchiveBytes-record.SizeBytes {
			return fmt.Errorf("invalid or oversized artifact inventory")
		}
		expectedKey := "role-source-artifacts/" + record.WorkspaceID + "/" + strings.TrimPrefix(record.Digest, "sha256:")
		if record.StorageKey != expectedKey {
			return fmt.Errorf("noncanonical artifact storage key")
		}
		seen[record.WorkspaceID+"\x00"+record.Digest] = true
		total += record.SizeBytes
	}
	return nil
}

func FileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func openExclusive(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}
