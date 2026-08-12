package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
}
