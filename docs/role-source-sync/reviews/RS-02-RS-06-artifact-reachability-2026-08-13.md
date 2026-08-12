# RS-02/RS-06 artifact reachability review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for the reachability prerequisite; NO-GO for object deletion**

## Customer and product outcome

Multica can now prove which immutable snapshots retain each content-addressed role-source artifact without repeatedly interpreting large manifest JSON. This does not yet delete customer data. It establishes the transactional evidence needed to implement retention without racing a scan that is publishing a new snapshot.

## Architecture review — 2/3

- `role_source_snapshot_artifact` stores tenant, source, snapshot, artifact digest and exact size; relationships stay application-enforced with no foreign keys.
- Snapshot reporting locks every readiness row `FOR SHARE`, inserts the immutable snapshot, writes all canonical reachability edges, re-reads them for exact digest/size agreement, then completes the scan and audit in one transaction.
- A future collector can use `FOR UPDATE SKIP LOCKED` on readiness rows. Shared-versus-exclusive row locking gives a clear winner between snapshot publication and GC claim.
- Existing snapshots are backfilled from all seven exact ArtifactRef locations in the normalized v1 manifest. Concurrent unique and lookup indexes are isolated migrations.
- Open objections: no retention policy or deletion/tombstone state machine exists yet; live PostgreSQL lock ordering and backfill performance are unmeasured.

## Product review — 2/3

- The ledger is invisible plumbing that prevents a future storage-cost feature from turning into silent rollback or historical-task data loss.
- Duplicate artifact use across roles is canonicalized by digest while each snapshot retains a visible edge.
- Open objections: administrators still cannot view retention eligibility, legal hold, storage footprint or projected reclaim; those controls must precede customer deletion.

## Test review — 2/3

- Static migration policy tests reject foreign keys, inline indexes and non-concurrent index files.
- Contract tests pin `FOR SHARE`, edge insertion ordering before scan completion, exact edge re-read and every backfill JSON path.
- sqlc generation and focused role-source, handler and migration suites pass.
- Open objections: the migration round-trip skips without PostgreSQL; add live concurrent snapshot/collector races, large historical backfill timing, duplicate digest and rollback/restore tests.

## CEO review — 2/3

- Explicit reachability is the minimum responsible investment before storage reclamation and supports enterprise retention/rollback claims across every adapter.
- It also avoids an unbounded JSON scan in each GC pass, keeping operating cost predictable as catalogs grow.
- Open objections: no bytes are reclaimed in this slice. Require measured storage age/volume and a reversible pilot before valuing the cost reduction.

## Security, privacy and data-loss decision

- The edge table stores only tenant-scoped IDs, content digests and sizes—no path, body, prompt, environment value or credential.
- Snapshot readiness locks and edge publication share the same transaction, so a GC claimant cannot observe a gap between them.
- Workspace deletion explicitly clears the new no-FK table.
- Object deletion remains forbidden until a leased tombstone/retry design, revive race handling, metrics, alerts and live fault injection are complete.

## Rollout decision

Merge the ledger and backfill behind the existing role-source rollout. Do not enable any delete worker from this evidence alone.
