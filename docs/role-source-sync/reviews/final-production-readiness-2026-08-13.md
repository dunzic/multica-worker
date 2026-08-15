# Pluggable role sources — final production-readiness review — 2026-08-13

Scope: RS-01 through RS-07 on `codex/pluggable-role-source`

Decision: **GO to merge with every rollout and destructive-operation gate
disabled; NO-GO for a customer cohort or production data mutation until the
recorded external gates below pass.**

This is not a code-placeholder decision. The branch contains source-neutral
interfaces, three adapters, bounded scanning, immutable evidence, safe apply,
materialization, secret/MCP transfer, rollback/provenance, retention/legal hold,
disaster recovery, capacity probes, delivery receipts and gated Helm operations.
The remaining objections require candidate-topology PostgreSQL, object storage,
cluster, remote-origin and runtime evidence that cannot be manufactured by unit
tests or a local single-primary container.

## Four-perspective decision

| Perspective | Score | Decision and remaining objection |
| --- | ---: | --- |
| Architecture expert | 2/3 | The server is source-neutral; AgentWaker remains an adapter; source lifecycle, generation-isolated reload, artifact reachability, apply receipts, task pins, holds, retention and DR share explicit lock and provenance contracts. A 3 requires live PostgreSQL lock/failover, versioned-object-store and cross-runtime evidence. |
| Product expert | 2/3 | Owners and members have a bounded read-only audit surface, drift/freshness evidence, retention preview and deliberate lifecycle operations. Mutation controls remain intentionally absent from broad UI; guided configuration, approval/apply/recovery and version-timeline UX still need controlled cohort validation. |
| Test expert | 2/3 | Role-source unit, race, fuzz, redaction, capacity, DR and Helm gates pass; repository frontend typecheck/tests and Go vet pass. The 2026-08-14 update adds every migration through 380, a 13-case atomic apply/commit-ambiguity matrix, a three-case two-control-plane concurrency gate, live adoption target/mapping and Agent/Skill name-claim races, a six-case Autopilot advisory-title-lock matrix, a real two-runtime unloaded-attestation incident recovery, local PostgreSQL 17.10 create/update runs for 1,000 roles plus 10,000 skills, a real AES-GCM secret/MCP lifecycle with after-consume rollback/retry/expiry/redaction, and a four-case legal-hold/task-pin versus prune matrix passing three consecutive runs with real `transactionid`/tuple waits and fail-closed loser states. 10,000-user database/S3/secret burst load, Kubernetes Jobs, real process kill, KMS rotation, exfiltration, failover and restore exercises remain mandatory. |
| CEO | 2/3 | The design creates a defensible multi-source control plane without binding the product to AgentWaker and keeps all customer/destructive exposure default-off. A production ROI/SLA decision would be unsupported until capacity, recovery time, support labor, failure rate and operator ownership are measured. |

No perspective can be raised to 3 by document review alone.

## Feature disposition

| Feature | Merge disposition | Production disposition |
| --- | --- | --- |
| RS-01 source registry and lifecycle | GO, disabled | NO-GO pending live migration, contention, restart and 10,000-user evidence |
| RS-02 scan and artifact contract | GO, disabled | NO-GO pending live remote-origin, versioned storage, GC/revival and readback evidence |
| RS-03 plan, approval and safe apply | GO for controlled default-off cohort after the 2026-08-14 live atomicity and two-control-plane concurrency matrices | NO-GO pending database-outage/failover, real process-kill and recorded operator recovery evidence |
| RS-04 materialization | GO for controlled default-off cohort after local 1,000-role/10,000-skill create/update evidence | NO-GO pending candidate-image two-replica/S3/contention/failover SLO and cross-runtime execution |
| RS-05 secret and MCP transfer | GO for a controlled default-off cohort after the local B11 lifecycle gate | NO-GO pending candidate-image KMS/HSM key rotation, process restart/lease reclaim, failover, burst-load and exfiltration exercises |
| RS-06 provenance, rollback and retention | GO, destructive workers disabled after the local four-case hold/pin/prune matrix | NO-GO pending policy-change and primary-failover races, retention RACI, versioned-object purge and recorded restore with RPO/RTO |
| RS-07 delivery receipts | GO as two-connector backend pilot | NO-GO pending ambiguous-send handling, operator retry, callbacks and production telemetry |

## Local evidence retained

- official Helm 3.20.2 archive matched its published SHA-256 sidecar; chart
  lint, default-off rendering, enabled capacity/backup Jobs and negative render
  cases passed;
- repository-pinned pnpm 10.28.2 completed all six typecheck tasks and all five
  Vitest workspace tasks; ESLint completed with zero errors and existing
  warnings;
- `go vet ./...` passed;
- `go test ./...` passed every package in the final 2026-08-14 run when local
  loopback binding was allowed; an earlier run retained evidence of the known
  `pkg/agent` parallel five-second timing instability, so candidate CI remains
  authoritative for that repository-wide release gate;
- all role-source packages, daemon, handlers, migrations, realtime, storage,
  DR and capacity commands passed in the full Go run;
- focused role-source race, fuzz, cross-build, migration-contract and Helm
  tests are recorded in `implementation-status.md` and the per-feature reviews.

The latest full run did not reproduce the historical `pkg/agent` instability;
that does not erase prior evidence, so it remains release-environment debt
rather than a role-source regression.

## Required external evidence before any production cohort

1. Gate A–D: PostgreSQL 17 isolated-schema migration up/down, tenant isolation,
   lock-order races, duplicate/idempotency/commit-timeout injection, primary
   failover and two-replica API behavior.
2. Gate E: candidate-image run with the approved 10,000-user cardinality model,
   two-replica 1,000-role/10,000-skill create/update/adoption contention,
   attestation restart burst, API/S3 percentiles, WAL/CPU/pool/lock metrics and
   alert-series cardinality. The local single-primary baseline is retained in
   `RS-04-production-scale-apply-2026-08-14.md` and is not the production SLO.
3. Gate F: versioned S3-compatible backup, interrupted transfer, isolated
   PostgreSQL restore, artifact rehydration, semantic verifier, key
   decryptability, RPO/RTO and signed evidence record.
4. Signed remote: controlled public DNS/TLS/CDN origin, key rotation/replay,
   outage and hardware-backed publisher exercises.
5. Runtime and product: Windows/macOS/Linux hot-reload/process-kill exercises,
   cross-runtime capability execution, browser validation and owner/admin
   operator rehearsal.
6. Governance: named SRE, security, product and data-retention owners; approved
   cohort minima, rollback authority, legal-hold RACI and incident support plan.

Use `production-validation.md` and `disaster-recovery.md` as the execution
runbooks. The Helm chart provides default-off one-shot capacity and backup Jobs;
restore/verify deliberately remains outside the ordinary release chart so it
cannot overwrite a target database.

## Release rule

Keep sync, scan, apply, artifact GC, retention, capacity and DR gates false.
Production remains NO-GO if any evidence-record row is missing, any of the four
scores is below 3, the `pkg/agent` parallel suite remains unstable in the release
environment, or any security, tenant-isolation, data-loss, audit, restore or
rollback objection is unresolved.
