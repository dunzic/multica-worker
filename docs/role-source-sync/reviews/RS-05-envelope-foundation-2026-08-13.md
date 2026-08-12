# RS-05 secret-envelope foundation review

Feature: Secret and MCP synchronization

Gate: cryptographic and adapter foundation; persistence/protocol/materialization excluded

Date: 2026-08-13

Decision: CONDITIONAL — merge as unreachable foundation; do not advertise secret transfer

Status note: this foundation-only decision is superseded by `RS-05-secret-transfer-consumption-2026-08-13.md` for the subsequent protocol and consumption implementation.

## Delivered boundary

The generic role-source package now defines an authenticated one-time envelope using an ephemeral X25519 sender key, HKDF-SHA256 and AES-256-GCM. Authenticated claims bind transfer ID, workspace, source, role, immutable snapshot and a canonical UTC expiry of at most 15 minutes. Payloads are bounded to environment values and canonical MCP definitions. Plaintext is absent from the serialized envelope.

The existing at-rest secret box gained additional authenticated data so a valid encrypted row cannot be moved to another tenant/object/version context. The AgentWaker adapter implements a daemon-only exporter: it reopens `.env` and MCP files through the bounded filesystem and compares every value HMAC, server definition hash, environment reference and object set to the already validated snapshot. Any source change requires rescan and reapproval.

No API, heartbeat capability, database challenge or materialization path invokes this foundation yet. AgentWaker continues to advertise `secret_transfer: false`.

## Architecture expert review

Score: 2/3

Accepted evidence:

- envelope confidentiality/integrity uses standard-library X25519, HKDF-SHA256 and AES-256-GCM;
- tenant/source/role/snapshot/transfer/expiry claims are AEAD additional data;
- keys, values, MCP objects and total plaintext/ciphertext have explicit limits before expensive decode/allocation;
- sender keys are ephemeral and server key pairs are generated with the system CSPRNG;
- the generic registry verifies snapshot and adapter identity before daemon secret export;
- AgentWaker-specific paths and formats remain inside the adapter.

Open objections:

- challenge/private-key persistence, replay consumption and server master-key version metadata are not implemented;
- secret-store rotation and runtime-only reveal are not implemented;
- Go string values cannot guarantee physical memory erasure; callers must minimize copies and clear reachable maps;
- transport authorization and audit are not wired.

## Product expert review

Score: 1/3

Accepted evidence:

- values can eventually move without entering snapshots, plans or ordinary APIs;
- changed source secrets fail closed against the reviewed snapshot;
- missing/changed configuration can be represented without revealing values.

Open objections:

- customers cannot initiate or complete a transfer;
- ownership policy, removal semantics, rotation UX and recovery are undefined;
- MCP compatibility and affected-role explanations are not exposed.

## Test expert review

Score: 2/3

Accepted evidence:

- tests prove round-trip encryption without plaintext wire exposure;
- cross-workspace/source/role/snapshot/transfer claim changes fail authentication;
- wrong private key, ciphertext tampering, expiry, unsafe names and oversized values fail;
- at-rest ciphertext fails when additional authenticated context changes;
- AgentWaker export succeeds for matching evidence and rejects post-scan environment/MCP changes.

Open objections:

- replay races, expiry races, restart, timeout and key-rotation tests await persistence;
- logs/errors/metrics/cache/database exfiltration scanning is incomplete;
- no live protocol or PostgreSQL evidence exists.

## CEO review

Score: 1/3

Accepted evidence:

- the foundation corrects the fork's critical plaintext-snapshot flaw without coupling the control plane to AgentWaker;
- the envelope can support future adapters and regulated deployment requirements.

Open objections:

- there is no usable customer outcome yet;
- operational key management and support liability remain unresolved;
- no independent security review has occurred.

## Security, privacy and data-loss blockers

- Secret transfer remains unadvertised and unreachable.
- Do not persist an ephemeral private key without master-key encryption, key ID and authenticated context.
- Do not consume an envelope without atomic one-time replay protection and fail-closed audit.
- Do not place decrypted values in snapshots, plans, receipts, ordinary agent responses, logs or analytics.

Final decision: CONDITIONAL
