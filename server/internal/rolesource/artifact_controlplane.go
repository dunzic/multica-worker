package rolesource

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const artifactLookupBatch = 1_000

var (
	ErrArtifactMissing        = errors.New("role source snapshot references unavailable artifacts")
	ErrInvalidArtifactRequest = errors.New("invalid role source artifact request")
)

type ArtifactLeaseInput struct {
	WorkspaceID string
	SourceID    string
	RequestID   string
	RuntimeID   string
	LeaseToken  string
}

type StoreArtifactInput struct {
	ArtifactLeaseInput
	Digest     string
	SizeBytes  int64
	StorageKey string
}

// ListMissingArtifacts is the daemon preflight used to avoid uploading
// content already present in this workspace. It validates the active lease but
// does not reserve content; StoreArtifactRecord rechecks the lease after the
// bytes have reached object storage.
func (c *ControlPlane) ListMissingArtifacts(ctx context.Context, input ArtifactLeaseInput, refs []ArtifactRef) ([]ArtifactRef, error) {
	if len(refs) > artifactLookupBatch {
		return nil, fmt.Errorf("%w: preflight exceeds batch limit", ErrInvalidArtifactRequest)
	}
	workspaceID, sourceID, requestID, runtimeID, leaseToken, err := parseFiveUUIDs(
		input.WorkspaceID, input.SourceID, input.RequestID, input.RuntimeID, input.LeaseToken,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArtifactRequest, err)
	}
	seen := make(map[string]ArtifactRef, len(refs))
	for _, ref := range refs {
		if err := validateArtifact(ref); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidArtifactRequest, err)
		}
		if prior, ok := seen[ref.Digest]; ok && prior.SizeBytes != ref.SizeBytes {
			return nil, fmt.Errorf("%w: digest has conflicting sizes", ErrInvalidArtifactRequest)
		}
		seen[ref.Digest] = ref
	}
	q := c.queries()
	source, err := q.GetRoleSourceInWorkspace(ctx, db.GetRoleSourceInWorkspaceParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	request, err := q.GetRoleSourceScanRequest(ctx, db.GetRoleSourceScanRequestParams{ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	if !c.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		return nil, ErrScanLeaseLost
	}
	digests := make([]string, 0, len(seen))
	for digest := range seen {
		digests = append(digests, digest)
	}
	rows, err := q.ListRoleSourceArtifactsByDigests(ctx, db.ListRoleSourceArtifactsByDigestsParams{WorkspaceID: workspaceID, Digests: digests})
	if err != nil {
		return nil, err
	}
	present := make(map[string]int64, len(rows))
	for _, row := range rows {
		present[row.Digest] = row.SizeBytes
	}
	missing := make([]ArtifactRef, 0, len(seen)-len(rows))
	for _, ref := range refs {
		if size, ok := present[ref.Digest]; ok {
			if size != ref.SizeBytes {
				return nil, errors.New("persisted artifact size conflicts with requested digest")
			}
			continue
		}
		if seen[ref.Digest] == ref {
			missing = append(missing, ref)
			delete(seen, ref.Digest)
		}
	}
	return missing, nil
}

// StoreArtifactRecord makes successfully uploaded content visible to snapshot
// validation. Content storage is deterministic by digest; the database row is
// immutable and a concurrent exact upload simply returns the existing row.
func (c *ControlPlane) StoreArtifactRecord(ctx context.Context, input StoreArtifactInput) (db.RoleSourceArtifact, bool, error) {
	if !sha256Pattern.MatchString(input.Digest) || input.SizeBytes < 0 || input.SizeBytes > 1<<30 ||
		strings.TrimSpace(input.StorageKey) == "" || len(input.StorageKey) > 1024 {
		return db.RoleSourceArtifact{}, false, fmt.Errorf("%w: invalid artifact record", ErrInvalidArtifactRequest)
	}
	workspaceID, sourceID, requestID, runtimeID, leaseToken, err := parseFiveUUIDs(
		input.WorkspaceID, input.SourceID, input.RequestID, input.RuntimeID, input.LeaseToken,
	)
	if err != nil {
		return db.RoleSourceArtifact{}, false, fmt.Errorf("%w: %v", ErrInvalidArtifactRequest, err)
	}
	expectedStorageKey := fmt.Sprintf("role-source-artifacts/%s/%s", util.UUIDToString(workspaceID), strings.TrimPrefix(input.Digest, "sha256:"))
	if input.StorageKey != expectedStorageKey {
		return db.RoleSourceArtifact{}, false, fmt.Errorf("%w: noncanonical storage key", ErrInvalidArtifactRequest)
	}
	tx, err := c.database.Begin(ctx)
	if err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := db.New(tx)
	if _, err := qtx.LockWorkspaceForRoleSourceMutation(ctx, workspaceID); err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	source, err := qtx.GetRoleSourceForUpdate(ctx, db.GetRoleSourceForUpdateParams{ID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	request, err := qtx.GetRoleSourceScanRequestForUpdate(ctx, db.GetRoleSourceScanRequestForUpdateParams{ID: requestID, SourceID: sourceID, WorkspaceID: workspaceID})
	if err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	if !c.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		return db.RoleSourceArtifact{}, false, ErrScanLeaseLost
	}
	row, err := qtx.InsertRoleSourceArtifact(ctx, db.InsertRoleSourceArtifactParams{
		WorkspaceID: workspaceID, Digest: input.Digest, SizeBytes: input.SizeBytes, StorageKey: input.StorageKey,
		UploadedByRuntimeID: runtimeID, FirstSourceID: sourceID, FirstScanRequestID: requestID,
	})
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		row, err = qtx.GetRoleSourceArtifact(ctx, db.GetRoleSourceArtifactParams{WorkspaceID: workspaceID, Digest: input.Digest})
	}
	if err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	if row.SizeBytes != input.SizeBytes {
		return db.RoleSourceArtifact{}, false, errors.New("artifact digest conflicts with persisted size")
	}
	if err := tx.Commit(ctx); err != nil {
		return db.RoleSourceArtifact{}, false, err
	}
	return row, created, nil
}

func (c *ControlPlane) scanLeaseIsActive(source db.RoleSource, request db.RoleSourceScanRequest, runtimeID, leaseToken pgtype.UUID) bool {
	return source.RuntimeID == runtimeID && request.Status == "claimed" && request.ClaimedByRuntimeID == runtimeID &&
		request.LeaseToken == leaseToken && request.LeaseExpiresAt.Valid && request.LeaseExpiresAt.Time.After(c.now())
}

func verifySnapshotArtifacts(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, refs []ArtifactRef) error {
	for start := 0; start < len(refs); start += artifactLookupBatch {
		end := start + artifactLookupBatch
		if end > len(refs) {
			end = len(refs)
		}
		digests := make([]string, 0, end-start)
		wanted := make(map[string]int64, end-start)
		for _, ref := range refs[start:end] {
			digests = append(digests, ref.Digest)
			wanted[ref.Digest] = ref.SizeBytes
		}
		rows, err := q.ListRoleSourceArtifactsByDigests(ctx, db.ListRoleSourceArtifactsByDigestsParams{WorkspaceID: workspaceID, Digests: digests})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if size, ok := wanted[row.Digest]; ok && size == row.SizeBytes {
				delete(wanted, row.Digest)
			}
		}
		if len(wanted) > 0 {
			return fmt.Errorf("%w: %d missing or mismatched", ErrArtifactMissing, len(wanted))
		}
	}
	return nil
}
