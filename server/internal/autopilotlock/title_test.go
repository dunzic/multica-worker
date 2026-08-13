package autopilotlock

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type recordingExecutor struct {
	titles []string
	err    error
}

func (e *recordingExecutor) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "pg_advisory_xact_lock") || !strings.Contains(query, "multica:autopilot-title:") {
		return pgconn.CommandTag{}, errors.New("unexpected title lock query")
	}
	e.titles = append(e.titles, args[1].(string))
	return pgconn.CommandTag{}, e.err
}

func TestLockTitlesUsesCanonicalDeduplicatedOrder(t *testing.T) {
	executor := &recordingExecutor{}
	err := LockTitles(context.Background(), executor, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, "Zulu", "Alpha", "Zulu")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Alpha", "Zulu"}; !reflect.DeepEqual(executor.titles, want) {
		t.Fatalf("locked titles = %v, want %v", executor.titles, want)
	}
}

func TestRoleSourceClaimQueryKeepsOrdinaryDuplicateSemantics(t *testing.T) {
	for _, required := range []string{
		"role_source_object_mapping", "mapping.target_kind = 'autopilot'", "mapping.target_id = target.id",
		"ownership.target_kind = 'autopilot'", "ownership.target_id = $3",
		"mapping.archived_at IS NULL", "ownership.archived_at IS NULL",
		"target.status <> 'archived'", "target.title = $2", "target.id <> $3",
	} {
		if !strings.Contains(titleConflictSQL, required) {
			t.Errorf("claim query missing %q", required)
		}
	}
	if strings.Contains(titleConflictSQL, "COUNT(*) FROM autopilot") {
		t.Fatal("claim query must not turn all ordinary Autopilot titles into a unique namespace")
	}
}
