# RS-04 Agent and Skill name-race review

Date: 2026-08-14

Gate: implementation and live PostgreSQL evidence

Decision: **GO to merge behind default-off apply; NO-GO for production cohort
expansion until Autopilot title races, full apply rollback, failover and
candidate-scale contention pass.**

## Customer outcome

If a teammate creates an Agent or Skill with the source's desired name after
plan preflight but before materialization, Multica keeps exactly the ordinary
workspace object and returns a stable conflict for the Role Source apply. It
does not expose a database error or silently create a duplicate managed object.

## Architecture expert — 3/3 for this slice

- The database's existing tenant-scoped name constraint remains the final
  serialization authority; no second lock namespace or global uniqueness rule
  was introduced.
- Both real batch materializers translate only the exact target constraint plus
  SQLSTATE `23505` to `ErrApplyConflict`.
- Crossed Agent/Skill constraints, other unique violations, serialization
  failures and error strings are deliberately not reclassified.
- The losing transaction rolls back before any provenance mapping is flushed.

Open objection: Autopilot titles intentionally allow ordinary duplicates and
therefore use a different advisory-lock policy. That path needs independent
live proof and must not reuse this unique-constraint classifier.

## Product expert — 3/3 for Agent/Skill conflict behavior

The winner policy is understandable and preserves user authority: the ordinary
workspace create succeeds, while the stale Role Source proposal fails closed
and can be regenerated to present an explicit adoption decision. This avoids
surprise overwrite, duplicate managed objects and database-language errors.

Open objection: controlled-cohort UX still needs to prove that the conflict
message leads operators back to rescan/replan without a blind retry.

## Test expert — 3/3 for same-primary Agent/Skill name claims

On fully migrated PostgreSQL 17, three consecutive runs prove for both target
kinds:

- a real `transactionid` wait on the uncommitted ordinary winner;
- one winner row and typed `state_conflict` for the materializer;
- zero Role Source target/mapping residue;
- zero workspace, actor, Agent, Skill and mapping fixture residue after each
  run;
- focused constraint-classification tests and the complete role-source package
  suite pass.

Open objection: add the separate Autopilot create/rename matrix, full
transaction failure injection around adopted writes, large contention and
primary-failover repetition.

## CEO — 2/3

This removes a common rollout/support failure for teams importing existing
worker catalogs and keeps the product's source-neutral governance model. The
change is small, bounded and immediately reduces repair/support risk.

The business score remains 2 because no customer-cohort conflict rate,
recovery time, throughput or support cost is measured, and Autopilot plus
failover coverage is incomplete.

## Rollout rule

- Keep apply default-off.
- Treat `state_conflict` as a rescan/replan signal, never an automatic retry
  with a new idempotency key.
- Run Gate B9 on every database/driver change.
- Do not generalize this result to Autopilot titles or primary failover.
