package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseConfigFailsBeforeDatabaseAccess(t *testing.T) {
	if _, err := parseConfig(nil); err == nil {
		t.Fatal("missing capacity scope accepted")
	}
	t.Setenv("DATABASE_URL", "")
	err := run(context.Background(), []string{
		"--workspace-id", "00000000-0000-4000-8000-000000000001",
		"--runtime-id", "00000000-0000-4000-8000-000000000002",
		"--report", t.TempDir() + "/report.json",
		"--minimum-workspace-members", "1", "--minimum-workspace-runtimes", "1",
		"--minimum-workspace-sources", "1", "--minimum-attestation-history", "1",
	})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run error = %v", err)
	}
}

func TestLatencyQuantilesUseNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for index := range values {
		values[index] = time.Duration(index+1) * time.Millisecond
	}
	result := summarizeLatency(values, 2)
	if result.P50Millis != 50 || result.P95Millis != 95 || result.P99Millis != 99 || result.MaximumMillis != 100 || result.Failures != 2 {
		t.Fatalf("latency summary = %+v", result)
	}
}

func TestPlanSummaryExportsOnlySafeEvidence(t *testing.T) {
	raw := []byte(`[{
		"Plan":{"Node Type":"Index Scan","Actual Rows":100,"Index Name":"role_source_workspace_idx","Index Cond":"workspace_id = 'secret-tenant'","Shared Hit Blocks":4,"Plans":[{"Node Type":"Index Scan","Index Name":"Bad Index / tenant","Shared Read Blocks":3}]},
		"Planning Time":1.25,"Execution Time":2.5
	}]`)
	result, err := summarizePlan("source_list", raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "secret-tenant") || strings.Contains(string(encoded), "Bad Index") {
		t.Fatalf("plan leaked filters or unsafe names: %s", encoded)
	}
	if len(result.Indexes) != 1 || result.Indexes[0] != "role_source_workspace_idx" || result.SharedHitBlocks != 4 || result.SharedReadBlocks != 3 {
		t.Fatalf("plan summary = %+v", result)
	}
}

func TestReportUsesExclusivePrivateFile(t *testing.T) {
	path := t.TempDir() + "/capacity.json"
	value := report{ContractVersion: capacityContract, Status: "failed"}
	if err := writeReportExclusive(path, value); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode = %o", info.Mode().Perm())
	}
	if err := writeReportExclusive(path, value); err == nil {
		t.Fatal("capacity evidence was overwritten")
	}
}

func TestCapacityProbeSourceHasNoMutationStatements(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(body))
	for _, forbidden := range []string{"insert into ", "update role_source", "delete from ", "truncate ", "create table ", "drop table "} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("read-only capacity probe contains mutation token %q", forbidden)
		}
	}
	for _, required := range []string{"default_transaction_read_only", "statement_timeout", "EXPLAIN (ANALYZE, BUFFERS, WAL, TIMING OFF, FORMAT JSON)"} {
		if !strings.Contains(string(body), required) {
			t.Errorf("capacity probe omits safety/evidence contract %q", required)
		}
	}
}
