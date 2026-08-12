# Implementation status

Updated: 2026-08-13

| Feature | State | Current evidence | Next completion evidence |
| --- | --- | --- | --- |
| RS-01 Adapter registry and source registration | In progress | Source-neutral descriptor catalog keeps filesystem authority out of the server; authenticated member/admin APIs, role authorization, default-off dual flags, safe response DTOs, strict request decoding, typed bounded config summary, source persistence/control-plane registration transaction; AgentWaker implements the daemon adapter contract | daemon-local config lifecycle, detach/pause APIs, workspace teardown, UI and a second reference adapter conformance proof |
| RS-02 Bounded scan and contract validation | In progress | Hardened root-confined filesystem; AgentWaker normalization; canonical snapshot identity; generic manifest-bomb limits; durable per-source scan queue with expiring leases; explicit capability/poll heartbeat negotiation; push wakeup; one-scan leases; stale-owner rejection; idempotent terminal report API; content-deduplicated immutable snapshot persistence; same-transaction hash-chained audit events | daemon config store and scan executor, scheduled recovery poll, artifact transfer, hostile manifest fuzzing, source-change retry and live PostgreSQL lease/concurrency evidence |
| RS-03 Snapshot, diff, approval and atomic apply | In progress | Deterministic source-neutral plan engine; nested object hashes; archive candidates; diagnostic blocking; tamper validation; current-main source/snapshot/plan/approval/apply/audit schema and generated queries follow no-FK/concurrent-index rules | conflict/adoption decisions, materialization transaction, source current-snapshot CAS, apply idempotency service and audit failure injection |
| RS-04 Materialization | Designed | normalized roles/skills/capabilities/bindings/automations defined | stable mapping schema, ownership masks, runtime pin implementation and cross-runtime tests |
| RS-05 Secret and MCP synchronization | Designed, fork approach rejected | plaintext-in-snapshot flaw identified; separate one-time transfer required | envelope protocol, encrypted store, key metadata/rotation, fail-closed audit and exfiltration suite |
| RS-06 Versioning, provenance and rollback | Designed | immutable/forward-rollback policy documented | capability versions, last-known-good activation, retention/GC, backup restore and disaster exercise |
| RS-07 Delivery receipts and external readback | Designed | generic receipt requirement documented | receipt schema, two connector implementations, readback reconciliation and tamper tests |

## Current branch quality evidence

- `go test ./internal/rolesource`
- `go test ./internal/rolesource/...`
- `go test ./internal/handler` (includes role-source API/feature-flag/redaction/protocol tests)
- `go test -race ./internal/rolesource/...`
- targeted `go test -race` for role-source handler and adapter packages
- compile-only checks for daemon and daemon WebSocket packages; full listener-based tests are blocked by the current filesystem/network sandbox
- bounded fuzz runs for env parsing, path confinement and canonical JSON
- deterministic plan, snapshot-integrity, no-change and diagnostic-blocking tests
- sqlc v1.31.1 generation; migration-policy tests; migration command compilation
- live PostgreSQL isolated-schema migration round-trip test exists but skipped locally because PostgreSQL was not running
- `git diff --check`

These checks cover the generic contract, bounded filesystem, AgentWaker adapter, authenticated API shapes, daemon protocol negotiation, deterministic planning and persistence code shape. They do not prove daemon-local execution, live PostgreSQL migrations/leases, materialization/apply behavior or the 10,000-user production target.
