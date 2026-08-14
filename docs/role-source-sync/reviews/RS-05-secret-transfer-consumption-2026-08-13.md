# RS-05 secret transfer and atomic consumption review

Feature: Secret and MCP synchronization

Gate: complete backend and daemon protocol, source-owned target merge, one-time consumption, live PostgreSQL lifecycle

Date: 2026-08-13; live-gate update: 2026-08-14

Decision: CONDITIONAL — the complete lifecycle now passes against a fully migrated PostgreSQL 17 database with real AES-GCM and transaction rollback; production enablement still requires candidate-topology key operation, restart/failover/load and exfiltration evidence

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
- persisted claims and envelopes are re-encoded from strict typed values before
  AAD or digest verification, so PostgreSQL `jsonb` key ordering cannot break
  decryptability or idempotent recovery;
- the authenticated claims expiry is bound back to the persisted expiry at
  PostgreSQL precision, while a bounded one-minute application/database clock
  tolerance validates creation time without extending the 15-minute envelope
  expiry;
- parent Agent/Skill/Autopilot target uniqueness remains database enforced,
  while environment, MCP and capability-binding mappings may share only the
  target already owned through their parent mapping;
- the heartbeat hot path performs no secret claim unless a bounded recovery or push-triggered poll was requested.

Residual constraints:

- workspace serialization deliberately favors correctness over parallel applies inside one tenant;
- global materialization concurrency remains bounded at eight and returns overload instead of queuing unbounded work;
- target Agent secret fields use the existing storage security model;
- the one-minute clock-skew allowance is validation tolerance only; production
  still requires NTP/clock-drift monitoring and a recorded skew alert policy.

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
- a disposable database migrated from 001 through 380 passes the exact
  request → idempotent request → exclusive claim → stale-token rejection →
  AES-GCM submit → idempotent submit → apply consume lifecycle;
- a deterministic failure immediately after consumption proves the transfer,
  Agent, mappings and source snapshot all roll back, followed by a successful
  retry and exact idempotent receipt recovery;
- the successful transaction proves environment and MCP values reach only the
  intended Agent fields, while transfer rows, receipts, audit and outbox contain
  no searchable plaintext marker;
- consumed and expired challenges clear envelope, lease and encrypted private
  key material to exactly 60 zero bytes; expiry uses the production
  `SKIP LOCKED` sweeper query;
- the gate passed once and then three consecutive times, leaving zero users,
  workspaces, Agents, runtimes, sources, transfers, mappings, applies, audit,
  outbox and artifact fixtures after every run;
- migrations 379/380 passed direct down/up SQL round trip, the final partial
  unique index matches only role/skill/automation roots, and the full
  role-source plus secretbox tests and focused `go vet` pass.

Defects found and closed by the live gate:

1. exact request replay failed when application and database clocks differed;
2. PostgreSQL `jsonb` byte reformatting broke private-key AAD decryptability and
   submitted-envelope idempotency;
3. the legacy target-unique index incorrectly prevented environment and MCP
   mappings from sharing their parent Agent.

Missing evidence:

- current/previous keys have not been provisioned through an approved KMS/HSM
  operator procedure or rotated while live transfers span both versions;
- daemon death after claim, lease expiry/reclaim across two daemon processes,
  control-plane process death during submit/apply and database primary failover
  remain candidate-topology gates;
- 10,000-user secret-transfer bursts, envelope/log/trace/backup exfiltration
  scans and alert/SLO behavior remain unmeasured.

## CEO review

Score: 2/3

Value assessment:

- closes the largest adoption blocker for real role import: credentials and MCP definitions can reach runnable Agents without polluting the auditable source model;
- preserves Multica's generic control plane and makes future adapters commercially viable;
- limits breach radius with short-lived, role-scoped challenges and one-time receipts;
- favors fail-closed conflicts over invisible customer configuration loss.

Release decision:

- keep `role_source_apply` disabled by default;
- allow controlled internal/staging use only after current and previous key
  configuration is verified and the B11 live gate is repeated from the
  candidate image;
- do not announce general availability until recovery, failover, scale,
  exfiltration and operator-runbook gates pass.

Final decision: CONDITIONAL
