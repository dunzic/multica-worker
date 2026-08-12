# RS-01 runtime loaded-configuration attestation review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for merge behind the existing role-source flags; NO-GO for broad production until live PostgreSQL migration/failover and cross-platform daemon restart exercises pass**

## Customer and product outcome

The control plane can now distinguish a file that was merely written from the configuration a daemon process actually loaded. Each role source reports one of seven explicit states: unattested, not loaded, config ID missing, adapter kind mismatch, adapter version drift, invalid evidence, or loaded and matching. Workspace members can inspect the current state, a redacted revision and the bounded history of distinct states without seeing local config IDs, paths, raw adapter configuration, allowed roots or keys.

## Architecture review — 2/3

- The protocol statement is source-neutral and commits to contract version, loaded state, a runtime-scoped commitment to the exact private-file revision, sorted runtime-scoped config-ID digests, kinds and adapter versions. A SHA-256 attestation ID detects body tampering.
- New daemons advertise support explicitly. Older daemons omit the capability and remain compatible; omission does not erase previously recorded evidence.
- The daemon sends evidence until the server durably stores and acknowledges the matching attestation ID, then suppresses it per runtime. Normal 15-second heartbeats therefore add no ongoing database write or payload at 10,000-user scale.
- Current evidence and distinct-state observations are written atomically in one SQL statement. The persistence transaction takes the shared workspace and runtime locks used by the no-foreign-key lifecycle contract before writing; runtime, profile, stale-runtime and workspace deletion paths explicitly remove both tables.
- A runtime referenced by any role source cannot be deleted by the user, profile cascade or stale-runtime collector. Registration takes the matching runtime lock, and legacy daemon-ID consolidation locks and atomically reassigns role sources to the replacement runtime before deleting old evidence, preventing an unattached source from being created by lifecycle maintenance.
- Bounded accepted/invalid/persistence-failure metrics add no tenant/request labels; a sustained persistence failure pages because the missing acknowledgement keeps the daemon retrying.
- Open objections: configuration hot reload is absent, the evidence is daemon self-attestation rather than hardware-backed identity, and multi-node failover has not run against a live database.

## Product review — 2/3

- The read-only settings surface turns protocol evidence into operational language and makes adapter drift visible before a scan fails.
- History records first/last observation and restart observation count, supporting incident reconstruction without retaining raw configuration.
- An unloaded statement is distinct from an old/unattested daemon, avoiding the misleading conclusion that silence means a valid empty configuration.
- Open objections: guided repair actions, daemon upgrade guidance, fleet aggregation and freshness/SLO alerts are not present.

## Test review — 2/3

- Protocol tests cover canonical identity, tamper detection, sorted/unique inputs, unloaded-state constraints and unknown nested fields.
- Daemon tests cover revision derivation from the exact securely opened body, adapter identity enumeration, per-runtime acknowledgement suppression, mismatched acknowledgement retry and HTTP wire shape.
- Handler tests cover every current-state classification and corrupt evidence fail-closed behavior. Static migration tests cover redacted schema, bounded arrays, distinct-state persistence, lock ordering and explicit teardown.
- UI tests cover current matching state, revision visibility, historical evidence and continued absence of mutation controls.
- Open objections: migrations 334–338, concurrent first heartbeat, duplicate restart, runtime deletion races and primary failover have not run on PostgreSQL because no local server is available.

## CEO review — 2/3

- This closes a high-cost enterprise support gap: operators can answer “what did the daemon actually load?” instead of comparing a server record with an unverified file.
- One generic proof contract serves AgentWaker and future adapters, so adapter growth does not multiply bespoke health systems.
- The acknowledgement handshake keeps steady-state cost near zero while preserving restart/config-change evidence.
- Open objections: prove reduced diagnosis time, mismatch frequency and successful self-recovery in pilot cohorts before assigning commercial ROI.

## Security and privacy decision

- Wire and persistence fields are bounded and strict. They contain only runtime-scoped content/config-ID digests and adapter identities; nested unknown fields are rejected. Raw IDs and a globally correlatable file revision are never sent, preventing workspaces on a shared daemon from receiving another workspace's local labels or common daemon fingerprint.
- The API never returns local paths, root names, raw JSON, digest keys or source content. The settings UI truncates content digests.
- The server validates the attestation ID before persistence and treats malformed stored source arrays as invalid evidence rather than matching them.
- This is operational attestation, not remote trust attestation. A compromised daemon can lie and remains inside the daemon trust boundary already authorized to scan and upload source content.

## Rollout decision

Merge behind `role_source_sync` plus `role_source_scan`. Use the status/history surface in an engineering cohort after applying migrations on a real PostgreSQL staging cluster. Broad rollout still requires live migration rollback, two-server duplicate-heartbeat/failover, daemon restart, runtime deletion, and stale-runtime cleanup evidence plus freshness alerts and a repair runbook.
