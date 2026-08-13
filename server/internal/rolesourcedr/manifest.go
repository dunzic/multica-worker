package rolesourcedr

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ArtifactRecord struct {
	WorkspaceID string
	Digest      string
	SizeBytes   int64
	StorageKey  string
}

// BuildManifest streams canonical PostgreSQL JSON text rather than loading
// table contents into memory. The output contains counts and commitments only;
// snapshot bodies, config summaries, request keys and ciphertext are absent.
func BuildManifest(ctx context.Context, q queryer) (Manifest, []ArtifactRecord, error) {
	backupID, err := uuid.NewRandom()
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("generate backup identity: %w", err)
	}
	manifest := Manifest{ContractVersion: ContractVersion, BackupID: backupID.String()}
	var timezone, byteaOutput, dateStyle, intervalStyle string
	if err := q.QueryRow(ctx, `
SELECT set_config('TimeZone', 'UTC', true),
       set_config('bytea_output', 'hex', true),
       set_config('DateStyle', 'ISO, YMD', true),
       set_config('IntervalStyle', 'iso_8601', true)`).Scan(&timezone, &byteaOutput, &dateStyle, &intervalStyle); err != nil {
		return Manifest{}, nil, fmt.Errorf("set deterministic database rendering: %w", err)
	}
	if err := q.QueryRow(ctx, "SELECT clock_timestamp(), current_setting('server_version_num')::int / 10000").Scan(&manifest.CreatedAt, &manifest.DatabaseMajorVersion); err != nil {
		return Manifest{}, nil, fmt.Errorf("read database identity: %w", err)
	}
	versions, err := stringColumn(ctx, q, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("read schema migrations: %w", err)
	}
	manifest.MigrationCount = int64(len(versions))
	manifest.MigrationRoot = commitment(versions)

	manifest.Tables = make([]TableSummary, 0, len(tableNames))
	for _, table := range tableNames {
		summary, err := summarizeTable(ctx, q, table)
		if err != nil {
			return Manifest{}, nil, err
		}
		manifest.Tables = append(manifest.Tables, summary)
	}
	artifacts, err := listArtifactRecords(ctx, q)
	if err != nil {
		return Manifest{}, nil, err
	}
	artifactValues := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		manifest.Artifacts.SizeBytes += artifact.SizeBytes
		artifactValues = append(artifactValues, fmt.Sprintf("%s\x00%s\x00%d\x00%s", artifact.WorkspaceID, artifact.Digest, artifact.SizeBytes, artifact.StorageKey))
	}
	manifest.Artifacts.Count = int64(len(artifacts))
	manifest.Artifacts.Commitment = commitment(artifactValues)

	if err := q.QueryRow(ctx, `
SELECT COALESCE(array_agg(DISTINCT key_id ORDER BY key_id), ARRAY[]::text[]),
       count(*) FILTER (WHERE status IN ('pending','claimed')),
       count(*) FILTER (WHERE status = 'submitted')
FROM role_source_secret_transfer
WHERE status IN ('pending','claimed','submitted') AND expires_at > $1
`, manifest.CreatedAt).Scan(&manifest.KeyRequirements.SecretTransferKeyIDs, &manifest.KeyRequirements.PendingTransfers, &manifest.KeyRequirements.SubmittedTransfers); err != nil {
		return Manifest{}, nil, fmt.Errorf("summarize secret-transfer key requirements: %w", err)
	}
	manifest.CreatedAt = manifest.CreatedAt.UTC()
	return manifest, artifacts, nil
}

func summarizeTable(ctx context.Context, q queryer, table string) (TableSummary, error) {
	if !knownTable(table) {
		return TableSummary{}, fmt.Errorf("refuse unknown role-source table %q", table)
	}
	rows, err := q.Query(ctx, "SELECT to_jsonb(row_value)::text FROM "+pgx.Identifier{table}.Sanitize()+" AS row_value ORDER BY to_jsonb(row_value)::text")
	if err != nil {
		return TableSummary{}, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	digest := sha256.New()
	var count int64
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return TableSummary{}, fmt.Errorf("scan %s: %w", table, err)
		}
		writeFramed(digest, value)
		count++
	}
	if err := rows.Err(); err != nil {
		return TableSummary{}, fmt.Errorf("stream %s: %w", table, err)
	}
	return TableSummary{Name: table, RowCount: count, Commitment: sumString(digest)}, nil
}

func listArtifactRecords(ctx context.Context, q queryer) ([]ArtifactRecord, error) {
	rows, err := q.Query(ctx, `
SELECT workspace_id::text, digest, size_bytes, storage_key
FROM role_source_artifact
ORDER BY workspace_id, digest`)
	if err != nil {
		return nil, fmt.Errorf("read role-source artifact inventory: %w", err)
	}
	defer rows.Close()
	artifacts := []ArtifactRecord{}
	for rows.Next() {
		var record ArtifactRecord
		if err := rows.Scan(&record.WorkspaceID, &record.Digest, &record.SizeBytes, &record.StorageKey); err != nil {
			return nil, fmt.Errorf("scan role-source artifact inventory: %w", err)
		}
		artifacts = append(artifacts, record)
	}
	return artifacts, rows.Err()
}

func stringColumn(ctx context.Context, q queryer, statement string) ([]string, error) {
	rows, err := q.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func commitment(values []string) string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	digest := sha256.New()
	for _, value := range values {
		writeFramed(digest, value)
	}
	return sumString(digest)
}

func writeFramed(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func sumString(digest hash.Hash) string {
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func knownTable(table string) bool {
	for _, candidate := range tableNames {
		if table == candidate {
			return true
		}
	}
	return false
}

// SameDatabaseState compares backup-authoritative PostgreSQL and artifact
// state. Backup identity/time and time-sensitive key advice are excluded; the
// exact transfer rows remain covered by the table commitment.
func SameDatabaseState(expected, actual Manifest) bool {
	if expected.ContractVersion != ContractVersion || expected.DatabaseMajorVersion != actual.DatabaseMajorVersion ||
		expected.MigrationCount != actual.MigrationCount ||
		expected.MigrationRoot != actual.MigrationRoot || expected.Artifacts != actual.Artifacts ||
		len(expected.Tables) != len(actual.Tables) {
		return false
	}
	for index := range expected.Tables {
		if expected.Tables[index] != actual.Tables[index] {
			return false
		}
	}
	return true
}

// SameArtifactInventory is the narrow precondition for rehydrating objects.
// PostgreSQL has already been restored, but its storage backend may still be
// empty; all role-source tables and migrations must nevertheless match.
func SameArtifactInventory(expected, actual Manifest) bool {
	if expected.ContractVersion != ContractVersion || expected.DatabaseMajorVersion != actual.DatabaseMajorVersion ||
		expected.MigrationCount != actual.MigrationCount || expected.MigrationRoot != actual.MigrationRoot ||
		expected.Artifacts != actual.Artifacts || len(expected.Tables) != len(actual.Tables) {
		return false
	}
	for index := range expected.Tables {
		if expected.Tables[index] != actual.Tables[index] {
			return false
		}
	}
	return true
}
