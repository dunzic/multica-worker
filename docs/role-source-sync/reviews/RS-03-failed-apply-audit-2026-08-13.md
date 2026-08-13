# RS-03 failed apply audit review

Date: 2026-08-13

Gate: design and merge evidence

Live-evidence update: the previously open PostgreSQL failure-injection and
commit-ambiguity items are superseded by
[`RS-03-atomic-apply-failure-evidence-2026-08-14.md`](RS-03-atomic-apply-failure-evidence-2026-08-14.md).

Final decision: **GO for merge behind disabled flags; NO-GO for production apply operations**

## Customer and product outcome

An apply request that returns an error now leaves content-free, append-only evidence independently of its main workspace mutation transaction. Workspace members can inspect the source, plan, approval, actor, apply/rollback mode, failure stage, stable failure code and timestamp from the existing read-only Role Sources settings surface. No retry, recovery or apply control is exposed.

## Architecture review — 2/3

- The failure row is inserted only after the inner apply call returns, so the main transaction defer has already rolled back before the independent write begins.
- The independent write uses a cancellation-detached context with a three-second bound. Client disconnects therefore do not erase the audit attempt indefinitely or create unbounded cleanup work.
- The ledger stores a SHA-256 request-key correlation value, never the raw idempotency key, raw error, source manifest, secret, MCP definition, artifact body or task content.
- Failure stage and code are database-constrained stable enums/bounded values. The same table covers apply and rollback without an AgentWaker-specific branch.
- Bounded Prometheus counters separate returned apply errors from failure-evidence write outcomes, and the Helm rule pages on any persistence or ID-generation failure without tenant/request labels.
- A commit-response error triggers an independent primary-database lookup by the exact idempotency key. A digest-verified, actor/approval/secret-transfer-exact successful receipt is returned as success; missing, conflicting or unavailable evidence remains an error and is separately measured/alerted.
- Open objections: a database outage can prevent both reconciliation and the independent row itself from being persisted; no durable outbox/fallback or retention/partition policy exists yet.

## Product review — 2/3

- Operators can distinguish preflight, transaction, materialization, finalize and commit failures without seeing sensitive technical error text.
- Empty, loading and error states are explicit, and the surface remains visibly read-only.
- The UI explicitly warns that a remaining commit-stage entry means automatic reconciliation did not confirm success and must not be treated as proof of rollback.
- Open objections: stable codes still need product-owned labels and remediation guidance; commit-stage ambiguity must be explained; there is no support workflow, acknowledgement, retry or recovery action.

## Test review — 2/3

- Unit tests cover stable error classification, wrapped errors, UTC timestamps and exact content-free persistence parameters.
- Handler tests prove the internal request-key digest and raw error fields are absent from member-visible JSON.
- Existing role-source, handler and server package suites pass with regenerated sqlc code and migration-policy coverage.
- Open objections: no live PostgreSQL failure injection yet proves rollback-then-independent-insert ordering, request cancellation behavior, commit ambiguity, database outage behavior, duplicate attempts, migration round-trip or high-volume listing latency.

## CEO review — 2/3

- Durable failure evidence strengthens the enterprise trust and support story without enabling high-consequence writes.
- A shared source-neutral ledger reduces diagnosis cost across future adapters.
- Open objections: this is risk-control infrastructure, not a complete customer workflow. Production value requires recovery time, failure-rate and operator-resolution metrics plus a proven runbook.

## Security, privacy and data-loss decision

- The public DTO intentionally omits the internal correlation digest.
- Stable codes replace raw database, filesystem and provider errors in persistence and UI.
- Invalid requests without safely parsed tenant/source/approval identity are not recorded in this tenant ledger; edge/API security telemetry must cover them separately.
- Failure-ledger persistence is best effort when PostgreSQL itself is unavailable. This limitation remains a production blocker until a durable fallback/outbox and alerting path is accepted.

## Rollout decision

Merge is allowed behind the existing disabled `role_source_sync` and `role_source_scan` read flags. The `role_source_apply` mutation flag remains disabled. The 2026-08-14 live PostgreSQL migration and failure-injection run now permits a controlled default-off apply cohort; broad customer rollout remains NO-GO pending outage/failover and operator-recovery evidence.
