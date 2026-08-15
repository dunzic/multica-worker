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
  EXPLAIN/large-inventory SLO, candidate-topology failover races, and independent
  retention policy for plan, apply and scan metadata.

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
- Open objections: replica competition during primary failover, large
  inventories, versioned-object purge and restore are not measured.

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

Repeat Gate B3 on the candidate two-replica PostgreSQL topology with failover
and versioned S3-compatible storage, approve retention RACI, then perform a
recorded restore drill before enabling the first customer policy. The local
single-primary race pass is not authorization to delete production history.
