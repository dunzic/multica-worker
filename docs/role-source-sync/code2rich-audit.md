# code2rich/multica absorption audit

Reviewed repository: `https://github.com/code2rich/multica`

Reviewed head: `486e6dfcc5fb41539035363d77b88e55767c35aa`

Compared with current local Multica main: `6bce42b84a509a7f5aba4208949e6e4b83f6c574`

## Decision

Absorb concepts and selected pure components; do not merge or cherry-pick the fork wholesale. The fork is based on an older upstream and mixes AgentWaker work with unrelated navigation, WeChat, deployment, and desktop-removal changes. Its database migrations also collide with and violate current main conventions.

## Reusable assets

- AgentWaker 2.1 typed contracts and YAML parsing.
- Deterministic package and directory hashing fixtures.
- Environment preview sanitization tests.
- Daemon-owned directory scan flow and heartbeat capability concept.
- Source-role and source-skill stable identity model.
- Capability profiles, version requirements, permissions, and role bindings.
- Atomic transaction boundary and per-source lock concept.
- Runtime capability materialization concept.
- AgentWaker test fixture with roles, skills, capabilities, MCP, env declarations, and automations.
- UI vocabulary for source configuration, preview, plan, and status.

## Production blockers found

| Severity | Finding | Evidence in fork | Required correction |
| --- | --- | --- | --- |
| Critical | Exact `env/.env` content is embedded under `source_files` in the scan manifest, then the manifest is persisted as a snapshot. This stores plaintext secrets in the audit/snapshot plane. | `server/internal/daemon/agentwaker_scan.go`, `collectLinkedRoleSourceFiles`; `server/internal/handler/agent_source_apply.go`, `envValuesFromRoleSourceFiles` | Snapshot may contain declarations and keyed digests only. Transfer values through a separate one-time encrypted apply channel. |
| Critical | Descendant path validation is incomplete. Registry manifest paths and several reads use `filepath.Join` plus `os.Stat`/`os.ReadFile`, which can follow symlinks or escape the intended package root. | `server/internal/daemon/agentwaker_scan.go`, `scanCapability`, `scanRole`, `readTextFile` | Resolve and verify every declared path under a canonical root, reject symlinks/non-regular files, and protect against read-time swaps. |
| High | “Unchanged” capability content still creates an immutable version and updates the active row. Role application also performs updates, deletes, and reinserts even when hashes match. | `server/internal/handler/agent_source_apply.go`, `applyCapabilities`, `applyRoles` | Plan must prove no-op; apply must skip all domain writes for unchanged objects. |
| High | Same-name skills are silently adopted and overwritten although the design says unrelated same-name objects are explicit conflicts. | `server/internal/handler/agent_source_apply.go`, `applyRoleSkills` | Persist an administrator adoption decision bound to object IDs and plan digest. |
| High | Missing capability/consumer diagnostics can be appended while apply continues; affected role activation is not fail-closed. | `server/internal/handler/agent_source_apply.go`, `applyCapabilityBindings` | Block affected roles before the transaction; preserve last-known-good materialization. |
| High | Rollback mutates an old snapshot state and reapplies without restoring secret versions. | `server/internal/handler/agent_source_plan.go` | Rollback is a new forward apply with its own plan and receipt; declare secret restorability explicitly. |
| High | `watch-assisted` is exposed but treated as manual. Binary artifact support is described as later work while the UI/model suggests the source is complete. | `server/internal/handler/agentwaker_sync.go`; scanner comments | Do not expose unsupported modes. Capability negotiation must drive UI and API behavior. |
| High | Migrations use foreign keys, cascades, inline unique constraints/indexes, non-concurrent indexes, and number ranges already occupied by current main. | fork migrations `163` through `207` | Redesign on current schema with no FKs/cascades and one concurrent index per migration. |
| Medium | Adapter type is hard-coded to AgentWaker in server, protocol, types, routes, and UI. | `agent_source` and `agentwaker` packages | Introduce source-neutral registry, manifest, plan, and storage; keep AgentWaker in an adapter package. |
| Medium | Environment merge policy is documented but `merge-preserve` is not actually implemented. | `server/internal/handler/agent_source_apply.go`, `applyRoleEnv` | Model source-owned keys separately and test both policies against existing user-managed keys. |
| Medium | Audit coverage is a summary returned to the caller rather than a complete append-only, fail-closed lifecycle record. | apply and scan handlers | Persist scan, plan, approval, apply, secret and rollback events with actor and digests. |
| Medium | Manifest bodies are carried in heartbeat/snapshot JSON rather than resolved through bounded content-addressed storage. | scanner capability and skill summaries | Keep metadata bounded; transfer artifacts by digest with authorization and limits. |

## Absorption method

1. Port pure contracts, parser behavior, hashing goldens, and fixtures into the generic adapter boundary.
2. Reimplement filesystem access using one hardened resolver shared by every adapter path read.
3. Normalize AgentWaker output into the generic manifest; no AgentWaker-shaped database columns.
4. Build new migrations against the current main schema and repository migration rules.
5. Port behavior only after a failing test captures the desired production invariant.
6. Keep unrelated fork changes out of this branch.

## Evidence still required before release

- exact commit-by-commit mapping from reusable fork code to new files;
- secret non-persistence test across snapshots, logs, events, errors, analytics, and API responses;
- transaction/idempotency/concurrency test suite;
- tenant and permission matrix;
- full migration and teardown tests;
- runtime materialization hash pin tests for supported runtimes;
- load, soak, fault-injection, backup/restore, and rollback exercises;
- four-perspective review records for every feature slice.
