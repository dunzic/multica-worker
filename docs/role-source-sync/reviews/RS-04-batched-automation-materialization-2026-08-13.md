# RS-04 batched automation materialization review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL scale, title-race and fault
evidence passes.**

## Architecture review — 2/3

- New Autopilot identities are allocated by the control plane; mapped rules
  retain their target identities. Trigger identities remain deterministic from
  source, role and automation identity.
- Each statement handles at most 250 automations and 64 MiB, including both the
  Autopilot main row and its schedule trigger.
- New rules are always paused/run-only and their triggers disabled. Updates
  change only title, description and schedule metadata; status, execution mode,
  assignee and trigger enabled state remain user-controlled.
- The statement returns only trigger-backed Autopilot IDs. Missing/retargeted/
  archived rules, cross-tenant targets, trigger-ID ownership conflicts or wrong
  trigger kinds produce an incomplete set and roll back the whole apply before
  mappings, receipt or audit commit.
- Ordinary Autopilots intentionally permit duplicate titles, so a global unique
  index would break the existing product contract. Role Source automation
  titles and ordinary create/rename paths instead share transaction-scoped,
  workspace-title advisory locks. The materializer locks the full desired set
  in canonical order and repeats its bounded conflict check before writing.
- Open objection: the lock protocol has unit and source-order contract coverage,
  but still needs a live two-transaction PostgreSQL race test.

## Product review — 2/3

- Large automation packages avoid one main-row plus one trigger round trip per
  rule while retaining the essential safety contract: imports cannot silently
  activate recurring work or retarget an existing rule.
- Trigger ownership conflicts fail closed instead of attaching a schedule to
  another Autopilot.
- The existing plan card exposes affected workers and tasks. Open objection:
  end-to-end customer apply progress and recovery guidance remain incomplete.

## Test review — 2/3

- Tests cover count bounds, a 10,000-automation/40-batch upper-bound shape,
  exact success and duplicate/missing/unexpected returned IDs.
- SQL contract tests cover tenant, assignee, non-archived and trigger ownership/
  kind constraints, paused/run-only create defaults, disabled triggers and the
  protected update-field boundary.
- Focused role-source, handler and server packages pass; generated SQL is part
  of the same change.
- Open objection: no live PostgreSQL test covers mixed create/update, trigger
  conflict, title race, WAL/locks, later-batch rollback, process interruption or
  ambiguous commit.

## CEO review — 2/3

- This removes the final obvious per-object main-row bottleneck from the core
  materialization pipeline while preserving a strong “no surprise scheduled
  work” promise.
- The source-neutral implementation benefits AgentWaker and future adapters.
- Open objection: value requires measured apply duration, database cost,
  timeout/rollback reduction and operator recovery outcomes on representative
  tenants.

## Evidence required for 3/3

1. PostgreSQL 17 production-shaped run with mixed create/update automations,
   measuring statements, WAL, row locks, transaction duration and pool usage.
2. Concurrent ordinary create/rename and source apply on PostgreSQL proving the
   shared title lock plus post-lock recheck; do not treat unit/source-order
   coverage as live race evidence.
3. Failure injection after every batch and during trigger, mapping, receipt and
   commit work, proving complete rollback and safe retry.
4. Candidate-image 10,000-user load and a two-operator rehearsal proving rules
   stay paused and triggers disabled until a separate human action.
