# RS-04 bounded mapping materialization review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL scale and fault evidence
passes.**

## Architecture review — 2/3

- Every planned source-object mapping remains staged in memory and sorted by
  canonical source kind, parent and object identity before persistence.
- The previous final flush sent all staged mappings in one unbounded JSON
  argument. It now emits at most 500 mappings and 8 MiB per typed recordset,
  preventing 1,000-role/10,000-skill applies from concentrating their entire
  provenance ledger in one statement.
- Each returned batch must contain exactly the requested source-object set;
  duplicate, missing and unexpected rows abort the same outer apply
  transaction before receipt, audit or current-snapshot advancement.
- All batches still execute inside the existing workspace/source lock and one
  atomic transaction. A later-batch failure rolls back earlier batches.
- The adjacent 10,000 employee-to-skill association set is also canonicalized
  and split at 1,000 rows or 2 MiB, with exact returned endpoint-pair checks;
  existing user-disabled associations remain disabled.

Open objection: query plans, row-trigger cost, WAL volume and lock duration have
not been measured on PostgreSQL 17 with production-shaped existing mappings.

## Product review — 2/3

- Large imports and updates should have a lower peak statement size and a more
  predictable failure boundary without changing the reviewed plan, ownership
  contract, receipt or recovery workflow.
- The change is adapter-neutral and benefits AgentWaker plus every future role
  source.

Open objection: this reliability work is intentionally invisible to customers;
its commercial value depends on measured apply completion and support outcomes.

## Test review — 2/3

- Unit tests cover count splitting, single-entry byte rejection and exact
  return-set success, duplicate, missing and unexpected rows.
- A production-shaped in-process fixture stages 11,000 role/skill mappings and
  10,000 employee-to-skill associations and proves deterministic bounded count
  and byte envelopes.
- Focused role-source tests and race tests pass.

Open objection: no live PostgreSQL test yet injects failure after every mapping
batch, proves full rollback and safe retry, or records statement latency and
lock waits under concurrent user activity.

## CEO review — 2/3

This removes another predictable large-tenant failure mode while preserving
the audit and atomicity promises. It reduces staging risk but does not by itself
justify general availability.

Evidence required for 3/3: PostgreSQL 17 scale run, batch-boundary failure
injection, concurrent-edit proof, end-to-end transaction/WAL/lock metrics and a
two-operator recovery rehearsal.
