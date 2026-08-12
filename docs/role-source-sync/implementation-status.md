# Implementation status

Updated: 2026-08-13

| Feature | State | Current evidence | Next completion evidence |
| --- | --- | --- | --- |
| RS-01 Adapter registry and source registration | In progress | Source-neutral contract, trusted registry, canonical validation and tests in `server/internal/rolesource`; architecture and review gates accepted | AgentWaker adapter, reference fake adapter contract suite, feature flag, source persistence/API and tenant authorization |
| RS-02 Bounded scan and contract validation | Design and absorption audit | code2rich scanner reviewed; path and secret blockers recorded | Hardened path resolver, AgentWaker adapter scan, artifact transfer, daemon protocol and immutable snapshot tests |
| RS-03 Snapshot, diff, approval and atomic apply | Designed | invariants and failure gates documented | current-main schema, deterministic plan engine, conflict decisions, idempotency, transaction and audit failure injection |
| RS-04 Materialization | Designed | normalized roles/skills/capabilities/bindings/automations defined | stable mapping schema, ownership masks, runtime pin implementation and cross-runtime tests |
| RS-05 Secret and MCP synchronization | Designed, fork approach rejected | plaintext-in-snapshot flaw identified; separate one-time transfer required | envelope protocol, encrypted store, key metadata/rotation, fail-closed audit and exfiltration suite |
| RS-06 Versioning, provenance and rollback | Designed | immutable/forward-rollback policy documented | capability versions, last-known-good activation, retention/GC, backup restore and disaster exercise |
| RS-07 Delivery receipts and external readback | Designed | generic receipt requirement documented | receipt schema, two connector implementations, readback reconciliation and tamper tests |

## Current branch quality evidence

- `go test ./internal/rolesource`
- `go test -race ./internal/rolesource`
- `git diff --check`

These checks cover only the generic contract and registry foundation. They do not prove any end-to-end source synchronization behavior or the 10,000-user production target.
