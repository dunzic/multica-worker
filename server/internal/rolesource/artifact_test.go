package rolesource

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCollectArtifactRefsCanonicalizesAndRejectsDigestSizeConflicts(t *testing.T) {
	manifest := planTestManifest()
	manifest.Roles[0].Skills[0].Entrypoint.Digest = testSHA256("b")
	snapshot := planTestSnapshot(t, manifest)
	refs, err := CollectArtifactRefs(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Digest >= refs[1].Digest {
		t.Fatalf("canonical artifact refs = %+v", refs)
	}

	conflictManifest := planTestManifest()
	conflictManifest.Roles[0].Skills[0].Entrypoint.Digest = conflictManifest.Roles[0].Instructions.Digest
	conflictManifest.Roles[0].Skills[0].Entrypoint.SizeBytes = conflictManifest.Roles[0].Instructions.SizeBytes + 1
	conflicting := planTestSnapshot(t, conflictManifest)
	if _, err := CollectArtifactRefs(conflicting); err == nil {
		t.Fatal("artifact collector accepted one digest with conflicting sizes")
	}
}

func TestScanLeaseIsActiveRequiresExactUnexpiredOwner(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	runtimeID := util.MustParseUUID("00000000-0000-4000-8000-000000000071")
	leaseToken := util.MustParseUUID("00000000-0000-4000-8000-000000000072")
	controlPlane := &ControlPlane{now: func() time.Time { return now }}
	source := db.RoleSource{RuntimeID: runtimeID}
	request := db.RoleSourceScanRequest{
		Status: "claimed", ClaimedByRuntimeID: runtimeID, LeaseToken: leaseToken,
		LeaseExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
	}
	if !controlPlane.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		t.Fatal("exact unexpired owner was rejected")
	}
	request.LeaseExpiresAt.Time = now
	if controlPlane.scanLeaseIsActive(source, request, runtimeID, leaseToken) {
		t.Fatal("expired lease was accepted")
	}
}
