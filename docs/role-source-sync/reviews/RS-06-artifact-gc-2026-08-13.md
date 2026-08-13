# RS-06 artifact garbage-collection review

Status note: the missing legal-hold and historical-snapshot policy/UI described
below are implemented by the later RS-06 reviews. The production deletion
NO-GO remains until their live PostgreSQL, storage and restore gates pass.

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for merge behind the existing role-source rollout; NO-GO for broad production deletion until live fault injection passes**

## Customer and product outcome

Role-source uploads that never become snapshots and object bodies left by workspace deletion no longer grow forever. The system retains every snapshot-reachable digest, delays orphan eligibility for 24 hours, and turns deletion into a durable retryable obligation instead of a best-effort side effect.

## Architecture review — 2/3

- One SQL statement exclusively locks an old unreferenced readiness row, creates a global deletion intent and removes readiness. Snapshot publication takes the conflicting shared lock and writes explicit edges.
- Workspace teardown enqueues all artifact keys before deleting tenant rows; the queue deliberately has no workspace identity so it survives teardown without retaining tenant metadata.
- Claims use `SKIP LOCKED`, two-minute leases and token-guarded transitions. Storage calls have a 30-second deadline and failed/ambiguous deletes return to bounded backoff.
- Successful deletes retain a 15-minute, 1-hour, 6-hour and 24-hour idempotent re-delete tail. A new exact upload may cancel pending/tombstoned intent but fails retryably while deletion owns the key.
- Open objections: no legal-hold/historical-snapshot retention UI, object readback verification or live multi-replica database proof.
- S3 deletion now uses an exact-key permanent purge: it removes the current object plus every retained version and delete marker, fails closed above 10,000 versions or when list/version-delete permission or Object Lock prevents erasure, and leaves the durable intent retryable. Local storage has no hidden version layer and implements the same purge contract.

## Product review — 2/3

- The first policy is intentionally conservative: only bodies with no snapshot edge after 24 hours, plus deleted-workspace bodies, are eligible.
- Current and historical snapshots remain untouched; rollback evidence is not shortened by this worker.
- Operators receive backlog/tombstone/delete metrics and a sustained-failure alert rather than silent storage accumulation.
- Open objections: expose projected/reclaimed bytes, manual quarantine/retry and retention policy before customer self-service.

## Test review — 2/3

- Static contracts pin edge absence, exclusive skip-locked claim, durable intent-before-readiness-delete, workspace enqueue ordering, lease-token guards and revive-state restrictions.
- Unit invariants require settle delay, delete-timeout/lease headroom and a strictly widening four-pass tombstone schedule.
- Focused service, role-source, handler, server, migration, metrics and static-analysis suites pass; sqlc generation succeeds.
- Open objections: PostgreSQL is unavailable locally. Run real concurrent snapshot/GC, workspace delete, expired lease, timeout-after-server-delete, late PUT, revive and two-replica tests against S3-compatible storage.

## CEO review — 2/3

- Bounded abandoned-upload cost is necessary for a 10,000-user service and makes workspace deletion operationally honest.
- The generic digest/intent mechanism applies to every adapter and avoids source-specific cleanup support.
- Open objections: measure backlog age, reclaimed bytes, storage savings and incident rate before treating GC as commercially proven.

## Security, privacy and data-loss decision

- Delete intents contain storage key, digest, size and lifecycle state only—no workspace ID, path, prompt, body or credential.
- Snapshot-reachable objects are excluded by indexed edges and row-lock fencing. Actively deleting keys cannot be revived underneath an in-flight delete.
- Deletion failure retains intent and retries; it never marks success optimistically.
- Production deletion remains blocked on live fault injection, version-inventory verification, Object Lock/legal-hold policy, backup/restore and an operator quarantine runbook. The independent `MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED` gate is default-off.

## Rollout decision

Merge the worker but keep its independent environment gate off. Before enabling deletion in a controlled cohort, run live failure tests and verify alerts/backlog dashboards. Historical snapshot retention remains unchanged.
