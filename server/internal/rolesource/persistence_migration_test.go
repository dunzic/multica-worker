package rolesource

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestRoleSourceMigrationsRespectRepositorySafetyRules(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "migrations", "2*_role_source_*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	if len(matches) < 20 {
		t.Fatalf("found %d role-source up migrations, want control-plane migration plus isolated indexes", len(matches))
	}
	indexPattern := regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b.*;\s*$`)
	for _, name := range matches {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		statements := []string{}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "--") {
				statements = append(statements, line)
			}
		}
		statementBody := strings.Join(statements, "\n")
		upper := strings.ToUpper(statementBody)
		if strings.Contains(upper, "REFERENCES ") || strings.Contains(upper, "FOREIGN KEY") || strings.Contains(upper, " ON DELETE ") {
			t.Fatalf("%s introduces a forbidden database relationship", name)
		}
		if strings.Contains(filepath.Base(name), "control_plane") {
			if strings.Contains(upper, "PRIMARY KEY") || strings.Contains(upper, "CREATE INDEX") || strings.Contains(upper, " UNIQUE ") {
				t.Fatalf("%s creates an inline/non-concurrent index", name)
			}
			continue
		}
		if !indexPattern.MatchString(statementBody) || strings.Count(statementBody, ";") != 1 {
			t.Fatalf("%s must contain exactly one CREATE INDEX CONCURRENTLY statement", name)
		}
	}
}

func TestRoleSourcePersistenceNeverStoresRawSourceConfig(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "273_role_source_control_plane.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(body))
	for _, forbidden := range []string{"root_path", "config_raw", "config_plaintext", "secret_value", "credential_value"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("control-plane schema contains forbidden raw source field %q", forbidden)
		}
	}
	for _, required := range []string{"daemon_config_id", "config_redacted", "snapshot_digest", "plan_digest", "event_digest", "lease_token"} {
		if !strings.Contains(schema, required) {
			t.Fatalf("control-plane schema is missing safety field %q", required)
		}
	}
}
