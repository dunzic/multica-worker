# RS-06 legal-hold foundation review

Date: 2026-08-13

Evidence update: 2026-08-15

Gate: architecture, product, test and CEO merge review

Final decision: **GO for merge behind the existing role-source rollout with
destructive workers disabled; the local PostgreSQL hold/prune race gate passes,
but broad production remains NO-GO pending primary failover, retention RACI,
versioned-object purge and disaster-recovery evidence**

## Architecture expert — 2/3

- A hold is source-neutral retention authority over an entire source or one
  immutable snapshot, not an AgentWaker lifecycle flag.
- Create/release use the shared workspace then source lock order and exact
  idempotency. Release is a separate row; both events enter the existing
  hash-chained audit ledger.
- Workspace deletion takes its exclusive workspace lock and checks active holds
  before any teardown mutation. A database trigger also rejects direct deletion
  of active holds and any update to hold/release authority.
- Tables and teardown remain explicit and no-FK. The existing
  workspace/source/created-at listing index also serves the bounded active-hold
  query; no redundant per-workspace index was added.
- The historical retention candidate/prune transaction consumes this authority
  under the shared workspace/source lock order and rechecks it immediately
  before deletion. A deterministic PostgreSQL 17 matrix now proves hold-first
  and prune-first linearization with real `transactionid` and tuple waits.
- Open objections: primary failover and candidate-topology cross-replica lock
  behavior are not measured; signed external authority is represented by a
  commitment, not verified by Multica.

## Product expert — 2/3

- Only owners can see and operate the panel. Admin lifecycle authority does not
  imply legal-release authority.
- Closed create/release reasons avoid an ungoverned case-note database. The UI
  accepts only an optional SHA-256 commitment and warns against predictable
  identifiers.
- The surface distinguishes active/released state and explicitly says a hold
  blocks deletion/pruning but does not pause scan/apply.
- The owner surface now includes policy-aware exact-count/referenced-byte
  preview and append-only policy CAS. Open objections remain for uniquely
  reclaimable bytes, realized savings, legal-authority integration, bulk holds,
  expiry workflow and customer export.

## Test expert — 2/3

- Unit/API tests cover strict bodies, source/snapshot validation, safe
  projection, exact idempotency, conflict mapping, owner-only routing and
  owner/admin UI separation.
- Migration contracts prove digest-only fields, isolated concurrent indexes,
  mutation triggers and dependency-ordered teardown.
- A disposable fully migrated PostgreSQL 17 database executed
  create/list/release/audit, active deletion fencing, direct mutation rejection
  and released teardown cleanup. The four-case hold/task-pin versus prune gate
  passed three consecutive runs with fail-closed loser states.
- Core typecheck/client tests, focused settings tests and Go role-source,
  handler and server suites pass locally.
- The isolated-schema migration round trip through migration 380 and task-pin
  triggers pass. Open objections are candidate-topology deadlock/failover,
  EXPLAIN/SLO, large-inventory and restore evidence.

## CEO / rollout owner — 2/3

- Enterprise retention cannot responsibly ship without a legal-hold primitive;
  this lowers data-loss and contractual risk before any historical pruning.
- The design is connector-neutral and low-cardinality, so new adapters inherit
  the same authority instead of creating product-specific compliance forks.
- The initial UX intentionally avoids case-management scope and stores no case
  narrative, reducing privacy, support and migration cost.
- Open objections: commercial readiness still needs approved retention tiers,
  measurable storage recovery, backup/restore proof and a recorded legal/security
  operating procedure.

## Security, privacy and data-loss decision

- Raw idempotency keys are hashed before persistence. Case number, narrative,
  customer identity, paths, manifests, bodies and credentials are not accepted.
- A plain SHA-256 of a low-entropy case number is reversible by guessing; the UI
  and runbook require approved high-entropy or HMAC-derived commitments.
- Active holds fail closed for workspace deletion at both the handler preflight
  and database row guard. Release does not erase historical evidence.
- A source-scoped hold is defined to protect future snapshots once historical
  pruning exists. Shipping a pruner that checks only current holds outside its
  locked candidate transaction is prohibited.

## Next evidence required

1. Repeat Gate B2 on the candidate two-replica topology during PostgreSQL
   primary failover and record lock waits, deadlocks and winner/loser states.
2. Approve retention tiers, minimum rollback window and legal authority/RACI.
3. Run versioned-object permanent purge with Object Lock/hold behavior and
   reconcile projected versus realized reclaim.
4. Prove backup restore, point-in-time recovery and storage reconciliation
   preserve active holds before enabling any historical deletion.
