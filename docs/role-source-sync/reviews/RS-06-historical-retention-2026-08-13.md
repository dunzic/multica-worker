# RS-06 historical-retention review

Date: 2026-08-13

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
- Open objections: no live PostgreSQL EXPLAIN/lock/failover evidence, and plan,
  apply and scan metadata have independent retention policies still to define.

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
- An opt-in PostgreSQL test encodes direct-delete rejection, legal-hold fencing,
  release, candidate claim, transactional prune and one audit event.
- Open objections: PostgreSQL is unavailable locally; task-pin/delete races,
  replica competition, failover, large plans and restore are not measured.

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

Run Gate B3 on PostgreSQL 17 and versioned S3-compatible storage, record all
four review scores at 3/3, then perform a restore drill before enabling the first
customer policy. A unit-test pass is not authorization to delete production
history.
