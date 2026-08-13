# RS-04 batched Skill materialization review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL scale and fault evidence
passes.**

## Architecture review — 2/3

- New Skill identities are allocated by the control plane; mapped Skills keep
  their exact target identities.
- Main-row create/update requests are split at 250 rows and 64 MiB. The
  10,000-Skill candidate shape therefore uses 40 main-row statements instead
  of 10,000.
- Updates remain workspace-scoped and change only name, description and
  content. Existing config and creator ownership are preserved.
- Duplicate, missing, unexpected, cross-tenant or missing mapped targets make
  the returned ID set differ and fail the whole apply transaction before
  bindings, mappings, receipts or audit commit.
- Supporting-file ownership was not flattened into the main-row batch. Each
  Skill still checks user/source path collisions, removes only its owned paths
  and verifies the exact upsert result before staging its mapping.
- Open objection: existing Skills with supporting files still incur per-object
  lock/list/delete/upsert work; automation writes also remain per object.

## Product review — 2/3

- Large role packages should no longer pay one Skill-main-row round trip per
  object, reducing apply-lock duration and timeout exposure without changing
  the approved plan, receipt or visible ownership model.
- User-authored config and creator attribution remain protected, and user-owned
  supporting files still cause a safe conflict rather than silent overwrite.
- Open objection: this is backend reliability work. Customer approval, apply,
  progress, cancellation and recovery controls remain intentionally disabled.

## Test review — 2/3

- Tests cover count-bounded batching, the 10,000-Skill/40-batch shape, exact
  returned-ID success, duplicate/missing/unexpected returns, workspace scope
  and config/creator update protection.
- Existing tests continue to cover supporting-file ownership, empty-file fast
  paths, Agent-Skill association exactness and mapping batch exactness.
- Focused role-source, handler and server packages pass; generated SQL is
  checked into this change.
- Open objection: no live PostgreSQL test measures mixed create/update batches,
  unique-name races, WAL, locks, statement latency, rollback after a later
  file conflict, process interruption or commit ambiguity.

## CEO review — 2/3

- Reducing a 10,000-Skill import from 10,000 main-row statements to 40 bounded
  statements materially improves the feasibility of controlled enterprise
  pilots without weakening customer-owned data boundaries.
- The optimization is source-neutral and benefits AgentWaker and all future
  adapters using the normalized role contract.
- Open objection: value must be demonstrated through measured apply duration,
  timeout/rollback rates, database cost and support incidents on representative
  tenants.

## Evidence required for 3/3

1. PostgreSQL 17 run with 1,000 roles and 10,000 Skills, mixed create/update,
   supporting files, exact query/WAL/lock/pool/transaction measurements.
2. Failure injection after each Skill batch and during file, binding, mapping,
   receipt and commit phases, proving complete rollback and safe retry.
3. Concurrent user file/config edits and unique-name collisions proving source
   ownership and database constraints remain authoritative.
4. Candidate-image 10,000-user load plus a two-operator apply/recovery rehearsal
   with measured time-to-complete and time-to-diagnose.
