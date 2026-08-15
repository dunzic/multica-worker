# RS-03 atomic apply failure evidence review

Date: 2026-08-14

Gate: implementation and live PostgreSQL evidence

Decision: **GO to merge behind default-off role-source apply; NO-GO for broad
production until database-outage durability, topology failover and the
production-shaped recovery drill pass.**

## Customer outcome

An apply can no longer be described as atomic only from code inspection. A
fully migrated PostgreSQL 17 run proves that failures across transaction,
materialization and finalization either leave all business state unchanged or
commit one verifiable receipt. An ambiguous timeout or disconnected response
after commit resolves from durable evidence and does not invite a second
materialization.

The exercise also found a real defect in the previously unexecuted role-create
path: sqlc had generated `SELECT item` from a typed recordset, producing a
single composite column that PostgreSQL could not expose as `source_kind`.
The query now parses every bounded JSON field explicitly, including UUID and
timestamp casts, and the real Agent-plus-mapping path executes successfully
before the injected rollback.

## Architecture review — 3/3 for this slice

- Nine private ordered injection points cover transaction begin, apply start,
  both sides of materialization, secret consumption, snapshot CAS, receipt,
  success audit and outbox insertion. Every point before commit rolls back the
  same database transaction.
- A separate role-create fixture proves an inserted Agent and its provenance
  mapping roll back with apply, snapshot, success audit and outbox state.
- Failed-attempt evidence is written only after rollback through an independent
  cancellation-detached, three-second context. It contains a stable stage/code
  and hashed request key, not raw errors or source content.
- A driver wrapper performs the real commit and then returns
  `context.DeadlineExceeded`. Reconciliation accepts success only when the
  stored actor, approval, plan, secret-transfer set and receipt all match.
- A cancelled request after commit still reconciles, and a second test bypasses
  in-process reconciliation to model process loss, constructs a new control
  plane and resolves the exact request idempotently.
- Fault injection has no exported setter, environment binding, API or UI. A
  static contract test fixes the hook order and proves production construction
  leaves it disabled.

Open objections: if PostgreSQL itself is unavailable, neither reconciliation
nor the independent failed-attempt insert is durable. A single local primary
does not prove failover fencing, connection-pool behavior or replica routing.

## Product review — 2/3

The behavior now matches the operator promise: pre-commit errors are proven
rollback cases; a commit-stage error remains ambiguous unless exact durable
success evidence resolves it. The existing recovery UI can refresh evidence
without exposing a blind historical retry, and response cancellation no longer
creates pressure to invent a new idempotency key.

Open objections: this slice adds no incident acknowledgement, support export,
RACI or measured recovery time. Operators still need a recorded drill for a
true database outage and a primary failover.

## Test review — 3/3 for the single-primary atomicity claim

Passing evidence:

- all 375 migrations were applied to disposable PostgreSQL 17;
- all 13 live cases pass: nine ordered pre-commit points, one real
  Agent-plus-mapping rollback, one driver-level ambiguous commit, one cancelled
  post-commit response and one new-control-plane restart recovery;
- every rollback case retains the original source snapshot, has zero apply,
  success-audit and outbox rows, and records exactly one content-free failure;
- every committed case has exactly one succeeded apply, one success audit, one
  outbox event, the target snapshot and zero failed-attempt rows;
- sqlc regeneration, focused unit tests and static default-off/hook-order
  assertions pass.

Open objections: real connection loss during PostgreSQL failover, hard process
kill, two API replicas, high-contention domain writes and 1,000-role/10,000-skill
latency/WAL remain staging gates. The deterministic post-commit process-loss
hook models the boundary but is not a substitute for killing a production
container.

## CEO review — 2/3

This closes a high-severity trust gap and paid for itself immediately by
finding a role-create SQL defect before rollout. The implementation is
adapter-neutral, so AgentWaker and later sources share the same atomicity and
recovery evidence. Exactly-once business effects under response ambiguity
reduce both data-repair cost and the risk of duplicate destructive work.

The commercial claim is still conditional. Broad rollout needs measured apply
failure rate, reconciliation success rate, operator resolution time, database
outage behavior and production-shaped capacity. Until then this is strong
pilot safety evidence, not proof of fleet readiness.

## Rollout and rollback

- Keep `role_source_apply` default-off and limit use to the controlled cohort.
- Run Gate B6 on every candidate database/driver upgrade.
- Run Gate D with real process kill and primary failover before widening the
  cohort; retain database-outage failure evidence and the recovery timeline.
- Rollback is code-only: the hook is nil in production and this slice adds no
  schema or customer-facing contract. Preserve the explicit JSON mapping query;
  reverting it restores the production role-create failure.
