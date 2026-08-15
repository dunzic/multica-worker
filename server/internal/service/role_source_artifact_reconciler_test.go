package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
)

type roleSourcePurgeRecordingStorage struct {
	purges  int
	deletes int
}

func (s *roleSourcePurgeRecordingStorage) DeleteObject(context.Context, string) error {
	s.deletes++
	return nil
}

func (s *roleSourcePurgeRecordingStorage) PurgeObject(context.Context, string) error {
	s.purges++
	return nil
}

func (s *roleSourcePurgeRecordingStorage) PurgeObjectWithResult(context.Context, string) (storage.PermanentPurgeResult, error) {
	s.purges++
	return storage.PermanentPurgeResult{
		Backend: storage.PermanentPurgeBackendS3, Mode: storage.PermanentPurgeModeVersions,
		VersionsDeleted: 1, ObservedBytesDeleted: 64, VerifiedAbsent: true,
	}, nil
}

type roleSourceDeleteOnlyStorage struct{ deletes int }

type roleSourceGuardProbe struct{ calls int }

func (g *roleSourceGuardProbe) WithDestructive(ctx context.Context, fn func(context.Context) error) error {
	g.calls++
	return fn(ctx)
}

func (s *roleSourceDeleteOnlyStorage) DeleteObject(context.Context, string) error {
	s.deletes++
	return nil
}

func TestRoleSourceArtifactGCUsesVersionPurgingWhenAvailable(t *testing.T) {
	versioned := &roleSourcePurgeRecordingStorage{}
	result, err := purgeRoleSourceArtifactObject(context.Background(), versioned, "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if !result.VerifiedAbsent || result.VersionsDeleted != 1 || result.ObservedBytesDeleted != 64 {
		t.Fatalf("purge result = %+v", result)
	}
	if versioned.purges != 1 || versioned.deletes != 0 {
		t.Fatalf("version-aware storage calls: purges=%d deletes=%d, want 1/0", versioned.purges, versioned.deletes)
	}

	legacy := &roleSourceDeleteOnlyStorage{}
	if _, err := purgeRoleSourceArtifactObject(context.Background(), legacy, "artifact"); err == nil {
		t.Fatal("delete-only storage was accepted as a permanent role-source purge")
	}
	if legacy.deletes != 0 {
		t.Fatalf("delete-only storage calls = %d, want 0 fail-closed calls", legacy.deletes)
	}
}

func TestRoleSourceArtifactGCDeletionSafetyContract(t *testing.T) {
	if RoleSourceArtifactGCSettleDelay < 24*time.Hour {
		t.Fatalf("settle delay %v is too short for abandoned scan/upload recovery", RoleSourceArtifactGCSettleDelay)
	}
	if roleSourceArtifactGCDelete <= 0 || roleSourceArtifactGCDelete*2 > roleSourceArtifactGCLease {
		t.Fatalf("delete timeout %v must fit twice inside lease %v", roleSourceArtifactGCDelete, roleSourceArtifactGCLease)
	}
	if len(roleSourceArtifactGCTombstoneSchedule) < 4 {
		t.Fatal("late PUT protection requires a widening tombstone tail")
	}
	for index := 1; index < len(roleSourceArtifactGCTombstoneSchedule); index++ {
		if roleSourceArtifactGCTombstoneSchedule[index] <= roleSourceArtifactGCTombstoneSchedule[index-1] {
			t.Fatal("tombstone re-delete schedule must strictly widen")
		}
	}

	queries, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", "role_source.sql"))
	if err != nil {
		t.Fatal(err)
	}
	queryText := string(queries)
	for _, required := range []string{
		"NOT EXISTS (\n          SELECT 1 FROM role_source_snapshot_artifact",
		"FOR UPDATE OF artifact SKIP LOCKED",
		"INSERT INTO role_source_artifact_delete_intent",
		"DELETE FROM role_source_artifact artifact",
		"state = 'deleting' AND lease_token = @lease_token",
		"state IN ('pending', 'tombstoned')",
	} {
		if !strings.Contains(queryText, required) {
			t.Fatalf("artifact GC query contract is missing %q", required)
		}
	}
	for _, file := range []string{"workspace.sql", "workspace_delete.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		queueAt := strings.Index(text, "queued_role_source_artifact_deletes")
		deleteAt := strings.Index(text, "DELETE FROM role_source_artifact")
		if queueAt < 0 || deleteAt < 0 || queueAt > deleteAt {
			t.Fatalf("%s must persist delete intents before workspace artifact rows disappear", file)
		}
	}
	router, err := os.ReadFile(filepath.Join("..", "..", "cmd", "server", "router.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(router), `MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED`) {
		t.Fatal("artifact deletion worker must remain behind an independent default-off operator gate")
	}
	if !strings.Contains(string(router), `DRGuard: drlock.NewGuard(pool)`) {
		t.Fatal("permanent artifact deletion must be serialized against role-source DR backup")
	}
}
