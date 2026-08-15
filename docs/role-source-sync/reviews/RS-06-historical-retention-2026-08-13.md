# RS-06 historical-retention review

Date: 2026-08-13

Evidence update: 2026-08-15

Final decision: **GO for merge with both worker gates disabled; NO-GO for a
production retention cohort until Gate B3, permanent storage purge and restore
evidence are recorded**

## Architecture expert — 2/3

- Database-guarded append-only policy revisions use CAS plus hashed idempotency keys. Selection
  and destruction are separated by a bounded durable lease.
- Candidate selection and the final transaction both protect current state,
  active holds, task pins, materialization mappings, active transfers/applies,
  recent or approved-unapplied plans and the configured successful rollback
  reserve. The final check uses the latest policy, so queued work cannot bypass
  a later stricter revision.
- A task-pin insert locks its snapshot `FOR KEY SHARE`; prune locks that row
  before rechecking pins. Snapshot mutation also requires transaction-local
  retention or workspace-teardown authority at the database layer.
- Snapshot edges disappear in the same transaction as manifest content and
  audit completion. Capability definitions are removed only when no exact
  retained manifest references them; artifact deletion remains a separate
  permanent-purge state machine.
- A disposable PostgreSQL 17 database now proves the destructive lock protocol
  in both legal-hold/prune and task-pin/prune commit orders. Open objections are
  candidate-topology failover races, candidate-hardware SLO repeat, and
  independent retention policy for plan, apply and scan metadata.
- A separate opt-in gate runs JSON `EXPLAIN ANALYZE/BUFFERS/WAL` over the exact
  generated candidate query, then drains 10,000 eligible snapshots across 100
  sources through the public 100-row batch boundary. Three consecutive local
  runs completed in 2.099–2.181 seconds with 23.522–23.890 ms p95 and no
  duplicate candidate or fixture residue. This validates one single-primary
  inventory shape, not 10,000 users or a failover topology.

## Product expert — 2/3

- Owners see exact eligible count, bounded snapshot details and referenced byte
  estimate before creating a policy revision. Admins cannot see or mutate legal
  retention authority.
- The UI has no immediate-delete control and explains that preview is
  revalidated, artifact reclaim is later, and versions outside the reserve can
  become non-runnable.
- Defaults are preview-only, 90 days and 10 successful versions; hard limits are
  30–3,650 days and 2–100 versions.
- Open objections: referenced bytes are not unique reclaim, no realized-savings
  report exists, and legal/security approval ownership is not integrated.

## Test expert — 2/3

- Static and unit contracts cover policy bounds, CAS/idempotency, every blocker,
  lease claims, teardown, mutation guards, capability reachability, bounded
  metrics and nested environment gates.
- API/core/settings tests cover strict owner-only requests, safe response shapes,
  exact preview totals, persistent request identity during retry and no manual
  delete affordance.
- The PostgreSQL end-to-end test executes direct-delete rejection, legal-hold
  fencing, release, candidate claim, transactional prune and one audit event.
- The six-case deterministic matrix passed three consecutive runs: hold-first,
  policy-disable-first and pin-first defer prune; prune-first makes a later hold
  invalid, permits only the later append-only policy revision, and rejects a
  later pin with SQLSTATE `23000`; no orphan pin/snapshot state remains.
- The 10,000-snapshot scale matrix passed three consecutive runs with exact
  candidate count/distinctness, 2.465–2.487 ms planning,
  22.267–25.461 ms first execution, 20.536–21.640 ms p50,
  23.522–23.890 ms p95, 24.147–25.461 ms p99 and 2.099–2.181 seconds total.
- Open objections: replica competition during primary failover, the same load
  on candidate hardware, versioned-object purge and restore are not measured.

## CEO / rollout owner — 2/3

- This bounds long-term manifest and artifact storage while retaining a defined
  rollback floor and compliance holds across every adapter.
- Two independent server gates plus per-source owner policy contain rollout and
  let enterprise pilots remain preview-only.
- The implementation avoids a support-heavy manual delete path and preserves
  digest/receipt/audit evidence after runnable snapshot content expires.
- Open objections: approve retention RACI and customer terms; measure actual
  reclaimed cost, support incidents and restore time before pricing or broad
  claims.

## Required next evidence

Repeat both Gate B3 race and 10,000-snapshot scale cases on the candidate
two-replica PostgreSQL topology with failover and versioned S3-compatible
storage, approve retention RACI, then perform a recorded restore drill before
enabling the first customer policy. The local single-primary race/scale pass is
not authorization to delete production history.
