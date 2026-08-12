# Implementation status

Updated: 2026-08-13

| Feature | State | Current evidence | Next completion evidence |
| --- | --- | --- | --- |
| RS-01 Adapter registry and source registration | In progress | Source-neutral descriptor catalog keeps filesystem authority out of the server; authenticated member/admin APIs, role authorization, default-off dual flags, safe response DTOs, strict request decoding, typed bounded config summary, source persistence/control-plane registration transaction; explicit no-FK workspace teardown protected by workspace mutation locks; AgentWaker implements the daemon adapter contract | managed config create/rotate/remove lifecycle, detach/pause APIs, UI and a second reference adapter conformance proof |
| RS-02 Bounded scan and contract validation | In progress | Hardened root-confined filesystem with root identity recheck; AgentWaker normalization; canonical snapshot identity; generic manifest-bomb limits; private daemon config file with digest key and allowed-root policy; durable per-source scan queue with expiring/renewable leases; explicit capability/poll negotiation; push wakeup plus throttled recovery poll; bounded daemon concurrency; stale-owner rejection; retrying idempotent terminal report API; daemon-side artifact reopen revalidates exact digest/size after scan; missing-only upload uses bounded batches, a private spool, server-side SHA-256 verification, deterministic tenant-scoped object keys and an immutable readiness ledger; snapshots with missing artifact bodies are rejected | managed config UX, large/binary direct-upload protocol, artifact storage readback/repair and GC, richer stable failure taxonomy, source-change retry and live PostgreSQL lease/concurrency/failover evidence |
| RS-03 Snapshot, diff, approval and atomic apply | In progress | Deterministic plans are generated only from persisted, digest-revalidated snapshots; member evidence and admin plan/approval/apply APIs are tenant-scoped and separately default-off; every archive candidate requires an explicit archive/retain decision; approved apply revalidates the current-snapshot CAS and exact approval, serializes the source, commits domain writes/current snapshot/idempotent apply receipt/hash-chained audit in one transaction, and verifies stored receipt digests on retry | failure-at-every-step injection, durable failed-attempt audit, outbox/after-commit events, conflict/adoption decisions, live PostgreSQL duplicate-key/timeout/restart proof and UI |
| RS-04 Materialization | In progress | Stable source-object mappings and ownership masks; roles become private runtime-bound agents, entrypoint-only skills become workspace skills without removing user-added associations, schedule automations become paused run-only rules with disabled deterministic triggers, and capability definitions become immutable versions; every persisted mapping is revalidated against target kind/workspace before use; no-change actions avoid domain writes and capability-version inserts | profile target contract, supporting-file ownership mappings, capability binding/runtime pins, reversible skill/capability removal, explicit adoption, cross-runtime/live-PostgreSQL tests and affected-worker UI |
| RS-05 Secret and MCP synchronization | Designed, fork approach rejected | plaintext-in-snapshot flaw identified; separate one-time transfer required | envelope protocol, encrypted store, key metadata/rotation, fail-closed audit and exfiltration suite |
| RS-06 Versioning, provenance and rollback | Designed | immutable/forward-rollback policy documented | capability versions, last-known-good activation, retention/GC, backup restore and disaster exercise |
| RS-07 Delivery receipts and external readback | Designed | generic receipt requirement documented | receipt schema, two connector implementations, readback reconciliation and tamper tests |

## Current branch quality evidence

- `go test ./internal/rolesource`
- `go test ./internal/rolesource/...`
- `go test ./internal/handler` (includes role-source API/feature-flag/redaction/protocol tests)
- `go test -race ./internal/rolesource/...`
- targeted `go test -race` for role-source handler and adapter packages
- daemon composition tests for private config permissions, symlink/out-of-root rejection, generic AgentWaker execution, negotiation/poll throttling, lease/result wire bodies and bounded capacity
- content-addressed artifact tests for canonical reference collection, post-scan source-change rejection, preflight/upload wire metadata, server hash/size gates and snapshot readiness checks
- full daemon and daemon WebSocket package tests pass with approved local-loopback access
- bounded fuzz runs for env parsing, path confinement and canonical JSON
- deterministic plan, snapshot-integrity, no-change and diagnostic-blocking tests
- plan/approval API tests for default-off behavior, deterministic response validation, exact idempotency-conflict mapping and idempotency-key redaction
- approval policy tests for blocked-plan rejection and exhaustive explicit archive/retain decisions
- apply contract tests for separate rollout gate, unsafe-scope 422 responses, stale snapshot CAS, tamper-evident receipts and ownership-contract blockers
- materialization SQL limits updates to declared source-owned agent/skill/autopilot fields; user status, enablement, secrets, MCP, model, permission and extra skill associations are preserved
- sqlc v1.31.1 generation; migration-policy tests; migration command compilation
- live PostgreSQL isolated-schema migration round-trip test exists but skipped locally because PostgreSQL was not running
- `git diff --check`

These checks cover the generic contract, bounded filesystem, AgentWaker adapter, authenticated API shapes, daemon-local execution, protocol negotiation/renewal, deterministic planning, artifact transfer and the static/unit behavior of atomic apply. They do not prove live PostgreSQL migration/transaction failure behavior, lock contention/failover, runtime capability pins, external readback or the 10,000-user production target.
