# RS-04 batched role materialization review

Date: 2026-08-13

Gate: implementation and merge evidence

Final decision: **GO to merge behind the default-off `role_source_apply` flag;
NO-GO for production apply until live PostgreSQL scale and fault evidence
passes.**

## Architecture review — 2/3

- New role-agent identities are allocated by the control plane; existing
  mappings retain their exact target identities.
- One typed JSON statement creates or updates at most 250 roles and 64 MiB per
  batch. A 1,000-role candidate therefore uses four role-target statements
  instead of 1,000 under production-shaped content.
- Updates remain tenant-, user-kind- and non-archived-target constrained and
  change only name, description, runtime binding and instructions. Permission,
  ownership, lifecycle, model, secrets, MCP and user preferences are preserved.
- The returned target-ID set must equal the request exactly. Duplicate,
  missing, unexpected, cross-tenant or archived targets fail the whole apply
  transaction before mappings, receipts or audit can commit.
- Mapping state is advanced only after each complete batch is verified; later
  secret, skill and automation phases consume those in-transaction mappings.
- Open objection: skill and automation create/update paths still issue
  per-object writes, and a real PostgreSQL query plan/WAL/lock profile is not
  recorded.

## Product review — 2/3

- Large role upgrades should spend less time holding the workspace apply lock,
  reducing timeout and ambiguous-commit risk without changing the approved
  plan or receipt visible to an operator.
- Existing customized permission, model and secret settings remain protected;
  batching does not broaden source ownership.
- Open objection: this is performance and reliability infrastructure, not a
  customer-visible workflow. Approval/apply/recovery controls remain disabled
  and require separate operator validation.

## Test review — 2/3

- Tests cover count-bounded batching, the 1,000-role/four-batch shape, exact
  return-set success, duplicate/missing/unexpected returns, tenant filters and
  the source-owned update-field boundary.
- Focused role-source, handler and server packages pass; generated SQL is
  checked into the same change.
- Open objection: no live PostgreSQL test yet proves create/update mixtures,
  statement size, row-lock behavior, rollback after later-batch failure,
  process interruption or retry after an ambiguous commit.

## CEO review — 2/3

- This removes a predictable database round-trip multiplier from enterprise
  role imports while preserving the safety contract, improving the chance that
  a controlled large-customer pilot completes inside an acceptable window.
- The implementation is source-neutral and benefits AgentWaker and future
  adapters without introducing an adapter-specific fork.
- Open objection: commercial value requires measured apply duration, timeout
  reduction, database cost and support-incident change on production-shaped
  tenants.

## Evidence required for 3/3

1. PostgreSQL 17 run for 1,000 roles and 10,000 skills with mixed create/update,
   exact WAL, lock wait, statement latency, transaction duration and pool data.
2. Failure injection after each role batch and in later skill/automation,
   receipt and commit phases, proving complete rollback and safe retry.
3. Concurrent user edits and runtime teardown proving protected fields and lock
   order remain correct.
4. Candidate-image 10,000-user load and a two-operator apply/recovery rehearsal
   with measured time-to-complete and time-to-diagnose.
