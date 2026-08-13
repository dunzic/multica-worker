# RS-04 batched Skill-file mutation review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL race, lock, rollback and scale
evidence passes.**

## Architecture review — 2/3

- The transaction-local file cache collapses every `(skill_id, path)` from its
  locked initial state to one final insert, update or delete, including path
  transfer between a Skill object and capability object in the same apply.
- Mutations are sorted by target/path and split at 250 rows or 64 MiB. Every
  database result must match the requested target, path and operation exactly
  before mappings and receipt can commit.
- Inserts have no conflict handler. A concurrent user create therefore raises a
  uniqueness conflict and rolls back instead of becoming an implicit update.
  Updates/deletes operate only on the previously locked target/path and repeat
  the workspace boundary in SQL.
- The lock set contains only targets touched by this apply, not every historical
  source mapping. Its file query contains only deduplicated desired-or-owned
  paths (bounded at 40,000 targets/16 MiB), so unrelated user files are neither
  loaded nor locked. New Skill IDs enter an empty cache without querying rows
  that cannot exist yet.
- Open objection: PostgreSQL behavior of the mixed data-modifying CTE has not
  been exercised against the candidate schema and production-shaped contention.

## Product review — 2/3

- Large role packages no longer issue file writes per Skill or capability
  object, and imports still cannot overwrite a user-created path.
- A path handed from one source object to another becomes one final update,
  avoiding transient delete/reinsert behavior and preserving row identity.
- Open objection: users need a path-specific, privacy-safe conflict explanation
  and a guided retain/rename/retry workflow before production rollout.

## Test review — 2/3

- Tests cover desired-or-owned path selection, final-state collapse,
  create-then-cancel, path transfer, count and byte batch bounds, the
  10,000-file/40-batch shape, and exact duplicate,
  missing, unexpected, wrong-target and wrong-operation database results.
- SQL contract tests require canonical tenant locks, three explicit operation
  branches and the absence of a conflict-update clause in the role-source path.
- Focused role-source, handler and server tests pass with regenerated sqlc code.
- Open objection: no live PostgreSQL test yet covers simultaneous user writes,
  statement interruption, later-batch failure, deadlock, process loss or
  ambiguous COMMIT.

## CEO review — 2/3

- The core Agent/Skill/file/Automation materialization writes are now bounded
  and set-based, removing the clearest application-side 10,000-user scale
  multiplier while strengthening customer-file protection.
- The implementation is source-neutral and benefits AgentWaker plus future
  adapters without moving protocol rules into the core domain.
- Open objection: production value still needs measured apply time, database
  cost, timeout reduction, conflict rate and operator recovery outcomes.

## Evidence required for 3/3

1. PostgreSQL 17 integration race suite for user insert/update/delete during
   preflight and every mutation batch, proving no user content is overwritten.
2. Failure injection after every file batch and during mapping, receipt and
   COMMIT work, proving total rollback and idempotent recovery.
3. Production-shaped 1,000-role/10,000-Skill/file run measuring statements,
   locks, deadlocks, WAL, transaction duration, pool pressure and rollback cost.
4. Candidate-image operator rehearsal with path-safe conflict detail, explicit
   retain/rename policy and a successful retry under the same approval model.
