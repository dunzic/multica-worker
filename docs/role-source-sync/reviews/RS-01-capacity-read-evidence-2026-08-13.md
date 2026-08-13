# RS-01 capacity read-evidence review — 2026-08-13

Feature: production-shaped source-list and loaded-attestation read evidence
Gate: merge / pre-production
Final decision: CONDITIONAL; merge the probe, but do not treat its result as the
10,000-user production gate.

| Perspective | Score | Decision and open objections |
| --- | ---: | --- |
| Architecture expert | 2/3 | The probe uses the exact sqlc source/attestation/runtime reads, forces PostgreSQL sessions read-only, bounds query time/concurrency/samples, checks PostgreSQL 17 and summarizes index use without exporting predicates. It does not model Redis liveness, JSON response encoding, TLS, server admission or two-replica behavior. |
| Product expert | 2/3 | A single private JSON report makes the largest-workspace read SLO and the 10,000-user dataset assertion reviewable. Operators must still supply an approved cohort shape; unset workspace/history minima cannot become an implicit capacity claim. |
| Test expert | 2/3 | Pure tests cover flags, nearest-rank percentiles, redacted plan summaries, private non-overwriting reports and source-level mutation rejection; race, vet and cross-build pass. A real PostgreSQL 17 candidate run is deliberately still open. |
| CEO | 2/3 | The probe converts one important scale claim into repeatable evidence at low operational risk. It does not yet establish end-to-end cost, failover behavior, write-burst capacity, S3 throughput or support staffing. |

Security and integrity conditions:

- the tool refuses an implicit database, uses `default_transaction_read_only`, a
  five-second statement timeout and only SELECT/EXPLAIN ANALYZE read paths;
- workspace/runtime identifiers are input-only and never written to the report;
  execution-plan predicates, relation names and row contents are discarded;
- reports are new mode-0600 files, include the full build commit, and fail when
  that commit was not embedded;
- dataset minima are explicit claims supplied by the validation owner. The
  default only enforces 10,000 global users plus one current attestation; Gate E
  commands must pass the approved workspace and runtime-history cardinalities.

Rollout decision: include the binary in the candidate image. Keep the feature
cohort NO-GO until this probe passes on the candidate PostgreSQL 17 dataset and
the remaining write/API/S3/metrics/failover Gate E evidence is complete.
