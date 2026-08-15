// role_source_capacity records content-free read-path capacity evidence from a
// production-shaped PostgreSQL 17 staging database. It never mutates data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var commit = "unknown"

const (
	capacityContract = "role-source-capacity-evidence-v1"
	maxSamples       = 10000
	maxConcurrency   = 128
	queryTimeout     = 5 * time.Second
)

type config struct {
	workspaceID uuid.UUID
	runtimeID   uuid.UUID
	reportPath  string
	samples     int
	concurrency int
	minimum     dataset
}

type dataset struct {
	Users               int64 `json:"users"`
	WorkspaceMembers    int64 `json:"workspace_members"`
	WorkspaceRuntimes   int64 `json:"workspace_runtimes"`
	WorkspaceSources    int64 `json:"workspace_sources"`
	CurrentAttestations int64 `json:"current_attestations"`
	AttestationHistory  int64 `json:"attestation_history"`
}

type latency struct {
	Samples       int     `json:"samples"`
	Failures      int     `json:"failures"`
	P50Millis     float64 `json:"p50_ms"`
	P95Millis     float64 `json:"p95_ms"`
	P99Millis     float64 `json:"p99_ms"`
	MaximumMillis float64 `json:"maximum_ms"`
}

type planEvidence struct {
	Name             string   `json:"name"`
	PlanningMillis   float64  `json:"planning_ms"`
	ExecutionMillis  float64  `json:"execution_ms"`
	ActualRows       float64  `json:"actual_rows"`
	SharedHitBlocks  float64  `json:"shared_hit_blocks"`
	SharedReadBlocks float64  `json:"shared_read_blocks"`
	Indexes          []string `json:"indexes"`
}

type report struct {
	ContractVersion     string         `json:"contract_version"`
	CheckedAt           time.Time      `json:"checked_at"`
	Commit              string         `json:"commit"`
	Status              string         `json:"status"`
	PostgreSQLMajor     int            `json:"postgresql_major"`
	MigrationCount      int64          `json:"migration_count"`
	Dataset             dataset        `json:"dataset"`
	MinimumDataset      dataset        `json:"minimum_dataset"`
	SourceList          latency        `json:"source_list"`
	AttestationHistory  latency        `json:"attestation_history"`
	MaximumAcquiredConn int32          `json:"maximum_acquired_connections"`
	PoolMaximum         int32          `json:"pool_maximum_connections"`
	Plans               []planEvidence `json:"plans"`
	Findings            []string       `json:"findings"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "role-source capacity:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := parseConfig(args)
	if err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required; defaults are forbidden for capacity evidence")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	if poolConfig.MaxConns < int32(cfg.concurrency) {
		poolConfig.MaxConns = int32(cfg.concurrency)
	}
	poolConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", queryTimeout.Milliseconds())
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "multica_role_source_capacity_read_only"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("connect capacity database")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("ping capacity database")
	}

	evidence, err := collect(ctx, pool, cfg)
	if err != nil {
		return err
	}
	if err := writeReportExclusive(cfg.reportPath, evidence); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"status": evidence.Status, "samples": cfg.samples,
		"findings": len(evidence.Findings), "report_written": true,
	})
	fmt.Println(string(body))
	if evidence.Status != "read_path_passed" {
		return fmt.Errorf("capacity read path failed with %d finding classes", len(evidence.Findings))
	}
	return nil
}

func parseConfig(args []string) (config, error) {
	flags := flag.NewFlagSet("role_source_capacity", flag.ContinueOnError)
	workspace := flags.String("workspace-id", "", "largest production-shaped staging workspace UUID")
	runtime := flags.String("runtime-id", "", "staging runtime UUID with attestation history")
	reportPath := flags.String("report", "", "new mode-0600 JSON evidence path")
	samples := flags.Int("samples", 500, "samples per read path")
	concurrency := flags.Int("concurrency", 32, "concurrent read workers")
	minimumUsers := flags.Int64("minimum-users", 10000, "minimum global staged users")
	minimumMembers := flags.Int64("minimum-workspace-members", 0, "approved cohort minimum members in selected workspace")
	minimumRuntimes := flags.Int64("minimum-workspace-runtimes", 0, "approved cohort minimum runtimes in selected workspace")
	minimumSources := flags.Int64("minimum-workspace-sources", 0, "approved cohort minimum sources in selected workspace")
	minimumHistory := flags.Int64("minimum-attestation-history", 0, "approved cohort minimum history rows for selected runtime")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if *reportPath == "" || *samples < 1 || *samples > maxSamples || *concurrency < 1 || *concurrency > maxConcurrency ||
		*minimumUsers < 1 || *minimumMembers < 1 || *minimumRuntimes < 1 || *minimumSources < 1 || *minimumHistory < 1 {
		return config{}, errors.New("valid --workspace-id, --runtime-id, --report, sample/concurrency and minimum values are required")
	}
	if extra := flags.Args(); len(extra) != 0 {
		return config{}, errors.New("unexpected positional arguments")
	}
	workspaceID, err := uuid.Parse(*workspace)
	if err != nil {
		return config{}, errors.New("--workspace-id must be a UUID")
	}
	runtimeID, err := uuid.Parse(*runtime)
	if err != nil {
		return config{}, errors.New("--runtime-id must be a UUID")
	}
	return config{
		workspaceID: workspaceID, runtimeID: runtimeID, reportPath: *reportPath,
		samples: *samples, concurrency: *concurrency,
		minimum: dataset{Users: *minimumUsers, WorkspaceMembers: *minimumMembers,
			WorkspaceRuntimes: *minimumRuntimes, WorkspaceSources: *minimumSources,
			CurrentAttestations: 1, AttestationHistory: *minimumHistory},
	}, nil
}

func collect(ctx context.Context, pool *pgxpool.Pool, cfg config) (report, error) {
	evidence := report{ContractVersion: capacityContract, CheckedAt: time.Now().UTC(), Commit: commit,
		Status: "failed", MinimumDataset: cfg.minimum, PoolMaximum: pool.Config().MaxConns}
	if err := pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&evidence.PostgreSQLMajor); err != nil {
		return report{}, errors.New("read PostgreSQL version")
	}
	if evidence.PostgreSQLMajor != 17 {
		return report{}, fmt.Errorf("PostgreSQL 17 is required, got major %d", evidence.PostgreSQLMajor)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&evidence.MigrationCount); err != nil {
		return report{}, errors.New("read migration count")
	}
	if err := readDataset(ctx, pool, cfg, &evidence.Dataset); err != nil {
		return report{}, err
	}
	if evidence.Dataset.Users < cfg.minimum.Users || evidence.Dataset.WorkspaceMembers < cfg.minimum.WorkspaceMembers ||
		evidence.Dataset.WorkspaceRuntimes < cfg.minimum.WorkspaceRuntimes || evidence.Dataset.WorkspaceSources < cfg.minimum.WorkspaceSources ||
		evidence.Dataset.CurrentAttestations < cfg.minimum.CurrentAttestations || evidence.Dataset.AttestationHistory < cfg.minimum.AttestationHistory {
		evidence.Findings = append(evidence.Findings, "dataset_below_declared_minimum")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(commit) {
		evidence.Findings = append(evidence.Findings, "build_commit_unset")
	}

	if err := runWarmup(ctx, pool, cfg); err != nil {
		return report{}, err
	}
	var maxAcquired atomic.Int32
	evidence.SourceList = measure(ctx, pool, cfg.samples, cfg.concurrency, &maxAcquired, func(ctx context.Context, conn *pgxpool.Conn) error {
		return sourceListProbeConn(ctx, conn, cfg.workspaceID)
	})
	evidence.AttestationHistory = measure(ctx, pool, cfg.samples, cfg.concurrency, &maxAcquired, func(ctx context.Context, conn *pgxpool.Conn) error {
		return historyProbeConn(ctx, conn, cfg.workspaceID, cfg.runtimeID)
	})
	evidence.MaximumAcquiredConn = maxAcquired.Load()
	if evidence.SourceList.Failures > 0 || evidence.AttestationHistory.Failures > 0 {
		evidence.Findings = append(evidence.Findings, "read_query_failure")
	}
	if evidence.SourceList.P99Millis >= 500 {
		evidence.Findings = append(evidence.Findings, "source_list_p99_slo_missed")
	}
	if evidence.AttestationHistory.P99Millis >= 500 {
		evidence.Findings = append(evidence.Findings, "attestation_history_p99_slo_missed")
	}

	plans, err := collectPlans(ctx, pool, cfg)
	if err != nil {
		return report{}, err
	}
	evidence.Plans = plans
	requiredIndexes := map[string][]string{
		"source_list":         {"role_source_workspace_idx"},
		"current_attestation": {"role_source_runtime_attestation_workspace_index", "role_source_runtime_attestation_runtime_unique"},
		"runtime_batch":       {"agent_runtime_pkey"},
		"attestation_history": {"role_source_runtime_attestation_observation_index"},
	}
	for _, plan := range plans {
		if !hasAnyIndex(plan.Indexes, requiredIndexes[plan.Name]) {
			evidence.Findings = append(evidence.Findings, plan.Name+"_required_index_not_used")
		}
	}
	sort.Strings(evidence.Findings)
	if len(evidence.Findings) == 0 {
		evidence.Status = "read_path_passed"
	}
	return evidence, nil
}

func readDataset(ctx context.Context, pool *pgxpool.Pool, cfg config, result *dataset) error {
	row := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM "user"),
       (SELECT count(*) FROM member WHERE workspace_id=$1),
       (SELECT count(*) FROM agent_runtime WHERE workspace_id=$1),
       (SELECT count(*) FROM role_source WHERE workspace_id=$1),
       (SELECT count(*) FROM role_source_runtime_attestation WHERE workspace_id=$1),
       (SELECT count(*) FROM role_source_runtime_attestation_observation WHERE workspace_id=$1 AND runtime_id=$2),
       EXISTS (SELECT 1 FROM agent_runtime WHERE workspace_id=$1 AND id=$2)
`, cfg.workspaceID, cfg.runtimeID)
	var runtimeBelongs bool
	if err := row.Scan(&result.Users, &result.WorkspaceMembers, &result.WorkspaceRuntimes, &result.WorkspaceSources,
		&result.CurrentAttestations, &result.AttestationHistory, &runtimeBelongs); err != nil {
		return errors.New("read capacity dataset")
	}
	if !runtimeBelongs {
		return errors.New("selected runtime does not belong to selected workspace")
	}
	return nil
}

func sourceListProbe(ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return sourceListProbeConn(ctx, conn, workspaceID)
}

func sourceListProbeConn(ctx context.Context, conn *pgxpool.Conn, workspaceID uuid.UUID) error {
	workspace := pgtype.UUID{Bytes: workspaceID, Valid: true}
	rows, err := conn.Query(ctx, `SELECT * FROM role_source WHERE workspace_id=$1 ORDER BY created_at DESC, id`, workspaceID)
	if err != nil {
		return err
	}
	sources, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.RoleSource])
	if err != nil {
		return err
	}
	runtimeIDs := make([]pgtype.UUID, 0, len(sources))
	for _, source := range sources {
		runtimeIDs = append(runtimeIDs, source.RuntimeID)
	}
	queries := db.New(conn)
	if _, err := queries.ListRoleSourceRuntimeAttestations(ctx, db.ListRoleSourceRuntimeAttestationsParams{WorkspaceID: workspace, RuntimeIds: runtimeIDs}); err != nil {
		return err
	}
	_, err = queries.GetAgentRuntimes(ctx, runtimeIDs)
	return err
}

func historyProbe(ctx context.Context, pool *pgxpool.Pool, workspaceID, runtimeID uuid.UUID) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return historyProbeConn(ctx, conn, workspaceID, runtimeID)
}

func historyProbeConn(ctx context.Context, conn *pgxpool.Conn, workspaceID, runtimeID uuid.UUID) error {
	rows, err := conn.Query(ctx, `SELECT * FROM role_source_runtime_attestation_observation WHERE workspace_id=$1 AND runtime_id=$2 ORDER BY last_observed_at DESC, attestation_id LIMIT 100`, workspaceID, runtimeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, err := rows.Values(); err != nil {
			return err
		}
	}
	return rows.Err()
}

func runWarmup(ctx context.Context, pool *pgxpool.Pool, cfg config) error {
	var workers sync.WaitGroup
	errorsOut := make(chan error, cfg.concurrency)
	for index := 0; index < cfg.concurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := sourceListProbe(ctx, pool, cfg.workspaceID); err != nil {
				errorsOut <- errors.New("source-list warmup failed")
				return
			}
			if err := historyProbe(ctx, pool, cfg.workspaceID, cfg.runtimeID); err != nil {
				errorsOut <- errors.New("attestation-history warmup failed")
			}
		}()
	}
	workers.Wait()
	close(errorsOut)
	for err := range errorsOut {
		return err
	}
	return nil
}

func measure(ctx context.Context, pool *pgxpool.Pool, samples, concurrency int, maxAcquired *atomic.Int32, probe func(context.Context, *pgxpool.Conn) error) latency {
	durations := make([]time.Duration, samples)
	jobs := make(chan int)
	var failures atomic.Int32
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				started := time.Now()
				queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
				conn, err := pool.Acquire(queryCtx)
				if err == nil {
					updateMaximum(maxAcquired, pool.Stat().AcquiredConns())
					err = probe(queryCtx, conn)
					conn.Release()
				}
				cancel()
				durations[index] = time.Since(started)
				if err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	for index := 0; index < samples; index++ {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return summarizeLatency(durations, int(failures.Load()))
}

func updateMaximum(value *atomic.Int32, candidate int32) {
	for current := value.Load(); candidate > current && !value.CompareAndSwap(current, candidate); current = value.Load() {
	}
}

func summarizeLatency(values []time.Duration, failures int) latency {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	millis := func(value time.Duration) float64 { return math.Round(float64(value.Microseconds())/10) / 100 }
	quantile := func(q float64) time.Duration {
		if len(values) == 0 {
			return 0
		}
		index := int(math.Ceil(q*float64(len(values)))) - 1
		return values[max(0, index)]
	}
	result := latency{Samples: len(values), Failures: failures, P50Millis: millis(quantile(.50)), P95Millis: millis(quantile(.95)), P99Millis: millis(quantile(.99))}
	if len(values) > 0 {
		result.MaximumMillis = millis(values[len(values)-1])
	}
	return result
}

func collectPlans(ctx context.Context, pool *pgxpool.Pool, cfg config) ([]planEvidence, error) {
	probes := []struct {
		name      string
		statement string
		args      []any
	}{
		{"source_list", `SELECT * FROM role_source WHERE workspace_id=$1 ORDER BY created_at DESC, id`, []any{cfg.workspaceID}},
		{"current_attestation", `SELECT * FROM role_source_runtime_attestation WHERE workspace_id=$1 AND runtime_id=ANY($2::uuid[]) ORDER BY runtime_id`, []any{cfg.workspaceID, []uuid.UUID{cfg.runtimeID}}},
		{"runtime_batch", `SELECT * FROM agent_runtime WHERE id=ANY($1::uuid[])`, []any{[]uuid.UUID{cfg.runtimeID}}},
		{"attestation_history", `SELECT * FROM role_source_runtime_attestation_observation WHERE workspace_id=$1 AND runtime_id=$2 ORDER BY last_observed_at DESC, attestation_id LIMIT 100`, []any{cfg.workspaceID, cfg.runtimeID}},
	}
	result := make([]planEvidence, 0, len(probes))
	for _, probe := range probes {
		var raw []byte
		statement := "EXPLAIN (ANALYZE, BUFFERS, WAL, TIMING OFF, FORMAT JSON) " + probe.statement
		if err := pool.QueryRow(ctx, statement, probe.args...).Scan(&raw); err != nil {
			return nil, fmt.Errorf("collect %s execution plan", probe.name)
		}
		plan, err := summarizePlan(probe.name, raw)
		if err != nil {
			return nil, err
		}
		result = append(result, plan)
	}
	return result, nil
}

var safeIndexName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

func summarizePlan(name string, raw []byte) (planEvidence, error) {
	var documents []map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&documents); err != nil || len(documents) != 1 {
		return planEvidence{}, fmt.Errorf("decode %s execution plan", name)
	}
	root, ok := documents[0]["Plan"].(map[string]any)
	if !ok {
		return planEvidence{}, fmt.Errorf("decode %s execution plan root", name)
	}
	result := planEvidence{Name: name, PlanningMillis: number(documents[0]["Planning Time"]), ExecutionMillis: number(documents[0]["Execution Time"]), ActualRows: number(root["Actual Rows"])}
	indexes := map[string]bool{}
	walkPlan(root, &result, indexes)
	for index := range indexes {
		result.Indexes = append(result.Indexes, index)
	}
	sort.Strings(result.Indexes)
	return result, nil
}

func walkPlan(node map[string]any, result *planEvidence, indexes map[string]bool) {
	result.SharedHitBlocks += number(node["Shared Hit Blocks"])
	result.SharedReadBlocks += number(node["Shared Read Blocks"])
	if index, ok := node["Index Name"].(string); ok && safeIndexName.MatchString(index) {
		indexes[index] = true
	}
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		if childNode, ok := child.(map[string]any); ok {
			walkPlan(childNode, result, indexes)
		}
	}
}

func number(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		result, _ := typed.Float64()
		return result
	case float64:
		return typed
	default:
		return 0
	}
}

func hasAnyIndex(actual, alternatives []string) bool {
	for _, candidate := range alternatives {
		if slices.Contains(actual, candidate) {
			return true
		}
	}
	return false
}

func writeReportExclusive(path string, value report) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(path)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	committed = true
	return nil
}
