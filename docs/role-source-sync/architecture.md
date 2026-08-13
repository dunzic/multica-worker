# Pluggable Role Sources and Auditable Sync

Status: accepted for implementation

Feature flag: `role_source_sync` (server-side until the first end-to-end slice is ready)

Initial adapters: `agentwaker_directory`, followed by the source-neutral `multica_manifest_directory` conformance adapter
Target: production use by organisations with an aggregate user population of at least 10,000

## Product outcome

A workspace administrator can register a role source, scan it without changing the workspace, review an evidence-backed plan, apply the plan atomically, and later compare or roll back versions. The source remains authoritative for source-managed fields; workspace-managed fields remain under human control.

“Pluggable” means the control plane and normalized manifest do not depend on AgentWaker directory layout. AgentWaker is the first trusted adapter. Git repositories, signed archives, registries, and managed role catalogs can be added through the same adapter contract without adding source-specific tables or application flows.

“Auditable” means every scan, plan, approval, apply, rollback, secret mutation, and external delivery has an immutable actor, input digest, output digest, timestamp, result, and receipt. An operator can answer who changed which role, from which source version, with which review decision, and what was actually materialized.

## Non-negotiable invariants

1. Scan is read-only. It never mutates agents, skills, capabilities, automations, MCP configuration, or secrets.
2. A snapshot contains no plaintext secret, source credential, or unrestricted absolute path.
3. Apply accepts a persisted plan digest, not a mutable live source. The source is rescanned to create a new snapshot when content changes.
4. Apply is serialized per source and idempotent by `(source_id, snapshot_digest, plan_digest, request_key)`.
5. Source identity, not display name, owns mappings. Same-name unmanaged objects are conflicts unless an administrator records an explicit adoption decision.
6. One database transaction applies all workspace mutations and appends the apply audit receipt. A failed audit write fails the apply.
7. No-change plans perform no domain writes and create no new capability versions.
8. Required dependency failures block only affected roles and leave the last-known-good version active.
9. User-managed bindings, fields, secrets, and lifecycle state survive synchronization unless an explicit policy delegates them to the source.
10. Runtime claims pin exact skill, capability, configuration, and adapter digests so later synchronization cannot alter running work.
11. A persisted config file is not considered loaded evidence. Daemons attest only runtime-scoped config-ID digests and adapter identities actually loaded, retry until durable acknowledgement, and never send raw IDs, paths, raw configuration or keys.
11. Imported scripts and adapters are data during scan and apply; they are never executed as validation.
12. Database changes follow repository rules: no foreign keys or cascades, and every index is built concurrently in its own migration.

## Architecture

```text
Administrator / CLI
        |
        | register, scan, approve plan, apply, rollback
        v
Multica control plane
  source registry -> immutable snapshot -> deterministic plan
        |                                      |
        | queued scan                          | atomic apply + audit receipt
        v                                      v
Selected daemon                         Workspace materialization
  trusted adapter registry              agents / skills / capabilities /
  path and source boundary              automations / MCP / secret refs
        |
        | content digests + bounded blobs; no plaintext secret in snapshot
        v
Content-addressed artifact store + one-time secret transfer channel
```

### Trusted adapter model

The first release uses compile-time registered Go adapters. It does not load arbitrary shared libraries or execute source-provided code. Each adapter declares:

- stable `kind`, adapter version, and normalized contract version;
- supported source operations and limits;
- whether it can resolve secrets, binary artifacts, change hints, and provenance;
- configuration validation and redaction behavior;
- a deterministic scan implementation that returns the normalized manifest.

The second compile-time adapter consumes the normalized Multica manifest contract directly from a bounded directory. It proves the registry, daemon work protocol, snapshots and artifact transport are not coupled to AgentWaker parsing. An out-of-process adapter protocol can be added later, but only with process isolation, signed packages, resource quotas, and an authenticated protocol. Dynamic code loading is not required to call the first release “pluggable.”

### Normalized manifest

Every adapter emits the same source-neutral model:

- roles with stable IDs, lifecycle intent, instructions artifact, profile artifact, runtime hints, and source fields;
- role-owned skills and supporting artifact digests;
- shared capabilities with versions, profiles, permission envelopes, schemas, and artifacts;
- role-to-capability bindings with version constraints and fallbacks;
- environment declarations and non-reversible value-change digests, never values;
- MCP declarations with secret references, not expanded credentials;
- source-managed automations;
- diagnostics, excluded objects, and source provenance.

The control plane validates and canonicalizes this model before it computes the snapshot digest. Adapter-specific fields are allowed only inside a bounded, namespaced extension object and cannot drive generic authorization.

### Persistence model

The final schema keeps distinct responsibilities:

- `role_source`: tenant-scoped source configuration, adapter kind/version, owning runtime, policy, and state.
- `role_source_scan_request`: durable queued/leased daemon scan work and its terminal result.
- `role_source_snapshot`: immutable normalized manifest metadata and content references.
- `role_source_artifact`: tenant-scoped immutable readiness ledger for verified bodies stored under deterministic content keys outside PostgreSQL.
- `role_source_plan`: immutable from/to diff, decisions, blockers, and plan digest.
- `role_source_plan_approval`: append-only approval/rejection decisions over an immutable plan.
- `role_source_apply`: one idempotent apply or rollback attempt with actor, request key, outcome, and receipt digest.
- `role_source_object_mapping`: stable source object ID to Multica object ID, object kind, ownership mask, and last applied digest.
- `role_source_audit_event`: append-only, secret-free lifecycle events and receipts.
- `role_source_secret_binding`: metadata and encrypted value reference; values are stored outside snapshots and ordinary APIs.
- shared capability identity, immutable versions, content-addressed artifacts, and runtime claim pins.

Relationships are enforced in application transactions and teardown code, not by database foreign keys.

The control plane never stores raw adapter configuration. It stores an opaque
daemon-owned configuration ID and a typed, bounded safe summary. The daemon is
the only component that resolves that ID to a local path or credential.

Local adapter authority is managed as a complete desired-state document. The
CLI accepts only a secret-free document from private file or bounded stdin,
requires the exact current revision (or `absent` for creation), validates every
adapter before publication, and atomically replaces a `0600` profile-local
file under a non-writable canonical directory. A cross-process sibling lock
prevents two managers from both winning the same revision. The manager, not the
operator document, generates and preserves the AgentWaker evidence key; key
rotation is a separate confirmed action that forces rescan/review. Recovery
summary exposes a stable validation code and revision even when a hand-edited
file is malformed, but never raw config, absolute paths or key material.

### Scan and secret boundary

The daemon adapter may read only paths allowed by the source configuration and adapter policy. It must reject symlinks, hard-link surprises where detectable, traversal, device files, sockets, forbidden roots, file-count/size overruns, and content that changes while being read.

Scanning secrets produces only declaration metadata and keyed change digests. Apply uses a separate, short-lived secret transfer request bound to workspace, source, snapshot, role, nonce, expiry, and target server key. The resulting encrypted values are written through the same transaction and fail-closed audit path as other mutations. Snapshot, plan, diagnostics, logs, analytics, events, caches, and receipts never contain plaintext values.

### Daemon scan protocol

The server exposes only adapter descriptors; it never constructs a filesystem-capable adapter. An administrator queues a scan through a workspace-admin API, and the control plane nudges the source's owning runtime through the existing pending-work channel. A daemon heartbeat separately declares `supports_role_source_scan` and `poll_role_source_scan`. Capability negotiation does not query PostgreSQL; a claim occurs only on an explicit poll. This prevents every 15-second fleet heartbeat from becoming an empty database claim while retaining a slower daemon recovery poll for missed hints.

One heartbeat can lease at most one scan. The command contains source/workspace identity, adapter kind/version, an opaque daemon-local configuration ID, lease token and expiry—never an absolute path, credential or snapshot body. A configured daemon allows two concurrent scans, renews a 15-minute lease before long scans expire, and performs at most one recovery poll per runtime every five minutes. The daemon reports either one validated secret-free snapshot or one stable error code. Terminal reports are idempotent when runtime, lease, digest and outcome match; a stale, foreign or conflicting retry is rejected. Feature rollout requires both `role_source_sync` and `role_source_scan`; `role_source_apply` remains independently disabled.

After normalization, the daemon sends artifact references in batches of at most 1,000 and uploads only missing digests while the same scan lease remains active. The initial AgentWaker transport accepts at most 8 MiB per artifact. Each server process admits at most 16 simultaneous spools, writes each body to a private temporary file, recomputes exact size and SHA-256 before object storage, and uses `role-source-artifacts/<workspace>/<digest>` as the deterministic key. Only then may the immutable readiness row be inserted. A snapshot is rejected unless every referenced digest/size has a readiness row. A source file changed between scan and reopen fails with `source_changed`; paths and bodies never enter heartbeat JSON, audit payloads or member APIs.

Snapshot acceptance takes a shared database lock on every referenced readiness
row, inserts the immutable snapshot, and writes one explicit
`role_source_snapshot_artifact` reachability edge per canonical digest in the
same transaction. A collector must claim candidate readiness rows with an
exclusive `SKIP LOCKED` lock, so it cannot race through a snapshot publication:
the snapshot either publishes its edge first or observes the body unavailable.
Existing manifests are backfilled across every exact ArtifactRef location.
The ledger is the deletion authority boundary; the worker below may act only
after an artifact has no edge and has crossed the settle window.

When the independent default-off
`MULTICA_ROLE_SOURCE_ARTIFACT_GC_ENABLED` operator gate is enabled, the artifact
reconciler moves only readiness rows older than 24 hours with no
reachability edge into a durable, workspace-independent deletion intent in one
SQL statement. Workspace teardown writes the same intent before removing its
artifact rows, so deleting a tenant does not orphan object-storage bytes. Each
worker claim has a two-minute lease; storage calls have a 30-second deadline;
failures return to bounded backoff. Storage must implement permanent purge;
S3 removes the current object plus every retained version/delete marker, and
permission, Object Lock or version-count failure remains retryable rather than
being reported as erasure. A successful purge retains a widening
15-minute, 1-hour, 6-hour and 24-hour tombstone re-delete tail to reclaim a PUT
that materializes after its client abandoned the upload. Exact re-upload may
cancel a pending/tombstoned intent, but never an actively deleting one. Metrics
report queued objects, purges, failures, active backlog and tombstones.

Legal hold is independent retention authority, not a source lifecycle state.
Only a workspace owner may create, list or release a hold. A hold applies to an
entire source or one existing immutable snapshot and uses closed reason codes;
Multica accepts no case narrative or case number. An optional external-record
reference is stored only as a SHA-256 commitment, and callers should use an
approved high-entropy or HMAC-derived value rather than a predictable
identifier. Idempotency keys are stored only as SHA-256 digests.

Creation and release serialize on the workspace/source lock order and write
hash-chained audit events. Release is a separate immutable row, so it cannot
rewrite the authority that established the hold. Database triggers reject
updates to holds/releases and reject direct deletion of an active hold. An
active hold blocks workspace teardown before any tenant mutation. The future
historical-retention worker must query this authority inside its candidate
selection transaction: a source hold protects current and future snapshots; a
snapshot hold protects that exact digest. This establishes the hard fence and
owner surface only—it does not yet delete historical snapshots.

Every role-source mutation first takes a shared lock on the workspace row. Workspace teardown takes an exclusive lock on the same row, then deletes audit events, approvals, applies, plans, snapshots, scan requests and sources explicitly before runtimes and the workspace. This lock order prevents a concurrent scan report from inserting an orphan after the no-foreign-key cleanup sweep.

### Deterministic plan and atomic apply

The plan engine compares normalized object IDs and digests. It emits create, update, unchanged, conflict, detach-candidate, archive-candidate, blocked, and policy-decision actions. Every action records the affected fields, ownership, reason, and risk level.

An apply rechecks membership, adapter compatibility, snapshot state, plan digest, approval policy, source lock, object conflicts, and secret-transfer freshness before entering the transaction. Domain writes and the apply receipt commit together. Events are published only after commit and carry redacted summaries.

### Last-known-good and rollback

An applied snapshot is never mutated. New snapshots and plans may fail without changing runtime state. Rollback creates a new forward apply referencing a prior snapshot; it does not rewrite history or merely toggle an old row. Secret rollback is explicit: values are versioned through encrypted references or reported as non-restorable when policy forbids retention.

### Runtime task pins

A source-managed task captures immutable source, role, snapshot, normalized role-object, target-state and resolved capability evidence when it is first enqueued. System retries inherit the original pin. The pin is content-free: it contains identifiers, versions and digests, never prompts, environment values, MCP definitions, artifact bodies or local paths.

The materialized Agent remains the execution target. Until old encrypted runtime configuration can be reconstructed, drift is fail-closed. Apply advances even unchanged role mappings to the new snapshot and invalidates queued/deferred/not-yet-finalized dispatched tasks bearing the previous pin in the same transaction. Running tasks continue. Claim revalidates the mapping and a digest of all source-managed execution state before returning the typed pin to the daemon. Final claim commit locks the role mapping before the exact task dispatch generation; apply uses the same mapping-to-task lock order, so a concurrent version advance and old task-token commit cannot cross. A stale task must be explicitly re-enqueued against the new version; it is never executed under a misleading old provenance label.

## Feature slices

| ID | Feature | User value | Production completion |
| --- | --- | --- | --- |
| RS-01 | Adapter registry and source registration | Add new role ecosystems without product forks | Trusted adapter contract, source CRUD, tenant authorization, config redaction, feature flag, lifecycle state |
| RS-02 | Bounded scan and contract validation | Know what will be imported before any change | Daemon scan queue, canonical manifest, immutable snapshot, diagnostics, path/content limits, no-secret proof |
| RS-03 | Snapshot, diff, approval, atomic apply | Safe repeatable synchronization | Deterministic plan, conflict decisions, per-source lock, idempotency, one transaction, no-op proof |
| RS-04 | Role, skill, capability and automation materialization | Turn source definitions into runnable digital workers | Stable mappings, ownership masks, capability resolution, runtime digest pins, user-managed preservation |
| RS-05 | Secret and MCP synchronization | Configure non-coding workers without leaking credentials | Separate one-time transfer, encrypted storage, key metadata, audited reveal/update, runtime-only decryption |
| RS-06 | Versioning, provenance and rollback | Upgrade many roles with a safe recovery path | Immutable versions, affected-consumer preview, last-known-good, forward rollback, retention and GC |
| RS-07 | Delivery receipts and external readback | Prove the worker performed the intended action | Generic receipt schema, evidence attachments, connector readback, correlation IDs, retry/dedup semantics |

## Production scale and reliability gates

The following are release gates, not claims about current measured capacity:

- load profile includes at least 10,000 accounts, 2,000 active workspaces, 5,000 configured sources, 100 concurrent scans, and 50 concurrent applies across distinct sources;
- one source supports at least 1,000 roles, 10,000 skills/bindings, and 10 GiB of referenced artifacts without placing bodies in heartbeat metadata or loading deferred capability/supporting packages into atomic-apply memory;
- metadata APIs meet p95 500 ms and p99 1 s under the target profile; queue wait and scan duration have separate service-level indicators;
- applying one source is serialized, while unrelated sources scale horizontally;
- worker crash, daemon disconnect, server restart, duplicate delivery, stale plan, and transaction retry are covered by fault-injection tests;
- audit event loss is zero by design: mutation is fail-closed when its receipt cannot commit;
- snapshot and artifact retention, garbage collection, backup restore, and disaster-recovery exercises are documented and tested;
- security review covers tenant isolation, path traversal, symlinks, manifest bombs, HTML/script content, MCP commands, SSRF, secret replay, log redaction, and privilege escalation;
- the feature ships behind staged workspace cohorts with kill switches for scan scheduling and apply separately.

## Delivery order

1. Generic contract, registry, canonical validation, and review gates.
2. AgentWaker adapter using the reusable parser/hash fixtures from `code2rich/multica`, with the secret leak removed.
3. Source/snapshot persistence and daemon scan protocol.
4. Deterministic plan, conflict decisions, idempotent atomic apply, and audit receipt.
5. Materialization and runtime pins.
6. One-time encrypted secret synchronization and MCP binding.
7. Continuous detection, rollback, receipt/readback, scale tests, and staged production rollout.

No intermediate milestone may be described as the completed feature unless all seven slices and production gates are evidenced.
