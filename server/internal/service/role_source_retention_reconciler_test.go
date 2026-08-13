package service

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeRoleSourceRetentionControl struct {
	queued    int
	queueErr  error
	pruned    int
	pruneErr  error
	lastID    pgtype.UUID
	lastToken pgtype.UUID
}

func (f *fakeRoleSourceRetentionControl) QueueRetentionCandidates(context.Context, int32) ([]db.RoleSourceRetentionCandidate, error) {
	f.queued++
	return nil, f.queueErr
}

func (f *fakeRoleSourceRetentionControl) PruneRetentionCandidate(_ context.Context, id, token pgtype.UUID) (string, error) {
	f.pruned++
	f.lastID, f.lastToken = id, token
	return "pruned", f.pruneErr
}

func TestRoleSourceRetentionReconcilerIsNilSafe(t *testing.T) {
	(&RoleSourceRetentionReconciler{}).RunOnce(context.Background())
}

func TestRoleSourceRetentionLeaseAndSweepAreOperationallyBounded(t *testing.T) {
	if roleSourceRetentionLease < time.Minute || roleSourceRetentionSweep < roleSourceRetentionLease {
		t.Fatalf("retention lease=%v sweep=%v", roleSourceRetentionLease, roleSourceRetentionSweep)
	}
	if roleSourceRetentionLimit < 1 || roleSourceRetentionLimit > 100 {
		t.Fatalf("retention per-sweep limit=%d", roleSourceRetentionLimit)
	}
}
