# RS-02 artifact readback, quarantine and repair review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge default-off; NO-GO for a production cohort until
live PostgreSQL and versioned-object-store fault evidence passes.**

## Customer and product outcome

Multica no longer assumes that a historical readiness row proves its object
body still exists. A bounded worker reads content-addressed bodies back,
quarantines confirmed loss or corruption, and lets the owning source repair the
same digest through the ordinary authenticated scan/upload path. Snapshot and
apply fail closed while a row is checking or quarantined.

## Architecture review — 2/3

- Immutable upload evidence remains immutable; mutable verification lifecycle
  is isolated in `role_source_artifact_integrity` with explicit leases,
  outcomes, counts and next-check time.
- Upload, repair, readiness, snapshot/apply locks, GC cleanup, workspace cleanup
  and DR inventory share one tenant-and-digest identity. No foreign key or
  cascade was introduced.
- Confirmed missing, size mismatch and digest mismatch quarantine. Typed object
  not-found classification is adapter-owned; other storage errors retry and
  cannot cause a fleet-wide false quarantine.
- `checking` is excluded from snapshot/apply, closing the readback-versus-use
  window. An exact concurrent re-upload resets the state; a stale worker token
  cannot overwrite it.
- Open objection: real PostgreSQL row-lock schedules, S3 version mutation,
  timeout and failover behavior are not yet recorded.

## Product review — 2/3

- Customers gain prevention of new snapshot/apply use after confirmed body loss
  without exposing object keys or offering a dangerous “clear quarantine”
  control.
- Repair reuses read-only source scan and exact digest upload, preserving source
  authority and auditability. Existing materialized workers are not silently
  rewritten.
- Alerts distinguish confirmed quarantine from transient storage availability.
- Open objection: the settings surface does not yet show a source-scoped repair
  workflow, safe progress, or support ownership; cohort rehearsal is required.

## Test review — 2/3

- Unit tests cover healthy, typed missing, transient open failure, size mismatch
  and digest mismatch; safety constants bound batch, concurrency, deadline and
  healthy interval.
- Migration/SQL contracts cover concurrent indexes, historical backfill,
  checking/quarantine exclusion, shared readiness locks and GC cleanup.
- Storage classifiers, worker wiring, metrics cardinality, alerts, Helm default,
  workspace deletion and DR invariants are included in focused and race suites.
- Open objection: tests do not yet mutate a live versioned bucket while
  snapshot/apply and re-upload compete, nor prove recovery after process and
  database failover at the 10,000-user target.

## CEO review — 2/3

- This closes a material silent-data-loss gap and improves the credibility of
  audit, rollback and long-lived source catalogs with small bounded background
  cost.
- The feature does not claim self-healing from arbitrary bytes: only the
  authoritative source can supply an exact digest, limiting operational and
  security liability.
- Open objection: production value and cost need measured corruption detection
  latency, false-positive rate, object-read spend, repair time and support load.

## Security, privacy and operational decision

- Metrics and ordinary logs contain only bounded outcomes, stages and fleet counts—no
  workspace, source, digest, storage key, path, body or raw provider error.
- Default batch 100, concurrency 8, lease two minutes, read deadline 30 seconds
  and healthy interval seven days bound storage and database pressure.
- The worker is independently gated by
  `MULTICA_ROLE_SOURCE_ARTIFACT_INTEGRITY_ENABLED=false` by default. It never
  deletes or overwrites objects.
- Operators must not edit integrity rows or copy arbitrary objects. The safe
  path is source rescan, exact upload, healthy readback and then a new scan.

## Evidence required for 3/3

1. PostgreSQL 17 migration/backfill plus checking/snapshot/apply/re-upload/GC
   lock races under normal operation, restart and primary failover.
2. Versioned S3-compatible tests for delete marker, missing version, truncated
   body, same-size corrupt body, 403/429/5xx/timeouts and restoration.
3. Candidate-image 10,000-user run measuring read operations, bytes, pool/CPU,
   lock wait, alert series, detection latency and repair SLO.
4. Operator exercise using the documented no-manual-clear flow, followed by DR
   verifier proof and named SRE/product/security ownership.
