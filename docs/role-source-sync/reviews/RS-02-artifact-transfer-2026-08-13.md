# RS-02 content-addressed artifact transfer review

Feature: Bounded scan and contract validation

Gate: design and implementation evidence for artifact transfer

Date: 2026-08-13

Decision: CONDITIONAL — merge behind disabled sync/scan flags; no production rollout

## Delivered boundary

The normalized snapshot continues to contain only artifact references. After scanning, the selected daemon reopens each missing file through the trusted adapter and bounded filesystem, verifies its exact size and SHA-256 against the snapshot, and streams it to a daemon-authenticated endpoint while the scan lease is active. The server spools no more than 8 MiB, recomputes the digest before object storage, writes to a deterministic tenant-scoped key, and then records immutable readiness. Snapshot persistence fails closed if any referenced digest/size lacks readiness.

## Architecture expert review

Score: 2/3

Accepted evidence:

- source-specific filesystem authority remains in the daemon adapter;
- the optional generic artifact opener preserves the source-neutral registry contract;
- source mutation between scan and upload is detected by identity/mtime/size and final digest checks;
- batch preflight avoids retransmitting workspace-local duplicate content;
- upload authorization is bound to workspace, source, request, runtime, lease token and expiry;
- object keys are deterministic by canonical workspace UUID and digest, so exact concurrent writes are content-identical;
- database readiness is immutable and tenant scoped; a storage success without a database row is never accepted by snapshot/apply paths;
- workspace teardown explicitly removes artifact ledger rows without foreign keys.

Open objections:

- object existence and digest are not yet re-read from storage before apply;
- crash-created unreferenced deterministic objects need retention/GC and reconciliation;
- the initial 8 MiB, 16-concurrent-spool transport does not meet future large/binary package needs;
- live PostgreSQL lease races and concurrent duplicate insertion remain unproved locally.

## Product expert review

Score: 1/3

Accepted evidence:

- an accepted preview now has retrievable bodies rather than unusable path metadata;
- duplicate content is skipped transparently;
- source changes fail the scan without changing the current worker.

Open objections:

- no UI reports upload progress, missing content or retry guidance;
- operators cannot yet inspect storage health or repair an unavailable body;
- accepted artifacts still cannot be materialized into runnable workers.

## Test expert review

Score: 2/3

Accepted evidence:

- canonical reference collection deduplicates digests and rejects size conflicts;
- AgentWaker artifact reopen succeeds for unchanged bytes and rejects post-scan mutation;
- client wire tests cover lease header, exact content length, batch response and PUT body;
- focused role-source, adapter, handler, daemon and race tests pass;
- migration policy and sqlc generation cover the new table and isolated concurrent unique index.

Open objections:

- server upload hash/oversize/lease-loss tests still need an authenticated handler fixture with storage failure injection;
- live PostgreSQL migration, duplicate upload and expired-lease races remain skipped because no local service is running;
- object-store timeout, partial-write, corruption and recovery tests are not complete.

## CEO review

Score: 2/3

Accepted evidence:

- content-addressed deduplication bounds repeated fleet transfer cost;
- digest-bound bodies are a reusable prerequisite for catalog, Git and signed-archive sources;
- fail-closed readiness materially reduces deployment and compliance risk.

Open objections:

- storage/egress cost, cache hit rate and support burden are not measured;
- the 10 GiB/source and 100-concurrent-scan targets need a direct object-storage upload path and load evidence;
- no end-to-end customer outcome exists until atomic apply is complete.

## Security, privacy and data-loss blockers

- Apply must re-read and hash every consumed object or rely on storage-level immutable checksum evidence before committing domain writes.
- Artifact member/download APIs remain absent; ordinary users cannot use this transport to exfiltrate source files.
- Secret-bearing and binary files remain excluded by AgentWaker scanning; RS-05 is a separate encrypted one-time channel.
- Production rollout remains blocked on storage reconciliation, failure injection and live database evidence.

Final decision: CONDITIONAL
