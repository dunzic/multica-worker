# Implementation status

Updated: 2026-08-13

| Feature | State | Current evidence | Next completion evidence |
| --- | --- | --- | --- |
| RS-01 Adapter registry and source registration | In progress | Source-neutral contract, trusted registry, canonical validation, typed bounded config summary, source persistence/control-plane registration transaction, separate sync/scan/apply flag keys; AgentWaker implements the adapter contract | authenticated API, membership/admin authorization, daemon capability negotiation, workspace teardown and a second reference adapter conformance proof |
| RS-02 Bounded scan and contract validation | In progress | Hardened root-confined filesystem; AgentWaker normalization; canonical snapshot identity; generic manifest-bomb limits; durable per-source scan queue with expiring leases, stale-owner rejection, content-deduplicated immutable snapshot persistence, and same-transaction hash-chained audit events | daemon heartbeat/API wiring, artifact transfer, hostile manifest fuzzing, source-change retry and live PostgreSQL lease/concurrency evidence |
| RS-03 Snapshot, diff, approval and atomic apply | In progress | Deterministic source-neutral plan engine; nested object hashes; archive candidates; diagnostic blocking; tamper validation; current-main source/snapshot/plan/approval/apply/audit schema and generated queries follow no-FK/concurrent-index rules | conflict/adoption decisions, materialization transaction, source current-snapshot CAS, apply idempotency service and audit failure injection |
| RS-04 Materialization | Designed | normalized roles/skills/capabilities/bindings/automations defined | stable mapping schema, ownership masks, runtime pin implementation and cross-runtime tests |
| RS-05 Secret and MCP synchronization | Designed, fork approach rejected | plaintext-in-snapshot flaw identified; separate one-time transfer required | envelope protocol, encrypted store, key metadata/rotation, fail-closed audit and exfiltration suite |
| RS-06 Versioning, provenance and rollback | Designed | immutable/forward-rollback policy documented | capability versions, last-known-good activation, retention/GC, backup restore and disaster exercise |
| RS-07 Delivery receipts and external readback | Designed | generic receipt requirement documented | receipt schema, two connector implementations, readback reconciliation and tamper tests |

## Current branch quality evidence

- `go test ./internal/rolesource`
- `go test ./internal/rolesource/...`
- `go test -race ./internal/rolesource/...`
- bounded fuzz runs for env parsing, path confinement and canonical JSON
- deterministic plan, snapshot-integrity, no-change and diagnostic-blocking tests
- sqlc v1.31.1 generation; migration-policy tests; migration command compilation
- live PostgreSQL isolated-schema migration round-trip test exists but skipped locally because PostgreSQL was not running
- `git diff --check`

These checks cover the generic contract, bounded filesystem, AgentWaker adapter, deterministic planning and persistence code shape. They do not prove live daemon/API integration, materialization/apply behavior or the 10,000-user production target.
