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

## Gate B4 — transactional apply-event outbox

First run the real generated-query lease state machine against disposable
PostgreSQL 17:

```bash
MULTICA_LIVE_ROLE_SOURCE_OUTBOX_TEST=1 \
  go -C server test -count=1 -run '^TestRoleSourceOutboxPostgresLeaseAndRetryStateMachine$' ./internal/rolesource
```

Then use two API replicas with the candidate Redis relay. Commit distinct
no-op/apply/rollback fixtures and inject process death after PostgreSQL commit,
before Redis publish, after Redis acceptance and before outbox acknowledgement.
Interrupt Redis before and after XADD, expire the owning lease, fail over the
PostgreSQL primary and restart both replicas with a backlog larger than one
worker batch.

Pass criteria:

- each committed receipt has exactly one outbox UUID in the same transaction;
- only one live lease owns an event and a stale token can neither acknowledge
  nor release it;
- every retry keeps the same event ID, and browser clients observe at most one
  effective invalidation despite local/relay/retry delivery;
- an event accepted by Redis but not acknowledged is retried after lease expiry
  without re-running apply or producing another outbox row;
- attempt 20 becomes a visible dead letter; no exhausted lease remains
  `publishing`, and no automated blind replay or deletion occurs;
- backlog, oldest age, dead letters and bounded outcomes alert as configured;
  seven-day published and 30-day dead cleanup runs in bounded batches only;
- one event per apply refreshes source/Agent/Skill/Autopilot state regardless of
  object count; measured Redis, DB, CPU and browser refetch load fits Gate E;
- an operator repairs dependencies and replays only the original stable event
  ID through an approved audited procedure; the apply request is never replayed
  to repair notification delivery.

## Gate B5 — controlled dead-letter replay

Apply migrations 369–375 to disposable PostgreSQL 17 and run the real replay
state machine:

```bash
MULTICA_LIVE_ROLE_SOURCE_REPLAY_TEST=1 \
  go -C server test -count=1 -run '^TestRoleSourceOutboxReplayPostgresStateMachine$' ./internal/rolesourcereplay
```

Then repeat the operator runbook in the production-shaped staging topology with
two named people and the candidate KMS/HSM Ed25519 keys. Kill the operator
process before commit, during commit response and immediately after the
database commit. Race two executions of one authorization, retry its exact
files after expiry, rotate one active key while retaining its public evidence,
cycle one event through all three generations, and attempt a fourth replay.
Back up and restore after each of generations zero through three.

Pass criteria:

- inspect and execute reject an altered outbox commitment, invalid apply
  receipt, missing/duplicate success audit or any broken link from audit
  sequence one through the matching event;
- the canonical authorization is valid for exactly 15 minutes and only two
  configured, distinct Ed25519 keys held by different operators can authorize
  it; unknown, aliased, duplicate or malformed key material fails closed;
- prepare and execute refuse symlinked, over-broad, oversized, replaced or
  trailing-content operator files; no private key enters the command, image,
  database, log or Helm values;
- exactly one serializable transaction consumes a generation, writes its
  immutable hash-chained receipt and requeues the original UUID with attempt
  zero; no event payload is accepted and apply is never invoked;
- an exact retry reconciles to the same receipt before or after authorization
  expiry, while a changed field, key ID or signature cannot reuse the consumed
  authorization ID;
- generation four is impossible, receipt update/delete is database-rejected,
  and settled cleanup cannot orphan a replay receipt; explicit workspace
  teardown removes receipt and event in the guarded order;
- DR export/restore includes outbox and replay rows and rejects broken receipt
  chains, generation/count mismatches, missing events or receipt commitment
  mismatch;
- the incident archive retains authorization bytes, both signatures, KMS/HSM
  approval evidence, historical public keys and returned receipts for the
  approved audit period; database SHA-256 commitments reproduce every retained
  artifact exactly;
- measured time to authorize, deliver and resolve fits the incident SLO, and
  two-replica Redis/process-death exercises from Gate B4 still deduplicate the
  stable UUID after replay.

## Gate B6 — atomic apply failure and commit ambiguity

Run the real apply transaction matrix against a disposable, fully migrated
PostgreSQL 17 database. The deterministic fault hook is private to the
same-package test: it has no exported setter, environment variable, server
route or production configuration binding.

```bash
MULTICA_LIVE_ROLE_SOURCE_APPLY_FAILURE_TEST=1 \
  go -C server test -count=1 \
  -run '^TestRoleSourceApplyFailureMatrixPostgres$' ./internal/rolesource
```

Pass criteria:

- failures immediately after transaction begin, apply start, before and after
  materialization, secret consumption, snapshot advance, receipt completion,
  success audit and outbox insertion leave no apply, audit or outbox row and do
  not advance the source snapshot;
- the independent failure ledger records one bounded stage/code and a SHA-256
  request-key commitment, never the raw key, raw database error or artifact;
- a role-create plan interrupted after materialization leaves neither the new
  Agent nor its provenance mapping, proving rollback of real domain writes
  rather than only a no-op transaction envelope;
- a database wrapper that commits successfully and returns
  `context.DeadlineExceeded` is reconciled to the exact succeeded receipt with
  one apply, one success audit and one outbox row;
- cancellation immediately after commit cannot cancel the bounded independent
  reconciliation read;
- simulating process loss after commit and constructing a new control plane
  allows the exact request to resolve to the same apply ID and receipt digest,
  without another materialization, audit event or outbox row;
- the batch mapping query used by the real role-create path executes through
  generated sqlc code on PostgreSQL; static compilation alone is insufficient.

This single-node gate proves transaction atomicity and commit ambiguity. It
does not prove behavior when PostgreSQL is completely unavailable or during a
primary failover; retain those as Gate D and deployment-topology blockers.

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

First run the strictly read-only evidence probe against the largest staged
workspace and a runtime with the cohort model's worst-case attestation history.
It pins every session to read-only mode, applies a five-second statement
timeout, runs concurrent source-list plus 100-entry history reads, summarizes
`EXPLAIN ANALYZE` without exporting tenant filters, and writes a new private
report. It never seeds or changes data:

The Helm chart exposes the same command as the default-off
`roleSource.capacityEvidence` one-shot Job. Supply the approved minima and a
unique DNS-safe run name plus a separate evidence PVC; do not reuse the uploads
or backup PVC.

```bash
/app/role_source_capacity \
  --workspace-id VALIDATION_WORKSPACE_UUID \
  --runtime-id VALIDATION_RUNTIME_UUID \
  --samples 500 --concurrency 32 \
  --minimum-users 10000 \
  --minimum-workspace-members APPROVED_LARGEST_WORKSPACE_MEMBERS \
  --minimum-workspace-runtimes APPROVED_LARGEST_WORKSPACE_RUNTIMES \
  --minimum-workspace-sources APPROVED_LARGEST_WORKSPACE_SOURCES \
  --minimum-attestation-history APPROVED_RUNTIME_HISTORY_ROWS \
  --report /private/evidence/role-source-capacity-read.json
```

The approved minima must come from the signed cohort model; use 100 history
rows when that model requires a 100-entry runtime history worst case.
`read_path_passed` proves only the database read slice and required index use.
It does not satisfy Gate E without the two-replica API, attestation-write burst,
S3, metrics and failover evidence below.

- 10,000 users' expected runtime/source cardinality and process restart burst;
- p50/p95/p99 attestation transaction latency during normal load and primary failover;
- database CPU, WAL bytes, lock wait time, deadlocks and connection-pool saturation;
- API p50/p95/p99 for source list plus 100-entry history;
- S3 upload/read/delete latency and deletion error rate;
- Prometheus series count for all role-source metrics.
- one default-off 1,000-role/10,000-skill apply fixture measuring name
  preflight, 10,000 association batch, capability batches, agent/skill writes,
  mappings, receipt and commit separately; record SQL parameter bytes, rows,
  transaction duration, lock wait, WAL, CPU/memory and rollback duration;
- repeat the large apply with existing disabled associations, exact and
  mismatched immutable capability versions, cross-tenant endpoints, duplicate
  source names, concurrent user agent/skill creation and concurrent same-title
  autopilot creation; every invalid or ambiguous case must roll back atomically;
- `multica_role_source_runtime_availability` changes by the expected count when
  one active source's daemon stops, emits only the two approved status series,
  and fires `MulticaRoleSourceRuntimeUnavailable` after the configured hold;
  paused/detached sources do not page.

Before a candidate-image Gate E run, execute the repository's default-off
single-node write baseline against an otherwise empty, fully migrated
PostgreSQL 17 database:

```bash
MULTICA_LIVE_ROLE_SOURCE_SCALE_TEST=1 \
DATABASE_URL='postgres://...' \
go -C server test -count=3 \
  -run '^TestRoleSourceProductionScaleApplyPostgres$' -v ./internal/rolesource
```

Every run must first persist exactly 1,000 Agents, 10,000 Skills, 10,000
bindings and 11,000 mappings, then apply an all-object v2 rename/version update
without changing those cardinalities. The update must preserve sampled
user-owned Agent permission/model/environment/MCP, Skill config and a disabled
Agent-to-Skill association. Each phase adds exactly one apply, success audit
and outbox event; a new control plane must return the same receipt on retry,
and fixture cleanup must
leave zero workspace/source/user/artifact residue. Preserve every
`scale_evidence` and `scale_update_evidence` line, including verified artifact
bytes, fixture/apply/retry duration, database growth, WAL, current and peak
heap, total allocation, receipt bytes and preservation booleans. This local baseline intentionally does not claim the Gate E
two-replica, object-storage, contention, failover, CPU or lock-wait SLO.

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
