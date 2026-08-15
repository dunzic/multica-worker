# RS-06 artifact permanent-purge receipt review — 2026-08-15

Scope: receipt-bearing permanent deletion after the four-pass tombstone tail

Evidence addendum: the same-day
[`RS-02-RS-06-versioned-s3-fail-closed-2026-08-15.md`](RS-02-RS-06-versioned-s3-fail-closed-2026-08-15.md)
closes the local versioned-provider late-PUT, legal-hold and explicit IAM-deny
probe. Candidate-provider, topology, receipt-correlation and recovery gates
remain open.

The same-day
[`RS-06-purge-ambiguity-evidence-2026-08-15.md`](RS-06-purge-ambiguity-evidence-2026-08-15.md)
adds the v2 lower-bound contract for partial responses, lost responses and
expired deleting leases. It closes the local transport/state-machine evidence,
not the candidate two-replica process-kill or failover gate.

Decision: **GO for merge behind the existing default-off artifact-GC gate;
NO-GO for customer deletion or storage-savings claims until the candidate
versioned-object-store, failover and provider-accounting gates pass.**

## Value hypothesis

The previous worker could eventually delete an object but left no durable proof
after removing its intent. This slice turns the fifth verified delete into one
immutable, self-verifying, content-free receipt. It gives an owner, operator and
restore verifier the same evidence without retaining the object path or body.
It proves logical exact-key absence, not a provider invoice or cost reduction.

## Architecture expert — 2/3

- Local and S3 storage return a closed provider-evidence result: backend, purge
  mode, versions/delete markers deleted, observed version bytes and final exact-
  key absence. S3 performs a preflight inventory, delete, post-delete inventory,
  exact version/marker deletion and a final empty-inventory check; all bounded
  or permission/Object-Lock failures remain retryable.
- Every tombstone pass persists evidence only when absence is verified and the
  backend/mode remains stable. The final query locks the leased intent, inserts
  one receipt and deletes the intent in the same statement/transaction. It
  independently requires each submitted aggregate to equal the evidence
  already stored on the intent plus the final pass.
- The receipt commits to intent/workspace identity, storage-key digest,
  artifact digest, original logical size, reason, aggregate provider evidence
  and a PostgreSQL-microsecond completion time. It has no raw key, path, body,
  credential or generic payload. Database triggers reject update and delete.
- API and DR consumers recompute the digest before trusting a row. Workspace
  totals come from the immutable full ledger; the owner UI returns only the
  newest 50 details and explicitly marks truncation. Backend and Core schema
  both reject integers outside JavaScript's exact range instead of silently
  rounding audit totals.
- Open objections: no two-replica/primary-failover execution, no candidate S3
  Object Lock/version-race proof (the local isolated MinIO protocol gate now
  passes), no provider request-ID commitment and no
  partition/retention policy for long-lived receipt volume.

## Product expert — 2/3

- The owner panel now separates preview from outcome: projected uniquely
  reclaimable bytes, logical bytes confirmed absent and provider-observed
  version bytes are distinct terms.
- The panel states that observed/logical bytes are storage evidence, not billed
  savings. There is no manual delete, retry or receipt-mutation control.
- Content-free details support deletion audit and incident triage without
  exposing a storage key. Existing members/admins do not gain new destructive
  authority; the route is owner-only.
- Open objections: wording/accessibility needs a controlled owner cohort;
  receipt export/search and evidence-retention terms are not approved; product
  must not turn the total into an ROI claim before provider reconciliation.

## Test expert — 2/3

- Local storage tests prove byte observation, final absence and idempotent empty
  retry. S3 tests prove three inventories, version/delete-marker counts and the
  final empty inventory contract.
- Unit tests prove deterministic receipt commitments, mutation detection,
  unsupported/changed providers and unverified absence fail closed. Core schema
  tests reject malformed totals; settings tests cover bounded rendering and the
  no-billing-savings warning.
- A disposable PostgreSQL 17 database migrated from 001 through 387. Three
  consecutive real state-machine runs executed ordinary five-pass completion,
  ambiguous response loss followed by retry and expired-lease reclaim. Direct
  SQL showed three complete and three incomplete v2 receipts, all exact keys
  absent, zero residual intents and the immutable trigger enabled. The v2
  downgrade guard failed closed; an empty-ledger 387 down/up round trip passed.
- The full role-source migration set passed isolated-schema up/down. Relevant
  Go tests and vet, Core 87 tests, Views 30 tests and changed-file ESLint pass.
  Full Core/Views typecheck remains red only on the three pre-existing Chat
  Quick Actions errors outside this slice.
- Open objections: the local real versioned-provider late-PUT, legal-hold and
  explicit version-delete-deny probes plus deterministic partial-delete,
  response-timeout and expired-lease ambiguity cases now pass; candidate-store
  >10,000 versions, real process death, two-replica race, primary failover,
  receipt correlation, restore and fleet/backlog SLO injection remain external
  gates.

## CEO / rollout owner — 2/3

- Durable deletion evidence materially reduces enterprise audit and support
  ambiguity and applies to every adapter, strengthening the source-neutral
  product rather than an AgentWaker-only fork.
- Default-off operation and explicit non-billing language contain reputation,
  compliance and customer-data risk.
- The feature does not yet prove lower cloud spend or 10,000-user operational
  readiness. Broad enablement would be an unsupported commercial claim until
  candidate topology, restore RPO/RTO, support labor, incident ownership and
  provider inventory/billing deltas are measured.

## Merge and production gates

Merge is allowed with `MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED=false`. The first
production cohort additionally requires:

1. two replicas and PostgreSQL primary failover while the final receipt query
   owns the lease;
2. candidate versioned S3-compatible late-PUT, partial-delete, Object Lock,
   permission and inventory-limit fault injection with retained provider
   request/inventory evidence (the local protocol subset already passes);
3. Gate F backup/restore proving every receipt commitment and absence of purged
   bodies, with measured RPO/RTO;
4. named SRE, security, product and data-retention owners plus alert/quarantine
   rehearsal; and
5. an independent object inventory and provider billing comparison after the
   provider accounting delay before any savings statement.
