# RS-04 atomic materialization design review

Feature: Role, skill, capability and automation materialization

Gate: design and implementation evidence; rollout excluded

Date: 2026-08-13

Decision: CONDITIONAL — merge behind disabled `role_source_apply`; no production cohort

## Delivered boundary

An authenticated workspace administrator can apply one exact approved plan with a bounded idempotency key. The transaction revalidates the immutable plan, target snapshot, approval decisions, source current-snapshot compare-and-swap, selected Runtime, artifact readiness/body digests and every existing target mapping before writing. Supported objects are roles, entrypoint-only skills, immutable capability definitions and schedule automations. A successful transaction commits mappings, domain objects, source snapshot advancement, a tamper-evident receipt and one hash-chained audit event together.

The initial materializer rejects rather than ignores role profiles, supporting skill files, capability bindings, environment declarations and MCP definitions. Skill/capability removal can only be retained; reversible archive semantics do not exist yet. Created automations are paused and schedule triggers disabled. Updates never resume them and preserve user-controlled enablement/status. Same-name unmanaged targets are conflicts; implicit adoption is forbidden.

## Architecture expert review

Score: 2/3

Accepted evidence:

- source row locking plus current-snapshot CAS rejects stale and concurrent plans;
- exact retries revalidate indexed receipt identity and canonical receipt digest;
- object mappings use stable source identity, explicit target kind and field ownership masks;
- no-FK mappings are tenant/kind/target-revalidated in bulk before use;
- only bodies copied into mutable runtime tables are loaded; capability/supporting packages remain content-addressed for their later runtime contracts instead of inflating apply memory;
- materialization preflight batch-loads the tenant artifact ledger and verifies bodies through content-addressed storage with bounded 16-way reads, SHA-256/size/UTF-8 checks and a 20,000-entrypoint/128 MiB ceiling;
- slow object-storage verification completes before mutation locks; the transaction reloads the exact snapshot and takes one batched shared ledger lock before using the verified bodies;
- all mapping changes are staged in memory and flushed through one typed JSON recordset; per-row stale-task triggers still execute, but thousands of mapping network round trips are removed;
- eight server-local materialization slots bound concurrent storage/transaction pressure;
- agents, skills and automations use narrow SQL that preserves user-owned fields;
- capability versions are immutable and content-digested;
- apply, mapping/domain mutations, source CAS, receipt and audit commit in one transaction.

Open objections:

- failed attempts that roll back are not yet durably recorded in a separate failure ledger;
- capability consumers do not exist; task/runtime provenance pins now do;
- no outbox publishes post-commit domain refresh events;
- live PostgreSQL deadlock, timeout and retry behavior is unproven.
- agent/skill/automation domain materialization still performs per-object writes, so the 1,000-role/10,000-skill transaction SLO is not yet measured or fully optimized.

## Product expert review

Score: 1/3

Accepted evidence:

- safe simple roles become private agents on the selected Runtime;
- imported skills are attached without deleting user-added associations;
- imported schedules cannot unexpectedly start work;
- unsupported semantics return an explicit blocker instead of silently losing source intent;
- archive/retain decisions remain visible in the approval and receipt.

Open objections:

- AgentWaker roles with profile/MCP/environment/bindings remain blocked;
- there is no UI for ownership, target conflicts, blockers or receipts;
- skill/capability removal is retain-only and adoption is unavailable;
- capability definitions are catalogued but not runnable.

## Test expert review

Score: 2/3

Accepted evidence:

- unit tests cover materialization scope acceptance/blocking, retain-only skill removal, snapshot CAS and receipt tampering;
- handler tests cover the independent apply flag, strict request forwarding, request-key redaction and unsafe-scope status mapping;
- existing snapshot/plan/approval digest and policy tests remain green;
- workspace cleanup includes mappings and capability versions;
- migration policy rejects foreign keys, inline indexes and multi-statement concurrent-index migrations;
- role-source and handler package tests pass after sqlc regeneration.
- deterministic capacity contracts now cover 1,000 roles plus 10,000 unique skill entrypoints (about 86 MiB declared content) and prove that deferred 10 GiB packages are excluded from apply memory;
- a source-order regression test proves object storage preflight precedes transaction begin and the in-transaction ledger recheck is one batched `FOR SHARE` query.
- mapping staging tests reject duplicate mutations, and the SQL contract uses one `jsonb_to_recordset` upsert while preserving row-trigger invalidation.

Open objections:

- no live PostgreSQL all-or-nothing failure injection at every mutation boundary;
- no concurrent same-key/same-source/different-source apply test;
- no timeout-after-commit, restart retry or object-store corruption integration test;
- no live 1,000-role/10,000-skill apply, 10,000-user load, lock-wait or object-storage latency benchmark.

## CEO review

Score: 2/3

Accepted evidence:

- stable mappings and ownership boundaries are reusable platform primitives, not AgentWaker-specific branching;
- paused-by-default automations and tamper-evident receipts reduce enterprise rollout risk;
- immutable capability versions preserve a credible path to shared capability reuse and marketplace economics.

Open objections:

- the supported slice does not yet make a representative AgentWaker package fully runnable;
- operational cost and sync-time SLOs are unmeasured; the new preflight removes avoidable lock time but does not prove the end-to-end SLO;
- the 10,000-user target and recovery story remain unproven.

## Security, privacy and data-loss blockers

- `role_source_apply` remains disabled by default.
- No plaintext secret transfer is authorized; environment/MCP/bindings fail closed.
- No implicit same-name adoption or destructive skill/capability deletion is allowed.
- Production rollout requires live migration, transaction failure injection, timeout retry and scale evidence.

Final decision: CONDITIONAL
