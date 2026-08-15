# RS-06 review — immutable runtime task pins

Feature: Versioning, provenance and rollback — task/runtime pin slice

Date: 2026-08-13

Evidence update: 2026-08-15

Decision: CONDITIONAL — merge behind the existing default-off role-source flags; do not enable for production cohorts until the live PostgreSQL, load and operator/UI gates below pass

## Decision record

Every task created for a source-managed agent receives an immutable, content-free provenance row at enqueue. It identifies the source, source role, snapshot, normalized role object and exact capability versions. All insert paths are covered by one database trigger. A system retry copies the parent pin byte-for-byte and records the parent task; it never resolves current source state.

The current materialized agent is still mutable storage, so the claim path fails closed when its current target-state digest or mapping no longer matches the pin. A source apply advances unchanged mappings to the new snapshot and cancels `queued`, `deferred` and not-yet-finalized `dispatched` tasks carrying the old pin inside the same transaction. Final claim commit locks the role mapping and then the exact task dispatch generation, matching apply's lock order and closing the last validation-to-token race. `running` and `waiting_local_directory` tasks are deliberately not cancelled: they already received their payload and continue under the provenance delivered at claim.

The pin contains no prompts, environment values, MCP definitions, artifact bodies or source paths. Members can list pins by source through bounded composite-cursor pagination. Daemon claims receive the same typed wire contract.

## Architecture expert review

Score: 2/3 — approve the invariant and domain boundary; production proof remains open.

Accepted:

- capture is centralized at the persistence boundary rather than duplicated across issue, chat, quick-create, deferred and autopilot handlers;
- retry inheritance preserves historical identity and prevents a rollback/apply race from silently changing a retry;
- source-specific AgentWaker fields remain outside the generic pin;
- the target-state digest covers the materialized agent, source-managed skills, supporting files and agent-skill associations without persisting another plaintext copy;
- mapping advancement and stale-task cancellation share the apply transaction, while finalization locks mapping then task generation, closing the validation/read/token race for tasks that have not begun;
- ordinary agents have no pin row and no larger queue row.

Objections:

- the database function recomputes a role target digest on enqueue and claim; representative workloads must prove this stays inside the claim SLO for roles with many skills/files;
- runtime reconstruction of an old encrypted configuration is not implemented. The safe behavior is cancellation and explicit re-enqueue, not execution of the old task;
- read-only capability bindings materialize into an owned skill namespace; a new daemon capability gate rejects older daemons, and the runtime verifies the exact marker, version, permission and every declared file digest before execution;
- retention must keep every snapshot/capability version reachable from retained task pins.

## Product expert review

Score: 2/3 — high trust value, incomplete operator experience.

Accepted:

- a run can answer “which role source and version did this task use?” rather than showing only the current Agent configuration;
- queued work cannot silently change meaning after a bulk role upgrade;
- cancellation is explicit and safer than pretending an old task ran its pinned version while using new secrets or skills;
- paginated member-visible provenance supports audit and support workflows without exposing confidential content.

Objections:

- UI must show “role updated after enqueue; create a new run” and offer a deliberate re-enqueue action;
- affected-task preview belongs in the apply plan before an administrator approves a bulk update;
- product copy must distinguish a system retry, manual rerun and re-enqueue-after-version-change;
- operators need filters by agent, task, snapshot and stale outcome in addition to the source timeline.

## Test expert review

Score: 2/3 — local PostgreSQL trigger and retention-race evidence passes;
candidate-topology load, failover and restore evidence is incomplete.

Passing evidence:

- sqlc generation and repository migration-policy tests;
- static contract tests prove no runtime plaintext fields are stored in the pin and retries copy parent snapshot/capability evidence;
- handler tests reject malformed capability evidence and prove the daemon wire shape is content-free;
- daemon client test proves typed batch-claim decoding with an authoritative empty capability list;
- execution-environment tests prove nested capability marker/package files survive workspace-native skill materialization across every file-based provider plus Codex and Hermes task homes;
- role-source/handler/migration race suites and `go vet` pass;
- the isolated-schema PostgreSQL 17 round trip through migration 380 exercises initial capture, retry inheritance, immutable pins and same-transaction stale-task invalidation;
- a deterministic live race gate proves pin-first holds `FOR KEY SHARE` and defers prune, while prune-first deletes then rejects the later pin with SQLSTATE `23000`; all four hold/pin cases pass three consecutive runs;
- deterministic planning at 10,000 roles completes locally; this is algorithm evidence, not an end-to-end capacity claim.

Missing evidence:

- inject concurrent claim/apply commits around each statement and prove no stale payload crosses finalization;
- benchmark pin capture/validation with realistic skill/file sizes, 100 concurrent scans and the target task claim rate;
- live rolling-upgrade deployment proof; the server-side old-daemon rejection path is covered by the negotiated capability contract;
- candidate-topology retention/GC, primary failover and backup-restore tests must retain pin-reachable snapshots and capability versions.

## CEO review

Score: 2/3 — preserves the differentiating audit promise; rollout remains blocked.

Business value:

- turns bulk role management from mutable configuration into defensible execution evidence;
- reduces enterprise incident cost by making configuration drift and retry provenance queryable;
- keeps the generic contract suitable for upstream contribution while retaining AgentWaker as an adapter;
- fail-closed behavior protects trust during the period before fully versioned runtime reconstruction exists.

Release position:

- merge as default-off infrastructure and continue implementation;
- no production marketing or “safe rollback” claim until live database race evidence, affected-task UX, retention and load gates pass;
- prioritize affected-worker preview and the explicit cancellation/re-enqueue workflow next; hard write-permission and executable-adapter contracts must remain separate safety gates.

## Exit gates

| Gate | Evidence required | Status |
| --- | --- | --- |
| Migration safety | Fresh/full schema up/down plus trigger behavior on supported PostgreSQL versions | Local PostgreSQL 17 pass through migration 380; candidate managed-version matrix remains open |
| Claim/apply atomicity | Deterministic concurrency test around dispatched/finalized/running boundaries | Open |
| Claim SLO | p95 < 500 ms and p99 < 1 s at target profile with realistic role dependencies | Open |
| Runtime reconstruction | Either versioned old-config execution or explicit cancellation/re-enqueue UX | Partial — safe cancellation only |
| Capability execution | Materialized binding and exact version pin consumed by runtime | Partial — read-only packages verify marker and file digests; live cross-runtime and write/adapter authority remain open |
| Audit UX | Source/task/version timeline, affected-task preview and re-enqueue action | Open |
| Retention/DR | Pin reachability, GC, backup and restore exercise | Partial — local pin/prune commit-order race passes; versioned-object GC, failover and restore remain open |
