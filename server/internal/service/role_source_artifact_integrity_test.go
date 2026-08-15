package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errIntegrityMissing = errors.New("missing")

type integrityStorage struct {
	body []byte
	err  error
}

func (s integrityStorage) GetReader(context.Context, string) (io.ReadCloser, error) {
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func (integrityStorage) IsObjectNotFound(err error) bool { return errors.Is(err, errIntegrityMissing) }

func integrityRow(body []byte) db.RoleSourceArtifactIntegrity {
	sum := sha256.Sum256(body)
	return db.RoleSourceArtifactIntegrity{
		WorkspaceID: pgtype.UUID{Valid: true}, ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]),
		StorageKey: "role-source-artifacts/workspace/digest", SizeBytes: int64(len(body)),
	}
}

func TestVerifyRoleSourceArtifactBodyClassifiesIntegrityOutcomes(t *testing.T) {
	body := []byte("safe body")
	row := integrityRow(body)
	tests := []struct {
		name    string
		storage integrityStorage
		row     db.RoleSourceArtifactIntegrity
		outcome string
		wantErr bool
	}{
		{name: "healthy", storage: integrityStorage{body: body}, row: row, outcome: artifactIntegrityHealthy},
		{name: "missing", storage: integrityStorage{err: errIntegrityMissing}, row: row, outcome: artifactIntegrityMissing},
		{name: "transient", storage: integrityStorage{err: errors.New("unavailable")}, row: row, wantErr: true},
		{name: "size", storage: integrityStorage{body: append(body, '!')}, row: row, outcome: artifactIntegritySizeMismatch},
		{name: "digest", storage: integrityStorage{body: []byte("bad-body")}, row: func() db.RoleSourceArtifactIntegrity {
			copy := row
			copy.SizeBytes = int64(len("bad-body"))
			return copy
		}(), outcome: artifactIntegrityDigestMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, err := verifyRoleSourceArtifactBody(t.Context(), test.storage, test.row)
			if test.wantErr {
				if err == nil || outcome != "" {
					t.Fatalf("outcome=%q err=%v, want transient error", outcome, err)
				}
				return
			}
			if err != nil || outcome != test.outcome {
				t.Fatalf("outcome=%q err=%v, want %q", outcome, err, test.outcome)
			}
		})
	}
}

func TestRoleSourceArtifactIntegritySafetyConstants(t *testing.T) {
	if roleSourceArtifactIntegrityConcurrency < 1 || roleSourceArtifactIntegrityConcurrency > 16 {
		t.Fatalf("unsafe concurrency %d", roleSourceArtifactIntegrityConcurrency)
	}
	if roleSourceArtifactIntegrityBatch < roleSourceArtifactIntegrityConcurrency || roleSourceArtifactIntegrityBatch > 500 {
		t.Fatalf("unsafe batch %d", roleSourceArtifactIntegrityBatch)
	}
	if roleSourceArtifactIntegrityReadTimeout <= 0 || roleSourceArtifactIntegrityReadTimeout*2 > roleSourceArtifactIntegrityLease {
		t.Fatalf("read timeout %v does not fit lease %v", roleSourceArtifactIntegrityReadTimeout, roleSourceArtifactIntegrityLease)
	}
	if roleSourceArtifactIntegrityHealthyTTL < 24*time.Hour {
		t.Fatalf("healthy interval %v is too aggressive", roleSourceArtifactIntegrityHealthyTTL)
	}
}
