// Package autopilotlock serializes title decisions that cross the ordinary
// Autopilot and Role Source write paths.
package autopilotlock

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const lockTitleSQL = `SELECT pg_advisory_xact_lock(
    hashtextextended(
        'multica:autopilot-title:' || $1::uuid::text || ':' || $2::text,
        0
    )
)`

const titleConflictSQL = `SELECT EXISTS (
    SELECT 1
    FROM autopilot target
    WHERE target.workspace_id = $1
      AND target.status <> 'archived'
      AND target.title = $2
      AND ($3::uuid IS NULL OR target.id <> $3)
      AND (
        EXISTS (
            SELECT 1 FROM role_source_object_mapping mapping
            WHERE mapping.workspace_id = target.workspace_id
              AND mapping.target_kind = 'autopilot'
              AND mapping.target_id = target.id
              AND mapping.archived_at IS NULL
        )
        OR EXISTS (
            SELECT 1 FROM role_source_object_mapping ownership
            WHERE ownership.workspace_id = $1
              AND ownership.target_kind = 'autopilot'
              AND ownership.target_id = $3
              AND ownership.archived_at IS NULL
        )
      )
)`

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LockTitles takes transaction-scoped locks in canonical order. Hash
// collisions only add serialization; the post-lock database checks remain the
// source of truth for ownership and conflicts.
func LockTitles(ctx context.Context, db executor, workspaceID pgtype.UUID, titles ...string) error {
	unique := make(map[string]struct{}, len(titles))
	for _, title := range titles {
		unique[title] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for title := range unique {
		ordered = append(ordered, title)
	}
	sort.Strings(ordered)
	for _, title := range ordered {
		if _, err := db.Exec(ctx, lockTitleSQL, workspaceID, title); err != nil {
			return err
		}
	}
	return nil
}

// HasTitleConflict protects the Role Source title namespace without making
// every ordinary Autopilot title unique. An ordinary write conflicts only with
// a managed target. A managed-target rename also conflicts with an ordinary
// target, preserving the stricter namespace after direct user edits.
func HasTitleConflict(ctx context.Context, db queryer, workspaceID pgtype.UUID, title string, excludedID pgtype.UUID) (bool, error) {
	var conflict bool
	err := db.QueryRow(ctx, titleConflictSQL, workspaceID, title, excludedID).Scan(&conflict)
	return conflict, err
}
