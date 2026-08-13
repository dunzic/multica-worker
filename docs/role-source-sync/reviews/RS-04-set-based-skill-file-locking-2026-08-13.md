# RS-04 set-based Skill-file locking review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL race, lock and scale evidence
passes.**

## Architecture review — 2/3

- All existing Skill targets affected by the source are locked once in canonical
  UUID order, and their files are loaded once in target/path order before any
  Skill main-row mutation.
- A transaction-local cache carries the exact file state across Skill supporting
  files, capability bundles and capability archive work on the same target.
- Existing-path updates are permitted only when the current source object's
  ownership mask contains that path. A path inserted concurrently after the
  initial read cannot be overwritten: the database uniqueness conflict returns
  no row, the exact-set check fails and the whole apply rolls back.
- Open objection: delete and upsert statements are still emitted per source
  object. The read/lock amplification is removed, but the write path is not yet
  globally batched.

## Product review — 2/3

- User-authored Skill files retain priority over imported content, including the
  previously unsafe read-then-upsert race window.
- Large packages no longer pay two lock/read round trips for every existing
  Skill or capability binding.
- Open objection: operators still lack apply progress and path-conflict recovery
  controls; the API only reports a stable content-free conflict class.

## Test review — 2/3

- Tests cover mandatory prepared state, missing target rejection, cached
  user-owned collision, exact/duplicate/missing/unexpected lock sets, canonical
  tenant-scoped SQL locks and ownership-gated conflict updates.
- Focused role-source, handler and server tests, race checks and Go vet pass;
  sqlc output is regenerated in the same change.
- Open objection: local validation cannot exercise real PostgreSQL row locks,
  foreign-key lock interaction, concurrent insert/delete/update, deadlock
  detection, rollback cost or 10,000-Skill latency.

## CEO review — 2/3

- The change removes a material database round-trip multiplier and closes a
  credible customer-data overwrite risk without weakening the source-neutral
  ownership model.
- It is useful to every adapter, not only AgentWaker.
- Open objection: measured database-time and incident-reduction value is absent,
  and per-object writes still cap the scale benefit.

## Evidence required for 3/3

1. PostgreSQL 17 race suite with user insert/update/delete during preflight and
   mutation, proving no user-owned content is overwritten and retries are safe.
2. Mixed Skill/capability path-transfer tests proving canonical locks are
   deadlock-free under concurrent user and source operations.
3. Production-shaped 1,000-role/10,000-Skill run measuring statements, locks,
   WAL, transaction duration, pool pressure and rollback cost.
4. Bounded global delete/update/insert batches with exact outcome verification,
   followed by candidate-image fault injection and operator rehearsal.
