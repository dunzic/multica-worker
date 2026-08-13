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
// by the restored database ledger. It refuses extra, missing, duplicate or
// digest-invalid members and never mutates PostgreSQL.
func RestoreArtifactArchive(ctx context.Context, source string, records []ArtifactRecord, store storage.Storage) error {
	if err := validateArtifactInventory(records); err != nil {
		return err
	}
	uploader, ok := store.(streamUploader)
	if !ok {
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
	tarReader := tar.NewReader(file)
	seen := map[string]bool{}
	for {
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
		temporary, err := os.CreateTemp("", "multica-role-source-restore-*")
		if err != nil {
			return fmt.Errorf("create private restore spool: %w", err)
		}
		temporaryPath := temporary.Name()
		bodyHash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(temporary, bodyHash), io.LimitReader(tarReader, header.Size+1))
		closeErr := temporary.Close()
		if copyErr != nil || closeErr != nil || written != record.SizeBytes || "sha256:"+hex.EncodeToString(bodyHash.Sum(nil)) != record.Digest {
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("artifact archive member failed digest verification")
		}
		present, err := artifactAlreadyValid(ctx, store, record)
		if err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
		if !present {
			body, err := os.Open(temporaryPath)
			if err != nil {
				_ = os.Remove(temporaryPath)
				return fmt.Errorf("open private restore spool: %w", err)
			}
			_, uploadErr := uploader.UploadStream(ctx, record.StorageKey, body, record.SizeBytes, "application/octet-stream", "")
			body.Close()
			if uploadErr != nil {
				_ = os.Remove(temporaryPath)
				return fmt.Errorf("restore artifact upload: %w", uploadErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("artifact archive is missing restored ledger members")
	}
	return nil
}

func artifactAlreadyValid(ctx context.Context, store storage.Storage, record ArtifactRecord) (bool, error) {
	reader, err := store.GetReader(ctx, record.StorageKey)
	if err != nil {
		return false, nil
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
