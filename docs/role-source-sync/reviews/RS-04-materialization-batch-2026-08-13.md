# RS-04 large materialization transaction review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the existing default-off apply gate;
NO-GO for the 1,000-role/10,000-skill production target until live PostgreSQL
evidence passes.**

## Customer and product outcome

A large role catalog no longer pays one database round trip per role-to-skill
association. The apply transaction stages unique associations in memory and
persists the complete tenant-validated set once. A newly created skill with no
supporting files also avoids two empty database queries. Target-name conflicts
are checked in one tenant query, and immutable capability definitions are
written in batches capped at 500 rows and 2 MiB. Existing skills and capability
packages retain their row locks and file-ownership conflict checks.

## Architecture review — 2/3

- Associations are deterministic and deduplicated by `(agent_id, skill_id)`;
  both endpoints must be active, expected-kind and in the locked workspace.
- `ON CONFLICT DO NOTHING` preserves the previous contract: role-source apply
  cannot silently re-enable a user-disabled association.
- The bulk write remains inside the existing atomic apply transaction. A
  partial or invalid result fails closed and rolls back all domain changes,
  mappings, audit and receipt state.
- The empty-file shortcut is restricted to a skill created in this same
  transaction. Existing targets still lock the skill and enumerate files
  before comparing source-owned paths.
- The name preflight allows only the exact mapped target, rejects any other
  workspace match and rejects plan-internal duplicate target names. Agent and
  skill unique constraints remain the concurrent-write authority.
- Capability batches accept an existing row only when tenant, source, semantic
  version, object digest and canonical JSON definition all match. Count and
  byte limits bound each SQL parameter; a partial result rolls back the apply.
- Open objection: agent, skill, automation and secret updates still include
  per-object statements; autopilot has no database title uniqueness constraint;
  live lock duration and WAL behavior are unknown.

## Product review — 2/3

- The optimization shortens the riskiest large-catalog transaction without
  weakening user ownership, tenant boundaries or audit semantics.
- There is no new operator choice or partially successful state. The same plan,
  approval, receipt and rollback model remains visible.
- Open objection: administrators still need a controlled apply/recovery surface
  with estimated duration, progress, timeout guidance and affected-object
  drill-down before a large customer cohort.

## Test review — 2/3

- Unit contracts cover invalid identifiers, duplicate suppression, canonical
  ordering and a 10,000-association deterministic batch.
- SQL contracts require workspace checks on agent and skill, active user-agent
  scope, conflict-safe insertion and absence of any association-enable update.
- Namespace contracts cover exact mapped-target allowance, any-other-row
  rejection and plan-internal duplicate names. Capability contracts cover exact
  immutable match, 10,000 definitions and 500-row/2-MiB batch bounds.
- A zero-file new-skill test runs with no query object, proving the shortcut
  cannot accidentally invoke empty lock/list calls.
- Existing role-source and race suites retain file ownership, mapping,
  idempotency, atomic apply and user-preservation coverage.
- Open objection: no real PostgreSQL execution plan or 1,000-role/10,000-skill
  transaction timing, cancellation, failover, retry or rollback measurement.

## CEO review — 2/3

- Removing roughly 10,000 association round trips, up to 20,000 empty file
  queries, 10,000 name queries and 10,000 capability insert/fallback-query
  pairs from representative large catalogs materially lowers database time,
  lock exposure and rollout risk with limited code and no product fork.
- The value is operational rather than a new sellable feature; it improves the
  credibility and support cost of enterprise-scale catalogs.
- Open objection: cost and SLA value require measured before/after database
  time, WAL, CPU, lock wait, timeout rate and operator labor in the candidate
  environment.

## Evidence required for 3/3

1. PostgreSQL 17 `EXPLAIN (ANALYZE, BUFFERS, WAL)` for 10,000 new, existing and
   mixed associations, capability versions and names, including disabled rows,
   exact/mismatched definitions and cross-tenant negative cases.
2. End-to-end 1,000-role/10,000-skill apply with transaction duration, pool
   wait, lock wait, statement bytes, WAL, CPU/memory and receipt verification.
3. Cancellation, statement timeout, primary failover, duplicate request and
   commit-response-loss exercises proving atomic rollback or exact receipt
   reconciliation.
4. Operator rehearsal with large-plan preview, safe apply window, progress,
   incident stop/rollback authority and named SRE/product ownership.
