# Role-source production validation

Status: required before any production cohort; passing unit/static tests is not a substitute.

The repository targets PostgreSQL 17. Run every database command below only against a disposable PostgreSQL 17 validation database or an isolated staging clone. The handler suite creates and deletes the workspace slug `handler-tests`; never point it at a production database. Use a dedicated staging bucket and credentials limited to the `multica-role-source-validation/` prefix for the storage probe.

## Required environment

```bash
export DATABASE_URL='postgres://VALIDATION_USER:VALIDATION_PASSWORD@VALIDATION_HOST:5432/VALIDATION_DATABASE?sslmode=require'

export S3_BUCKET='VALIDATION_BUCKET'
export S3_REGION='VALIDATION_REGION'
export AWS_ACCESS_KEY_ID='VALIDATION_ACCESS_KEY'
export AWS_SECRET_ACCESS_KEY='VALIDATION_SECRET_KEY'
# For MinIO, OSS, COS or another S3-compatible staging service:
export AWS_ENDPOINT_URL='https://VALIDATION_ENDPOINT'
export S3_USE_PATH_STYLE='true'
```

Do not paste environment values into an evidence report. Record only the database engine/version, deployment topology, storage provider, region, test timestamp, commit and redacted run identifier.

## Gate R — signed remote origin and key lifecycle

Use a disposable public DNS name and the production TLS/CDN/object-storage path;
do not weaken the adapter to point at localhost. Publish one valid bundle and
artifact set using a staging hardware-backed or production-equivalent Ed25519
signer. Configure current and next public keys through managed daemon-config
CAS, wait for loaded evidence, and execute scan-only traffic from every target
daemon platform.

Inject: NXDOMAIN, DNS timeout, public-to-private DNS rebinding, IP literal,
redirect to another origin, proxy environment variables, invalid/expired TLS,
HTTP 4xx/5xx, slow headers/body, compression bomb, declared/streamed oversize,
manifest/tree tamper, wrong/unknown/retired key, changed artifact, origin outage,
old valid release replay and current-to-next key rotation.

Pass criteria:

- only direct, same-origin, default-port HTTPS to a public resolved address is
  dialled; proxy, redirect and private-address paths receive no request;
- deadlines and 8 MiB bounds release connections and scan capacity without
  leaking URL, key, signature, body or local configuration in logs/API/audit;
- one-byte manifest, commitment, signature or artifact changes fail closed and
  leave the last applied snapshot active;
- current+next trust overlap permits a no-downtime signer transition; removing
  current makes a replay signed only by that key fail;
- issuer, signing key ID, revision, tree digest and signature commitment survive
  snapshot backup/restore, while raw signatures/private keys do not enter the
  control plane;
- the release ledger/transparency policy detects an old but cryptographically
  valid revision; Multica does not claim built-in freshness or anti-replay;
- scan p95/p99, DNS/TLS latency, origin error rate and artifact throughput are
  recorded at the 100-concurrent-scan target before cohort approval.

## Gate A — real migration round trip

This applies every real role-source migration in a private schema, verifies the resulting table/index inventory and runs all down migrations back to zero:

```bash
go -C server test -count=1 -run '^TestRoleSourceMigrationsRoundTripInIsolatedSchema$' ./cmd/migrate
```

Then prove multiple server processes cannot race a pending or already-applied migration set:

```bash
go -C server test -count=1 -run '^TestRunMigrationsConcurrent(Pending|AlreadyApplied|MixedPoolStress)$' ./cmd/migrate
```

Pass criteria: no invalid concurrent index, duplicate migration, missing table/index, rollback residue or timeout.

## Gate B — loaded-config attestation transactions

```bash
go -C server test -count=1 -run '^TestRoleSourceRuntimeAttestation(PersistsDistinctRestartHistory|CannotReappearAfterRuntimeDelete)$|^TestRoleSourceRegistrationLockSerializesRuntimeDelete$' ./internal/handler
```

Repeat the transaction races under the Go race detector:

```bash
go -C server test -race -count=10 -run '^TestRoleSourceRuntimeAttestation(PersistsDistinctRestartHistory|CannotReappearAfterRuntimeDelete)$|^TestRoleSourceRegistrationLockSerializesRuntimeDelete$' ./internal/handler
```

Pass criteria:

- duplicate restart evidence produces one distinct history row and increments its observation count;
- changed config evidence produces a new history state and becomes current;
- a heartbeat blocked behind runtime deletion fails after the deletion commits and cannot recreate current or historical orphan rows;
- role-source registration's shared runtime lock conflicts with deletion's exclusive lock.

## Gate B2 — legal-hold authority and deletion races

Run the control-plane and handler tests against the disposable PostgreSQL 17
database, then repeat the workspace-delete race from two server replicas:

```bash
go -C server test -count=1 -run '^TestRoleSourceLegalHoldCreateReleaseAndAudit$|^TestDeleteWorkspace_ActiveRoleSourceLegalHoldIsHardFence$' ./internal/handler
go -C server test -race -count=10 -run '^TestDeleteWorkspace_ActiveRoleSourceLegalHoldIsHardFence$' ./internal/handler
```

In staging, hold the workspace deletion transaction after its workspace lock;
concurrently create a source-scoped hold. Repeat with hold creation first,
release racing deletion, a snapshot-scoped hold and a direct legacy cleanup
query. Pass criteria:

- exactly one lock order wins without deadlock or partial tenant teardown;
- an active hold leaves workspace, source, snapshots, artifacts and the hold
  intact, and direct active-hold deletion fails at the database layer;
- exact create/release retries produce one row and one audit event each, while
  changed retries fail closed;
- after an authorized release, teardown deletes the hold before its release
  record and completes without residue;
- API, logs and reports contain no raw idempotency key, case number, narrative,
  path, body or credential.

This gate validates the hold fence only. It does not authorize historical
snapshot pruning; that requires a separate retention-candidate race and restore
exercise after the retention worker exists.

## Gate B3 — historical-retention transaction and provenance races

```bash
go -C server test -count=1 -run '^TestRoleSourceRetentionPolicyHoldFenceAndPrune$' ./internal/handler
go -C server test -race -count=10 -run '^TestRoleSourceRetentionPolicyHoldFenceAndPrune$' ./internal/handler
```

Then run two server replicas with both retention and permanent artifact-GC gates
enabled against a disposable PostgreSQL 17 dataset containing current, recent,
old-successful, never-applied, held, task-pinned and shared-artifact snapshots.
At the snapshot-row lock, race each of: task enqueue, source/snapshot hold,
policy disable/extension, apply, secret transfer, new plan approval, rollback,
workspace delete, worker lease expiry and PostgreSQL failover.

Pass criteria:

- current, held, task-pinned, mapped, in-flight, recent/approved-plan and latest
  successful reserve snapshots never disappear;
- a concurrent task pin either commits first and defers prune, or fails because
  retained snapshot provenance no longer exists—no orphan pin is possible;
- policy changes are append-only, exact-retry idempotent and CAS-safe; a stricter
  age/reserve revision applies to already queued candidates;
- only one replica completes a candidate, and snapshot edges, snapshot content,
  orphan capability versions, candidate completion and hash-chain audit commit
  together;
- shared capability versions and artifact bodies remain while any retained
  snapshot references them; last-edge artifacts enter the independent purge
  ledger and are permanently removed only after its settle/tombstone protocol;
- migration up/down, trigger behavior, EXPLAIN plans, lock waits, WAL, deadlocks,
  backlog age and p50/p95/p99 remain within the recorded cohort budget;
- backup restore and point-in-time recovery restore active holds, policy
  revisions, candidates, snapshots, edges and task pins consistently.

## Gate C — configured S3-compatible backend

The opt-in probe writes a unique small object, reads back the exact bytes, permanently purges the current object plus every retained version/delete marker, verifies the version inventory is empty and requires a not-found read. Ordinary CI skips it. The test identity needs `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucketVersions` and `s3:DeleteObjectVersion` for the validation prefix. Object Lock or legal hold must cause a visible failure unless the approved retention policy explicitly owns that block.

```bash
MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 \
  go -C server test -count=1 -run '^TestS3StorageLiveRoleSourceRoundTrip$' ./internal/storage
```

Pass criteria: fixed-length streaming upload, byte-exact readback, zero retained versions/delete markers and verified current absence. Any transport/authentication error after purge is a failure, not proof of absence.

## Gate F — database/object-store disaster recovery

Follow [`disaster-recovery.md`](disaster-recovery.md) on a production-shaped
PostgreSQL 17 primary plus the candidate versioned S3-compatible store. Start a
backup while two replicas are scanning/applying and separately race retention,
workspace deletion and permanent purge. Kill the backup during object copy and
`pg_dump`, fail over the database primary, interrupt one artifact restore, omit
one object, change one byte, remove one current/previous secret key, break an
audit link and restore to a point immediately before/after a legal hold.

Pass criteria:

- the exclusive/shared lock places every destructive operation wholly before
  or after the exported snapshot without deadlock or lost purge obligation;
- incomplete output directories cannot be mistaken for successful backups and
  restore is byte-verified/idempotent;
- every injected omission/tamper/key/edge/chain fault returns non-zero with a
  bounded content-free finding; a valid restore passes every row and object;
- current snapshots, active holds, append-only policies, task pins, mappings,
  apply receipts, audit sequence and runtime attestations reproduce exactly;
- fresh daemon attestation, read-only scan and controlled no-op apply pass
  before traffic, retention or GC resumes;
- measured RPO/RTO, dump/archive sizes, lock wait p99, WAL/load, restore
  throughput and operator actions fit the approved 10,000-user cohort budget.

## Gate D — failover and restart exercise

Run against the actual staging topology, not a single local process:

1. Start two server replicas and one configured daemon on the candidate commit.
2. Restart the daemon twice without changing config. Verify one current attestation, one history state and the observation count increasing once per process start.
3. Stop the daemon and wait past the configured Redis TTL/database stale threshold. Verify current source status becomes runtime unavailable while the last attestation status remains loaded; restart and verify it returns online without rewriting history incorrectly.
4. Apply a changed config entry without restarting. Within two reload intervals,
   verify a new active local revision, repeated attestation until durable
   acknowledgement, one new history state, correct drift classification and no
   raw config ID/path in health, API or logs. An in-flight scan must complete on
   its captured old generation while the next scan uses the new generation.
5. Publish malformed JSON, change the file to public permissions, and remove
   it in separate exercises. Each must expose a bounded local `degraded` code,
   retain the last-known-good scanner and keep its revision. Restore the exact
   valid bytes and verify health recovers without a restart or duplicate
   history. Starting with no implicit default file must remain safely
   `unloaded`; creating it must enable the scanner live.
6. Force PostgreSQL primary failover while the daemon is retrying an unacknowledged attestation. Verify the daemon continues retrying, one durable state wins and the server acknowledges only after commit.
7. Hold runtime deletion open, start a first-attestation heartbeat, then commit deletion. Verify the heartbeat fails and no orphan evidence remains.
8. Queue and claim a scan plus secret transfer, then pause the source while
   daemon result reports race. Verify one lock order, no deadlock, terminal
   cancelled/failed work, cleared envelope/private-key ciphertext and one
   hash-chained `source_paused` event in the same commit.
9. Verify direct active-to-detached and detached-to-resumed transitions fail;
   pause then detach releases the old runtime for cleanup without deleting
   source history. Rebind to another workspace-owned runtime must remain paused
   and resume must fail until fresh matching destination attestation exists.
10. Restore the old primary only after fencing it from writes. Verify no duplicate history or split-brain current state appears.

Pass criteria: no lost accepted state, no acknowledgement before durability, no orphan rows, no cross-workspace identifiers, no unbounded retries and no duplicate current row.

## Gate E — capacity evidence

Use a production-shaped dataset and two server replicas. Measure, do not infer:

- 10,000 users' expected runtime/source cardinality and process restart burst;
- p50/p95/p99 attestation transaction latency during normal load and primary failover;
- database CPU, WAL bytes, lock wait time, deadlocks and connection-pool saturation;
- API p50/p95/p99 for source list plus 100-entry history;
- S3 upload/read/delete latency and deletion error rate;
- Prometheus series count for all role-source metrics.
- `multica_role_source_runtime_availability` changes by the expected count when
  one active source's daemon stops, emits only the two approved status series,
  and fires `MulticaRoleSourceRuntimeUnavailable` after the configured hold;
  paused/detached sources do not page.

Required initial SLOs for the engineering cohort:

- steady-state heartbeats carry no attestation payload after acknowledgement;
- p99 first/restart attestation persistence below 1 second outside failover;
- source list p99 below 500 ms for the cohort's largest workspace;
- zero deadlocks, orphan rows, cross-tenant fields or unbounded metric labels;
- every persistence failure emits `persist_failed` and the Helm alert fires in the staging alert pipeline.
- the runtime-unavailable alert identifies no tenant in Prometheus labels; the
  authorized settings view and [operator runbook](operator-runbook.md) provide
  the identification and recovery path.

## Evidence record

| Field | Required value |
| --- | --- |
| Commit | Full Git SHA |
| PostgreSQL | Version, topology and region |
| Object storage | Provider, region and version; no credentials |
| Dataset | Users, workspaces, runtimes, sources and history rows |
| Migration | Up/down result and elapsed time |
| Concurrency | Iterations, failures, deadlocks and max lock wait |
| Failover | Trigger time, recovery time and retry count |
| Capacity | p50/p95/p99, CPU, WAL, pool and S3 measurements |
| Security | Redaction and tenant-isolation checks |
| Decision | GO/NO-GO with all four review scores |

No production rollout is allowed with a missing row in this record, a review score below 3, or an unresolved security, data-loss, tenant-isolation, audit or rollback objection.
