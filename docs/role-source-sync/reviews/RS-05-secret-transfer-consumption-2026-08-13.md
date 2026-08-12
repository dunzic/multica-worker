# RS-05 secret transfer and atomic consumption review

Feature: Secret and MCP synchronization

Gate: complete backend and daemon protocol, source-owned target merge, one-time consumption

Date: 2026-08-13

Decision: CONDITIONAL — code-complete and feature-flagged; production enablement still requires live PostgreSQL migration/round-trip evidence and operator key provisioning

## Delivered customer outcome

An administrator can request a one-time secret transfer for one approved role and pass the returned transfer ID into the matching plan apply. Only a daemon that explicitly negotiates `role-source-secret-transfer-v1` can claim the challenge. The daemon rescans the local source, requires the exact approved snapshot digest, revalidates every environment HMAC and MCP definition hash, and uploads an X25519/AES-GCM envelope. The server authenticates and stores only ciphertext until the approved apply transaction.

Apply requires an exact role-to-transfer-ID map. In the same workspace-serialized transaction it validates plan, approval, snapshot, transfer, tenant, runtime, role, expiry, key version and envelope digest; decrypts only in memory; merges only mapped source-owned environment keys and MCP server IDs; preserves unrelated user-owned entries; writes the target Agent; consumes the transfer; clears recoverable challenge ciphertext; advances snapshot CAS; and emits tamper-evident audit and receipt evidence. A collision with a user-owned key/server fails instead of taking ownership silently.

The existing Agent storage contract remains unchanged: `agent.custom_env` and `agent.mcp_config` are the platform's plaintext destination fields and depend on database/back-up access controls. The new protocol prevents plaintext from entering role-source snapshots, plans, transfer rows, receipts, daemon hints, logs and ordinary role-source APIs; it does not claim application-level encryption of the existing Agent fields.

## Architecture expert review

Score: 3/3 for the implementation boundary

Accepted evidence:

- adapter protocol is source-neutral; AgentWaker filesystem and format logic stays in its adapter;
- an independent capability bit prevents old scan-only daemons from claiming sensitive work;
- the 15-minute challenge and five-minute lease are tenant/runtime/source/role/snapshot bound;
- persisted private keys use master-key AEAD with claims as additional authenticated data;
- key IDs select a current encryption key or previous decrypt-only keys during rotation;
- apply consumes secrets inside the same transaction as target changes, source snapshot CAS, receipt and audit;
- environment and MCP use independent object mappings, so source ownership is explicit and user additions survive updates;
- expired and consumed rows clear envelope and encrypted ephemeral private-key material;
- the heartbeat hot path performs no secret claim unless a bounded recovery or push-triggered poll was requested.

Residual constraints:

- workspace serialization deliberately favors correctness over parallel applies inside one tenant;
- global materialization concurrency remains bounded at eight and returns overload instead of queuing unbounded work;
- target Agent secret fields use the existing storage security model.

## Product expert review

Score: 2/3

Accepted evidence:

- the API supports request, asynchronous daemon transfer and exact apply consumption;
- transfer IDs and envelope digests appear in the receipt without exposing values;
- stale source, expired challenge, missing transfer, user-owned collision and unsupported adapter have actionable failure classes;
- secret removal cannot happen through a generic archive: removed environment/MCP objects must be retained, while configured-to-unconfigured changes are explicit approved updates.

Open product work:

- no web UI yet guides the multi-role request/wait/apply sequence;
- MCP review currently exposes identity and immutable hash rather than a separately redacted command preview;
- rotation/recovery runbooks need operator-facing documentation.

## Test expert review

Score: 2/3

Passing evidence:

- cryptographic round trip, claim replay binding, expiry, tamper, size and unsafe-name tests;
- daemon rescan-to-envelope-to-decrypt round trip with no plaintext wire fields;
- independent heartbeat capability negotiation and client result protocol tests;
- AgentWaker post-scan environment and MCP mutation rejection;
- public request response excludes request key, private key ciphertext, key ID and envelope;
- exact payload set/hash validation, keyring rotation selection, receipt tamper detection and migration-policy tests;
- focused Go package, daemon, WebSocket, server and migration tests pass; race/vet must be rerun at the final gate.

Missing evidence:

- no local PostgreSQL is listening and no `DATABASE_URL`/`TEST_DATABASE_URL` is configured, so migrations 304–308 and the full request/claim/submit/apply/consume transaction have not run against a live database;
- process restart, database failover and 10,000-user load/fault injection remain final production-gate work.

## CEO review

Score: 2/3

Value assessment:

- closes the largest adoption blocker for real role import: credentials and MCP definitions can reach runnable Agents without polluting the auditable source model;
- preserves Multica's generic control plane and makes future adapters commercially viable;
- limits breach radius with short-lived, role-scoped challenges and one-time receipts;
- favors fail-closed conflicts over invisible customer configuration loss.

Release decision:

- keep `role_source_apply` disabled by default;
- allow controlled internal/staging use only after current and previous key configuration is verified;
- do not announce general availability until live PostgreSQL, recovery, scale and operator-runbook gates pass.

Final decision: CONDITIONAL
