# RS-06 legal-hold foundation review

Date: 2026-08-13

Gate: architecture, product, test and CEO merge review

Final decision: **GO for merge behind the existing role-source rollout; NO-GO
for historical snapshot pruning or broad production until live PostgreSQL
races, retention policy and disaster-recovery evidence pass**

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
- Open objections: no historical snapshot candidate/prune transaction consumes
  this authority yet; database failover and cross-replica lock behavior are not
  measured; signed external authority is represented by a commitment, not
  verified by Multica.

## Product expert — 2/3

- Only owners can see and operate the panel. Admin lifecycle authority does not
  imply legal-release authority.
- Closed create/release reasons avoid an ungoverned case-note database. The UI
  accepts only an optional SHA-256 commitment and warns against predictable
  identifiers.
- The surface distinguishes active/released state and explicitly says a hold
  blocks deletion/pruning but does not pause scan/apply.
- Open objections: no policy-aware retention preview, projected bytes, legal
  authority integration, bulk holds, expiry workflow or customer export.

## Test expert — 2/3

- Unit/API tests cover strict bodies, source/snapshot validation, safe
  projection, exact idempotency, conflict mapping, owner-only routing and
  owner/admin UI separation.
- Migration contracts prove digest-only fields, isolated concurrent indexes,
  mutation triggers and dependency-ordered teardown.
- Opt-in PostgreSQL tests cover create/list/release/audit, active deletion
  fencing, direct delete rejection and released teardown cleanup.
- Core typecheck/client tests, focused settings tests and Go role-source,
  handler and server suites pass locally.
- Open objections: PostgreSQL is unavailable locally, so real trigger,
  migration, deadlock, failover and multi-replica results are not recorded.

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

1. Run Gate B2 against PostgreSQL 17 with two server replicas and record lock
   waits, deadlocks and all winner/loser states.
2. Approve retention tiers, minimum rollback window and legal authority/RACI.
3. Implement a leased retention candidate/prune state machine that checks
   source/snapshot holds and runtime-task reachability in the same transaction.
4. Prove backup restore, point-in-time recovery and storage reconciliation
   preserve active holds before enabling any historical deletion.
