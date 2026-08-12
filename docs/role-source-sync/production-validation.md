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

## Gate C — configured S3-compatible backend

The opt-in probe writes a unique small object, reads back the exact bytes, permanently purges the current object plus every retained version/delete marker, verifies the version inventory is empty and requires a not-found read. Ordinary CI skips it. The test identity needs `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucketVersions` and `s3:DeleteObjectVersion` for the validation prefix. Object Lock or legal hold must cause a visible failure unless the approved retention policy explicitly owns that block.

```bash
MULTICA_LIVE_ROLE_SOURCE_STORAGE_TEST=1 \
  go -C server test -count=1 -run '^TestS3StorageLiveRoleSourceRoundTrip$' ./internal/storage
```

Pass criteria: fixed-length streaming upload, byte-exact readback, zero retained versions/delete markers and verified current absence. Any transport/authentication error after purge is a failure, not proof of absence.

## Gate D — failover and restart exercise

Run against the actual staging topology, not a single local process:

1. Start two server replicas and one configured daemon on the candidate commit.
2. Restart the daemon twice without changing config. Verify one current attestation, one history state and the observation count increasing once per process start.
3. Stop the daemon and wait past the configured Redis TTL/database stale threshold. Verify current source status becomes runtime unavailable while the last attestation status remains loaded; restart and verify it returns online without rewriting history incorrectly.
4. Change one adapter version/config entry and restart. Verify a new state, visible drift classification and no raw config ID/path in API or logs.
5. Force PostgreSQL primary failover while the daemon is retrying an unacknowledged attestation. Verify the daemon continues retrying, one durable state wins and the server acknowledges only after commit.
6. Hold runtime deletion open, start a first-attestation heartbeat, then commit deletion. Verify the heartbeat fails and no orphan evidence remains.
7. Restore the old primary only after fencing it from writes. Verify no duplicate history or split-brain current state appears.

Pass criteria: no lost accepted state, no acknowledgement before durability, no orphan rows, no cross-workspace identifiers, no unbounded retries and no duplicate current row.

## Gate E — capacity evidence

Use a production-shaped dataset and two server replicas. Measure, do not infer:

- 10,000 users' expected runtime/source cardinality and process restart burst;
- p50/p95/p99 attestation transaction latency during normal load and primary failover;
- database CPU, WAL bytes, lock wait time, deadlocks and connection-pool saturation;
- API p50/p95/p99 for source list plus 100-entry history;
- S3 upload/read/delete latency and deletion error rate;
- Prometheus series count for all role-source metrics.

Required initial SLOs for the engineering cohort:

- steady-state heartbeats carry no attestation payload after acknowledgement;
- p99 first/restart attestation persistence below 1 second outside failover;
- source list p99 below 500 ms for the cohort's largest workspace;
- zero deadlocks, orphan rows, cross-tenant fields or unbounded metric labels;
- every persistence failure emits `persist_failed` and the Helm alert fires in the staging alert pipeline.

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
