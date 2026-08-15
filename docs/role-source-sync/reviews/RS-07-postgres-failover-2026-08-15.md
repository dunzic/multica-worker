# RS-07 PostgreSQL primary-failover evidence review

Feature: controlled channel-delivery ambiguity resolution

Date: 2026-08-15

Decision: **GO for the repeatable local PostgreSQL 17 physical-failover gate and
the two-connector controlled pilot; NO-GO for 10,000-user GA until the same
fault matrix passes in the candidate multi-AZ topology with real providers,
KMS/HSM, mixed traffic and delivered alerts.**

## Exercised topology

Each run creates and later removes a new isolated Docker network, two database
volumes and these processes:

- one PostgreSQL 17 primary and one physical standby built with
  `pg_basebackup`;
- synchronous streaming replication, verified as `sync` before the fault;
- HAProxy as the stable database endpoint, with the standby used only after the
  primary is unavailable;
- two complete `multica-backend:dev` containers sharing that database endpoint
  and one Redis 7 instance;
- one Go test process holding a generation-1, two-distinct-key Ed25519
  `confirmed_not_delivered` authorization.

The harness starts backend A through migration completion before starting
backend B. This matches the required single-migrator deployment order and
avoids treating concurrent schema migration as an application availability
test.

## Fault and assertions

The harness hard-kills the primary before releasing eight reconciliation
workers. It does not promote the standby until the test process has observed a
real database error. After promotion, every worker retries through the unchanged
HAProxy address and must return the same immutable generation-1 receipt.

The test and an independent SQL query jointly require:

- PostgreSQL major version 17 and `pg_is_in_recovery() = false` after promotion;
- final delivery state `retry_authorized`, reconciliation count 1 and next
  generation 2;
- exactly one `channel_delivery_reconciliation` row despite eight callers;
- identical outcome and receipt digest for all callers;
- both backend `/readyz` checks and Redis `PONG` after failover;
- zero delivery or reconciliation fixture rows after test cleanup;
- exact cleanup of every run-scoped container, volume, network and signal file.

## Repetition evidence

Three consecutive runs created independent topologies and passed:

| Run | Workers | Attempts | Transient errors | First success after first error | Orchestrated failover | Receipt rows | Fixture residue |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 8 | 17 | 9 | 5.125 s | 6 s | 1 | 0 |
| 2 | 8 | 17 | 9 | 5.118 s | 6 s | 1 | 0 |
| 3 | 8 | 17 | 9 | 5.129 s | 5 s | 1 | 0 |

The transient errors include lost primary connections and serializable
concurrency conflicts during recovery. They are deliberately retried; no
automatic provider send occurs in this reconciliation transaction.

An earlier post-static-check rehearsal exposed that `pg_isready` alone could
accept the official image's temporary initialization postmaster immediately
before it shut down. That failed rehearsal is not counted above. The harness now
requires PID 1 to be the final `postgres` process and executes `SELECT 1` in the
target database before continuing; the three recorded runs all use this
corrected readiness contract.

## Four-perspective review

### Architecture expert

Score: **3/3 for the local RS-07 failover contract; 2/3 for the target topology**

The stable endpoint, synchronous WAL boundary, immutable authorization receipt
and serializable idempotency contract converge across a real postmaster loss.
The gate also proves that two backend replicas remain ready after promotion.
It does not prove managed-service fencing, multi-AZ control-plane behavior,
split-brain prevention or real-provider acceptance boundaries.

### Product expert

Score: **2/3**

The result reduces a material controlled-pilot risk: an approved retry remains
auditable and singular through a database-primary outage. Product GA still
needs an operator drill with real Slack/DingTalk evidence exports, named
ownership, alert delivery and a measured customer-facing recovery objective.

### Test expert

Score: **3/3 for this repeatable local gate; 2/3 for production evidence**

The fault is a hard container kill, promotion is withheld until a client error
is observed, state is checked from both the production API path and independent
SQL, and three fresh topologies leave no fixture or Docker residue. Remaining
coverage requires candidate-image/provider kill points, managed PostgreSQL
failover, KMS/HSM rotation/revocation, mixed-load SLOs and the complete green CI
service matrix.

### CEO

Score: **2/3 for production launch; 3/3 for continuing the controlled pilot**

The local result is strong enough to fund the external exercise and removes
the objection that reconciliation idempotency exists only on a single primary.
It is not evidence for a 10,000-user SLA, provider-level exactly once or
multi-AZ disaster recovery. Broad launch remains NO-GO.

## Reproduction

From the repository root, with Docker available and the current backend image
built:

```bash
DOCKER_BIN=/usr/local/bin/docker \
  scripts/validation/rs07-postgres-failover.sh
```

The script generates per-run database, replication and JWT secrets and does not
print them. On failure it emits bounded container status and log tails, then
removes only resources carrying that run's `multica-rs07-failover-<pid>` prefix.

## Remaining production blockers

- repeat request-write, provider-acceptance and receipt-commit kill points with
  two candidate backend replicas and real Slack/DingTalk sandboxes;
- run managed multi-AZ PostgreSQL failover and prove old-primary fencing,
  connection-pool recovery, alert delivery and the approved RTO/RPO;
- use production KMS/HSM keys and rehearse rotation, revocation and audit export;
- run the signed 10,000-user mixed workload while recording provider throttling,
  pool saturation, CPU, WAL, locks and p50/p95/p99;
- complete Gate F restore and provider inventory/accounting reconciliation.
