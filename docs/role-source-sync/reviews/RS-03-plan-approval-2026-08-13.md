# RS-03 persisted plan and approval design review

Feature: Snapshot, diff, approval and atomic apply

Gate: design and implementation evidence for persisted plan/approval; atomic apply excluded

Date: 2026-08-13

Decision: CONDITIONAL — merge behind disabled sync/scan flags; apply remains unavailable

## Delivered boundary

An authenticated workspace administrator can generate a deterministic plan from a selected immutable snapshot, approve or reject it with an idempotency key, and retrieve snapshot, plan and approval evidence. Members have read-only evidence access. The server never reads the mutable source while planning. Before use or response it recomputes snapshot/plan integrity and verifies the JSON body against indexed digest columns.

Approval is deliberately narrower than apply. A blocked plan cannot be approved. Every archive candidate requires one explicit `archive` or `retain` decision. Rejection cannot carry apply decisions. Exact retries return the existing approval; reuse of a request key with different plan, decision, actor or decisions returns a conflict. The client retry key is not exposed by ordinary responses.

## Architecture expert review

Score: 2/3

Accepted evidence:

- plan generation locks the workspace/source and reads only tenant-scoped persisted snapshots;
- source UUIDs are canonicalized before they enter deterministic plan identity;
- target and current snapshots are fully digest-revalidated after JSONB reconstruction;
- persisted plans are revalidated, including digest, summary, action semantics and indexed snapshot columns;
- approval decisions are typed, canonicalized and exhaustive for archive candidates;
- approval creation and its hash-chained audit event commit in one transaction;
- `(source_id, request_key)` gives per-source idempotency without globally correlating client keys;
- schema evolution contains no foreign keys, and the unique index is isolated in one concurrent-index migration;
- member responses omit daemon configuration, lease tokens and approval request keys.

Open objections:

- there is no conflict/adoption inventory or field-level ownership mask yet;
- approval is not yet consumed by an atomic materialization transaction;
- pagination uses a bounded first page but has no cursor contract;
- live PostgreSQL migration, duplicate-key race and lock-contention evidence is unavailable locally.

## Product expert review

Score: 1/3

Accepted evidence:

- the API distinguishes creates, updates, unchanged objects, blocked objects and archive candidates;
- archive versus retain is an explicit administrator decision rather than an implicit delete;
- members can inspect immutable evidence while only administrators can generate or decide plans.

Open objections:

- there is no UI, localized explanation or affected-running-worker preview;
- same-name unmanaged objects cannot yet be presented as adopt/keep-separate choices;
- a successful approval does not yet produce a runnable worker or a receipt.

## Test expert review

Score: 2/3

Accepted evidence:

- pure tests cover missing archive decisions, explicit retain, blocked-plan rejection and rejection payload rules;
- persisted snapshot/plan reconstruction rejects tampered body/column identities;
- handler tests prove default-off isolation, deterministic plan response validation, conflict mapping and request-key redaction;
- strict JSON decoding rejects unknown fields, including nested approval decisions;
- role-source, handler and server packages pass focused tests after sqlc regeneration.

Open objections:

- exact retry, conflicting retry and concurrent approval need a live PostgreSQL integration suite;
- migration up/down execution has not run against a live PostgreSQL instance in this environment;
- 2026-08-14 evidence update: the live PostgreSQL atomic-apply matrix now covers
  ordered failure injection, post-commit timeout/cancellation and a newly
  constructed control plane after simulated process loss; real container kill,
  primary failover and database-total-outage durability remain open;
- large plan/approval-list benchmark evidence is not yet available.

## CEO review

Score: 2/3

Accepted evidence:

- immutable previews and attributable approvals are reusable enterprise control-plane value rather than AgentWaker-specific behavior;
- explicit removal decisions reduce a high-cost class of accidental bulk deletion;
- deterministic evidence makes future upgrade, rollback and compliance reporting commercially credible.

Open objections:

- customers still cannot complete an end-to-end upgrade;
- time saved, incident reduction and support burden are not measurable before UI/materialization pilots;
- the 10,000-user concurrency and recovery target remains unproven.

## Security, privacy and data-loss blockers

- `role_source_apply` remains unavailable until approved decisions, mappings, domain mutations, current-snapshot compare-and-swap, receipt and audit commit atomically.
- Same-name unmanaged objects remain conflicts until explicit adoption exists.
- Approval never authorizes secret transfer; RS-05 requires a separate one-time encrypted protocol.
- The feature flags stay disabled by default and no rollout is permitted without live database and failure-injection evidence.

Final decision: CONDITIONAL
