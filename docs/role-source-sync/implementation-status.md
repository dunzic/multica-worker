# Implementation status

Updated: 2026-08-13

| Feature | State | Current evidence | Next completion evidence |
| --- | --- | --- | --- |
| RS-01 Adapter registry and source registration | In progress | Source-neutral contract, trusted registry, canonical validation, redacted configuration view and tests in `server/internal/rolesource`; AgentWaker implements the same adapter contract | Feature flag, source persistence/API, tenant authorization and a second reference adapter conformance proof |
| RS-02 Bounded scan and contract validation | In progress | Hardened root-confined filesystem; AgentWaker roles, skills and bounded supporting artifacts, capabilities and requirements, bindings, environment metadata, MCP and automations normalize into a secret-free manifest; traversal/symlink/no-mutation tests pass | Daemon queue/protocol, content transfer, immutable snapshot persistence, hostile manifest fuzzing and source-change retry |
| RS-03 Snapshot, diff, approval and atomic apply | Designed | invariants and failure gates documented | current-main schema, deterministic plan engine, conflict decisions, idempotency, transaction and audit failure injection |
| RS-04 Materialization | Designed | normalized roles/skills/capabilities/bindings/automations defined | stable mapping schema, ownership masks, runtime pin implementation and cross-runtime tests |
| RS-05 Secret and MCP synchronization | Designed, fork approach rejected | plaintext-in-snapshot flaw identified; separate one-time transfer required | envelope protocol, encrypted store, key metadata/rotation, fail-closed audit and exfiltration suite |
| RS-06 Versioning, provenance and rollback | Designed | immutable/forward-rollback policy documented | capability versions, last-known-good activation, retention/GC, backup restore and disaster exercise |
| RS-07 Delivery receipts and external readback | Designed | generic receipt requirement documented | receipt schema, two connector implementations, readback reconciliation and tamper tests |

## Current branch quality evidence

- `go test ./internal/rolesource`
- `go test ./internal/rolesource/...`
- `go test -race ./internal/rolesource/...`
- bounded fuzz runs for env parsing, path confinement and canonical JSON
- `git diff --check`

These checks cover the generic contract, bounded filesystem and AgentWaker adapter. They do not prove daemon/server persistence, apply behavior or the 10,000-user production target.
