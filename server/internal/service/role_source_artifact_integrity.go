package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	roleSourceArtifactIntegritySweep       = time.Minute
	roleSourceArtifactIntegrityLease       = 2 * time.Minute
	roleSourceArtifactIntegrityReadTimeout = 30 * time.Second
	roleSourceArtifactIntegrityHealthyTTL  = 7 * 24 * time.Hour
	roleSourceArtifactIntegrityBatch       = 100
	roleSourceArtifactIntegrityConcurrency = 8
)

const (
	artifactIntegrityHealthy        = "healthy"
	artifactIntegrityMissing        = "missing"
	artifactIntegritySizeMismatch   = "size_mismatch"
	artifactIntegrityDigestMismatch = "digest_mismatch"
	artifactIntegrityReadFailed     = "read_failed"
)

type roleSourceArtifactIntegrityReader interface {
	GetReader(context.Context, string) (io.ReadCloser, error)
}

type roleSourceArtifactNotFoundClassifier interface {
	IsObjectNotFound(error) bool
}

// RoleSourceArtifactIntegrityReconciler continuously reads content-addressed
// bodies back from storage. Confirmed absence or byte mismatch quarantines the
// matching readiness row through a separate state ledger. Transient read,
// authorization and close errors only retry; they never trigger mass repair.
type RoleSourceArtifactIntegrityReconciler struct {
	Queries *db.Queries
	Storage roleSourceArtifactIntegrityReader
	Logger  *slog.Logger
	Metrics *metrics.RoleSourceArtifactIntegrityMetrics

	readTimeout time.Duration
	batch       int
}

func (r *RoleSourceArtifactIntegrityReconciler) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *RoleSourceArtifactIntegrityReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(roleSourceArtifactIntegritySweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *RoleSourceArtifactIntegrityReconciler) RunOnce(ctx context.Context) {
	if r == nil || r.Queries == nil || r.Storage == nil {
		return
	}
	limit := r.batch
	if limit <= 0 || limit > 500 {
		limit = roleSourceArtifactIntegrityBatch
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(roleSourceArtifactIntegrityConcurrency)
	for range limit {
		token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
		row, err := r.Queries.ClaimNextRoleSourceArtifactIntegrity(groupCtx, db.ClaimNextRoleSourceArtifactIntegrityParams{
			LeaseToken: token, LeaseDuration: pgInterval(roleSourceArtifactIntegrityLease),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			if groupCtx.Err() == nil {
				r.logger().Warn("role source artifact integrity claim failed")
				if r.Metrics != nil {
					r.Metrics.RecordFailure("claim")
				}
			}
			break
		}
		group.Go(func() error {
			r.check(groupCtx, row, token)
			return nil
		})
	}
	_ = group.Wait()
	if r.Metrics != nil {
		counts, err := r.Queries.CountRoleSourceArtifactIntegrityStates(ctx)
		if err == nil {
			r.Metrics.Pending.Set(float64(counts.PendingCount))
			r.Metrics.Quarantined.Set(float64(counts.QuarantinedCount))
		} else if ctx.Err() == nil {
			r.Metrics.RecordFailure("count")
		}
	}
}

func (r *RoleSourceArtifactIntegrityReconciler) check(ctx context.Context, row db.RoleSourceArtifactIntegrity, token pgtype.UUID) {
	timeout := r.readTimeout
	if timeout <= 0 {
		timeout = roleSourceArtifactIntegrityReadTimeout
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	outcome, err := verifyRoleSourceArtifactBody(readCtx, r.Storage, row)
	cancel()
	if err != nil {
		r.release(ctx, row, token)
		return
	}
	if outcome == artifactIntegrityHealthy {
		writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer writeCancel()
		updated, updateErr := r.Queries.CompleteRoleSourceArtifactIntegrityHealthy(writeCtx, db.CompleteRoleSourceArtifactIntegrityHealthyParams{
			NextDelay: pgInterval(roleSourceArtifactIntegrityHealthyTTL), WorkspaceID: row.WorkspaceID,
			ArtifactDigest: row.ArtifactDigest, LeaseToken: token,
		})
		if updateErr != nil || updated != 1 {
			r.logger().Warn("role source artifact integrity completion failed")
			if r.Metrics != nil {
				r.Metrics.RecordFailure("complete")
			}
			return
		}
	} else {
		writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer writeCancel()
		updated, updateErr := r.Queries.QuarantineRoleSourceArtifactIntegrity(writeCtx, db.QuarantineRoleSourceArtifactIntegrityParams{
			Outcome: pgtype.Text{String: outcome, Valid: true}, WorkspaceID: row.WorkspaceID,
			ArtifactDigest: row.ArtifactDigest, LeaseToken: token,
		})
		if updateErr != nil || updated != 1 {
			r.logger().Warn("role source artifact integrity quarantine failed")
			if r.Metrics != nil {
				r.Metrics.RecordFailure("quarantine")
			}
			return
		}
		r.logger().Error("role source artifact body quarantined", "outcome", outcome)
	}
	if r.Metrics != nil {
		r.Metrics.RecordOutcome(outcome)
	}
}

func (r *RoleSourceArtifactIntegrityReconciler) release(ctx context.Context, row db.RoleSourceArtifactIntegrity, token pgtype.UUID) {
	attempt := max(int(row.Attempt), 1)
	delay := time.Minute << min(attempt-1, 6)
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer writeCancel()
	updated, err := r.Queries.ReleaseRoleSourceArtifactIntegrity(writeCtx, db.ReleaseRoleSourceArtifactIntegrityParams{
		RetryDelay: pgInterval(delay), WorkspaceID: row.WorkspaceID,
		ArtifactDigest: row.ArtifactDigest, LeaseToken: token,
	})
	if err != nil || updated != 1 {
		r.logger().Warn("role source artifact integrity release failed")
		if r.Metrics != nil {
			r.Metrics.RecordFailure("release")
		}
	} else if ctx.Err() == nil {
		r.logger().Warn("role source artifact integrity read failed; retained for bounded retry")
	}
	if updated == 1 && r.Metrics != nil {
		r.Metrics.RecordOutcome(artifactIntegrityReadFailed)
	}
}

func verifyRoleSourceArtifactBody(ctx context.Context, storage roleSourceArtifactIntegrityReader, row db.RoleSourceArtifactIntegrity) (string, error) {
	reader, err := storage.GetReader(ctx, row.StorageKey)
	if err != nil {
		if classifier, ok := storage.(roleSourceArtifactNotFoundClassifier); ok && classifier.IsObjectNotFound(err) {
			return artifactIntegrityMissing, nil
		}
		return "", fmt.Errorf("open artifact body: %w", err)
	}
	hash := sha256.New()
	readBytes, readErr := io.Copy(hash, io.LimitReader(reader, row.SizeBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return "", fmt.Errorf("read artifact body: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close artifact body: %w", closeErr)
	}
	if readBytes != row.SizeBytes {
		return artifactIntegritySizeMismatch, nil
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != row.ArtifactDigest {
		return artifactIntegrityDigestMismatch, nil
	}
	return artifactIntegrityHealthy, nil
}
