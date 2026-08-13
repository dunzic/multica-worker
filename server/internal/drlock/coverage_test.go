package drlock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryDestructiveRoleSourcePathParticipatesInDRProtocol(t *testing.T) {
	files := map[string][]string{
		filepath.Join("..", "rolesource", "retention.go"):                    {"pg_advisory_xact_lock_shared", "PruneRetentionCandidate"},
		filepath.Join("..", "handler", "workspace.go"):                       {"pg_advisory_xact_lock_shared", "DeleteWorkspaceRoleSources"},
		filepath.Join("..", "service", "role_source_artifact_reconciler.go"): {"DRGuard.WithDestructive", "PurgeObject"},
		filepath.Join("..", "..", "cmd", "role_source_dr", "main.go"):        {"pg_advisory_lock($1)", "AdvisoryLockKey"},
	}
	for path, contracts := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, contract := range contracts {
			if !strings.Contains(string(body), contract) {
				t.Errorf("%s omits DR contract %q", path, contract)
			}
		}
	}
}
