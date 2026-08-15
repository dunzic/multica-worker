# RS-06 local signed backup/restore drill review

Feature: versioning, provenance, retention and disaster recovery

Date: 2026-08-15

Decision: **GO for the repeatable local PostgreSQL 17 signed restore baseline;
NO-GO for production Gate F until the same matrix passes against the candidate
multi-AZ database, versioned object store and production KMS/HSM with measured
RPO/RTO and named operator approval.**

## Exercised recovery chain

Each run creates and removes a new isolated Docker network, PostgreSQL 17
volume, source database, restore database and private work directory. It then:

1. applies every migration to the source database;
2. creates one workspace, one immutable content-addressed artifact ledger row,
   its matching healthy integrity row and the exact 38-byte object body;
3. generates an Ed25519 signing key pair, keeping private and public material in
   separate environment contracts for backup and verification;
4. runs the packaged `role_source_dr backup`, which exports one PostgreSQL
   snapshot and writes a signed manifest, custom dump and deterministic tar;
5. restores the dump into a newly created database and the object into a newly
   created storage directory;
6. runs artifact restoration twice to prove idempotency, then runs the full
   signed verifier over all 25 role-source tables and the exact object bytes;
7. independently checks the restored digest and PostgreSQL ledger state;
8. proves that a failed `pg_dump` leaves `INCOMPLETE` and no manifest;
9. injects changed archive bytes, a missing object, changed object bytes and a
   changed restored database row, requiring a non-zero command plus the exact
   redacted finding class for each;
10. restores the valid state and requires a final passing verification.

The script uses the packaged backend image, not a test double. Database and
signing passwords are generated per run and are not printed. Cleanup is limited
to the run-scoped `multica-rs06-dr-<pid>` resources and its `mktemp` directory.

## Defects found and closed by the drill

The first real execution found that `role_source_dr` discarded `pg_dump`'s
captured output and exposed only `exit status 1`. The command now includes a
trimmed, 2 KiB maximum diagnostic when the subprocess fails.

That diagnostic then exposed a second issue: `pg_dump` tried to resolve the
container's arbitrary numeric UID as a local user even though `DATABASE_URL`
already contained a database identity. The command now:

- parses the authoritative URL;
- passes the database username explicitly;
- passes a password-free PostgreSQL URL in the process arguments;
- removes pgx-only `pool_*` query settings before invoking libpq; and
- supplies the password only through a deduplicated `PGPASSWORD` environment
  entry.

Unit tests require the username, snapshot and output file arguments, preserve
`sslmode`, reject password or pool-setting leakage into process arguments and
prove stale `PGPASSWORD` replacement. The packaged command then completed the
real backup under host UID 501, which has no `/etc/passwd` entry in the image.

Two additional failures were confined to the new harness: `pg_restore` needed
the same password-free URL/`PGPASSWORD` split, and object fault injection had
to occur inside the Docker filesystem view rather than through a desktop bind-
mount cache. Neither is counted as a passing production feature until the
corrected end-to-end runs below.

## Three-run evidence

Three consecutive corrected runs created independent databases and bundles:

| Measure | Run 1 | Run 2 | Run 3 |
| --- | ---: | ---: | ---: |
| Role-source tables verified | 25 | 25 | 25 |
| Artifact ledger/body count | 1 / 1 | 1 / 1 | 1 / 1 |
| Database dump | 429,441 B | 429,509 B | 429,463 B |
| Artifact archive | 3,072 B | 3,072 B | 3,072 B |
| Signed manifest | 5,263 B | 5,263 B | 5,263 B |
| Total bundle | 437,776 B | 437,844 B | 437,798 B |
| Coarse backup wall time | <1 s | <1 s | 1 s |
| Restore + first full verify | 2 s | 2 s | 2 s |
| Complete gate including faults | 9 s | 10 s | 9 s |

Every run reported idempotent restore and refused all four corruption classes.
A final post-adjustment smoke run also passed with a 437,806-byte bundle and a
10-second complete gate.

The same packaged backend was then rebuilt at commit
`6fa6c7235bdefea937c70b573fd6e6fddf124a95` and installed into the standard
self-host Compose environment. Independent inspection of the running binary
found that exact embedded commit (rather than the previous image), migration
398 remained applied with `idx_channel_delivery_retry_publish_due` present,
and `/health`, `/readyz` and the existing frontend `/login` each returned 200.
This proves local packaging and deployment continuity only; it does not turn
the single-node self-host environment into candidate production evidence.

These are coarse local timings for one 38-byte object. They establish behavior
and packaging only; they are not capacity evidence, an RTO commitment or an RPO
measurement. PostgreSQL's exported snapshot provides the local consistency
boundary, but no customer write stream or point-in-time recovery was measured.

## Four-perspective review

### Architecture expert

Score: **3/3 for the local recovery contract; 2/3 for target topology**

The database snapshot, artifact inventory, manifest signature and verifier now
survive an actual dump/restore boundary in packaged binaries. Passwords do not
enter subprocess arguments, arbitrary runtime UIDs work, and changed state
fails closed. Still open are managed-primary fencing, object version/delete-
marker restoration, KMS-backed signing, secret-key escrow and concurrent
retention/GC behavior in the candidate topology.

### Product expert

Score: **2/3**

The operator workflow now has executable evidence rather than documentation
alone, and its failures are diagnosable without exposing backup contents. A
production offer still needs named ownership, scheduled drill policy,
retention/escrow terms, customer recovery communication and approved RPO/RTO.

### Test expert

Score: **3/3 for the local gate; 2/3 for production evidence**

The test crosses real process, database dump, database restore and filesystem
boundaries, validates a non-empty artifact inventory and uses exact negative
findings. It also records the failures discovered while making the gate real.
Missing are process kills during object copy and restore, primary failover,
large/versioned object inventories, key loss/rotation, concurrent mutations and
candidate-environment alert evidence.

### CEO

Score: **2/3 for production launch; 3/3 for funding the candidate Gate F drill**

This closes the risk that the DR design only worked in unit tests and finds two
packaging defects before a customer drill. It does not price recovery labor or
prove an enterprise SLA. Production and destructive-worker rollout remain
NO-GO.

## Reproduction

Build the intended backend image, then run from the repository root:

```bash
MULTICA_RS06_BACKEND_IMAGE='candidate-backend-image' \
DOCKER_BIN=/usr/local/bin/docker \
  scripts/validation/rs06-dr-restore.sh
```

## Remaining production blockers

- repeat with the candidate managed PostgreSQL topology and force primary
  failover before, during and after `pg_dump` snapshot import;
- use the candidate versioned object store, inject partial copy, response loss,
  restore interruption and object-version/delete-marker faults;
- use production KMS/HSM sign/verify and secret-transfer current/previous key
  escrow, then remove and rotate each required key deliberately;
- run realistic inventory and concurrent scan/apply/retention/delete traffic,
  measuring lock waits, WAL, bundle size, RPO and RTO;
- restore immediately before and after a legal hold, verify provider inventory
  and billing reconciliation, deliver alerts and obtain named SRE, security,
  product and CEO sign-off.
