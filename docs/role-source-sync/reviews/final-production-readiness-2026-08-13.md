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
| Architecture expert | 2/3 | The server is source-neutral; AgentWaker remains an adapter; source lifecycle, generation-isolated reload, artifact reachability, apply receipts, task pins, holds, retention and DR share explicit lock and provenance contracts. The local versioned-provider purge/late-write/Object-Lock/IAM gate and RS-07 synchronous physical-primary failover now pass; a 3 still requires candidate-provider receipt correlation, managed multi-AZ failover/fencing and cross-runtime evidence. |
| Product expert | 2/3 | Owners and members have a bounded read-only audit surface, drift/freshness evidence, referenced versus uniquely reclaimable retention previews and deliberate lifecycle operations; owners now see immutable logical-absence purge totals and the newest 50 verified receipts, while the UI explicitly refuses to call projection, observed bytes or logical absence realized/billed savings. Mutation controls remain intentionally absent from broad UI; receipt export/retention terms, guided configuration, approval/apply/recovery and version-timeline UX still need controlled cohort validation. |
| Test expert | 2/3 | Role-source unit, race, fuzz, redaction, capacity, DR and Helm gates pass; current focused Core/Views tests, focused ESLint and role-source Go vet pass. Current Core/Views package typecheck is still blocked by three pre-existing Chat Quick Actions errors in unmodified files, so it is not counted as a green release gate. The 2026-08-14/15 update adds role-source migrations through 386 plus channel-delivery migration 398, a 13-case atomic apply/commit-ambiguity matrix, a three-case two-control-plane concurrency gate, live adoption target/mapping and Agent/Skill name-claim races, a six-case Autopilot advisory-title-lock matrix, a real two-runtime unloaded-attestation incident recovery, local PostgreSQL 17.10 create/update runs for 1,000 roles plus 10,000 skills, a real AES-GCM secret/MCP lifecycle with after-consume rollback/retry/expiry/redaction, a six-case legal-hold/policy/task-pin versus prune matrix passing three consecutive runs with real `transactionid`/tuple waits and fail-closed loser states, a three-run shared-artifact projection/prune/audit matrix, a three-run exact-SQL PostgreSQL gate that queues 10,000 eligible snapshots with 10,000 artifacts/edges across 100 sources in 2.150–2.213 seconds end-to-end with 23.232–23.649 ms p95, a three-run real PostgreSQL five-pass purge-receipt state machine with immutable-row guards and post-round-trip digest verification, a real isolated versioned-provider suite passing exact multi-version purge, late-write convergence, legal-hold refusal and explicit version-delete-deny refusal under a prefix-scoped application identity, six-run real OS-process channel-delivery kill plus local 10,000-receipt audit/retry-backlog gates across two independent invocations, three fresh two-backend/shared-Redis/synchronous-standby RS-07 primary-failover runs, and three packaged RS-06 mid-object `SIGKILL`/resume runs over 64 MiB without canonical partial exposure or plaintext spool. 10,000-user database/S3/secret burst load, candidate-provider receipt correlation, Kubernetes Jobs, candidate-provider-bound process kill/response loss, KMS rotation, exfiltration, managed failover and restore exercises remain mandatory. |
| CEO | 2/3 | The design creates a defensible multi-source control plane without binding the product to AgentWaker and keeps all customer/destructive exposure default-off. A production ROI/SLA decision would be unsupported until capacity, recovery time, support labor, failure rate and operator ownership are measured. |

No perspective can be raised to 3 by document review alone.

2026-08-15 ambiguity-evidence addendum: migration 387 introduces receipt v2.
Provider mutation followed by response loss and expired `deleting` lease
reclaim are now durable ambiguity events; exact-key absence remains verifiable,
while version/delete-marker/observed-byte counts are labelled lower bounds.
Fresh PostgreSQL 17 migrations, three consecutive ordinary/response-loss/lease-
reclaim runs, v1/v2 digest checks, guarded downgrade, empty-ledger down/up,
transport-fault tests, API/Core/Views/DR parsing and the ambiguity alert pass.
This strengthens the Test and Architecture evidence within their existing 2/3
scores; it does not replace candidate-store process-kill, two-replica, failover,
restore, RACI or provider reconciliation evidence.

2026-08-15 RS-07 failover addendum: three independently created local
topologies with PostgreSQL 17 synchronous physical replication, HAProxy, shared
Redis and two backend containers passed hard primary loss. Promotion was
withheld until the reconciliation client observed an error; eight workers then
converged to one receipt in 5–6 seconds, both backends recovered ready and each
run left zero fixture residue. This closes the RS-07 local physical-failover
objection only. Managed multi-AZ fencing, real provider boundaries, KMS/HSM,
alerts and mixed 10,000-user traffic remain production blockers, so the overall
2/3 and NO-GO decision do not change.

2026-08-15 RS-06 local restore addendum: after the original three packaged
PostgreSQL 17 runs and smoke, three expanded fresh runs now sign and restore all
25 role-source tables plus two exact artifacts totaling 67,108,902 bytes. Each
run kills the packaged restore container at a different partial byte count,
keeps the canonical object absent, reclaims deterministic staging on retry and
passes idempotent restore, failed-backup `INCOMPLETE` and four corruption
refusals. Restoration now preflights the full signed archive before mutation,
streams without an anonymous plaintext spool, verifies exact provider readback
and reconciles a committed-but-response-lost upload. This closes the local
filesystem/process-interruption objection only, not candidate versioned-store
response loss, KMS, managed failover, concurrent load or RPO/RTO evidence;
Architecture/Test and overall production scores remain 2/3 and the NO-GO
decision does not change.

2026-08-15 RS-06 KMS-signing addendum: new backups now bind
`signature_scheme=ed25519-sha512-commitment-v2` and sign a compact,
domain-separated SHA-512 commitment, while manifests without the field retain
legacy-v1 verification. The AWS path requires an Ed25519/SIGN_VERIFY key,
independently pinned public key, resolved immutable ARN,
`ED25519_SHA_512`/`RAW` response metadata and successful local signature
verification; errors are bounded and no raw-key/unsigned fallback exists. Helm
KMS mode requires workload identity, renders no signing-private-key variable,
uses only `S3_ENDPOINT_URL` for custom storage and rejects global/service AWS
endpoint overrides before output or storage access; shared AWS files are
disabled and the official KMS resolver is reinstated as defense in depth.
Three fresh 64 MiB process-kill runs plus one assertion smoke passed the complete
local recovery/fault matrix with the packaged v2 scheme. This is code, render,
fake-provider and local-private-key compatibility evidence only. No real KMS,
CloudTrail/IAM deny, rotation/revocation or candidate Gate F run exists, so all
four scores and the production NO-GO remain unchanged.

## Feature disposition

| Feature | Merge disposition | Production disposition |
| --- | --- | --- |
| RS-01 source registry and lifecycle | GO, disabled | NO-GO pending live migration, contention, restart and 10,000-user evidence |
| RS-02 scan and artifact contract | GO, disabled after local versioned-provider purge/readback evidence | NO-GO pending live remote-origin, candidate-provider GC/revival/integrity races and two-replica/failover evidence |
| RS-03 plan, approval and safe apply | GO for controlled default-off cohort after the 2026-08-14 live atomicity and two-control-plane concurrency matrices | NO-GO pending database-outage/failover, real process-kill and recorded operator recovery evidence |
| RS-04 materialization | GO for controlled default-off cohort after local 1,000-role/10,000-skill create/update evidence | NO-GO pending candidate-image two-replica/S3/contention/failover SLO and cross-runtime execution |
| RS-05 secret and MCP transfer | GO for a controlled default-off cohort after the local B11 lifecycle gate | NO-GO pending candidate-image KMS/HSM key rotation, process restart/lease reclaim, failover, burst-load and exfiltration exercises |
| RS-06 provenance, rollback and retention | GO, destructive workers disabled after the local hold/policy/pin/prune matrix, 10,000-snapshot exact-SQL scale gate, v2 ambiguity-aware immutable purge receipts, real versioned-provider fail-closed suite and packaged local signed restore plus mid-object process-kill/resume baseline | NO-GO pending candidate-provider process-kill/response-loss and primary-failover race and scale repeat, candidate-provider receipt/inventory/accounting reconciliation, production KMS, retention RACI and recorded restore with approved RPO/RTO |
| RS-07 delivery receipts | GO as a two-connector controlled pilot with signed ambiguity resolution, a three-process kill chain, a local 10,000-receipt backlog gate and a three-run local physical-primary failover gate | NO-GO pending remaining connectors, attachments, real-provider candidate replicas, managed multi-AZ failover/fencing, KMS/HSM and approved 10,000-user mixed-load evidence |

## Local evidence retained

- official Helm 3.20.2 archive matched its published SHA-256 sidecar; chart
  lint, default-off rendering, enabled capacity/backup Jobs and negative render
  cases passed;
- repository-pinned pnpm 10.28.2 completed all six typecheck tasks and all five
  Vitest workspace tasks; ESLint completed with zero errors and existing
  warnings in the final 2026-08-14 run. The current 2026-08-15 package-level
  Core/Views typecheck attempt is not green: it stops on existing Chat Quick
  Actions updater-type errors in unmodified `packages/core/chat/mutations.ts`
  and `packages/core/realtime/use-realtime-sync.ts`; focused changed-file tests
  and ESLint remain green;
- `go vet ./...` and `go build ./...` passed again after the AWS KMS dependency
  addition in a standard Go 1.26 non-root environment with read-only source;
- `go test -count=1 ./...` passed every package, including `pkg/agent`, in that
  same standard non-root Go 1.26 environment with read-only source. Earlier
  Alpine attempts failed because that image lacked bash/git and a passwd/HOME
  entry for UID 501; those environment failures are not counted as code gates;
- all role-source packages, daemon, handlers, migrations, realtime, storage,
  DR and capacity commands passed in the full Go run;
- the DR protocol/command passed focused ordinary and race tests, targeted vet
  and build. Helm lint/render, default-off/private-key mode, KMS workload-
  identity mode, missing-identity refusal and no-private-key render assertions
  passed; both updated shell gates passed ShellCheck;
- focused role-source race, fuzz, cross-build, migration-contract and Helm
  tests are recorded in `implementation-status.md` and the per-feature reviews.
- the RS-07 PostgreSQL 17 gate passed six real OS-process kill chains across two
  independent `-count=3` invocations: replica A died after the retry-publication
  lease, B reclaimed and died after consuming the signed authorization/provider-
  send lease, and C froze the abandoned send as attempt-2 ambiguity while
  preserving generation 1 and requiring generation 2. The matching six-run
  10,000-receipt gate validated 200 bounded audit reads per run at 41.83–66.46
  ms p99 and eight-worker unique publish-lease claims in 108.91–124.91 ms with
  zero duplicates and zero cleanup residue. This is local single-primary
  database evidence, not two candidate backend images, provider acceptance,
  alert delivery or 10,000-user traffic.
- the standard self-host backend was rebuilt and recreated at exact embedded
  commit `f9bc54aa8`; it retained migration 398 and
  `idx_channel_delivery_retry_publish_due`, returned 200 from `/health` and
  `/readyz`, and the existing frontend `/login` returned 200. Independent
  binary inspection excluded the previously running `6fa6c7235` image. The
  KMS/endpoint-isolation commit `13f91ea42fb015e2bda9b0de88cb7ab7a22237e6`
  was then rebuilt and deployed with the same three 200 responses, exact binary
  commit match, migration 398/index continuity and no recent panic/fatal/error;
  the running container and tag both resolved to
  `sha256:d65d78cc0b67630457ae8a698c9465bc8af5efe20b561a4e97a346bcbdcf2179`.
  A separate isolated RS-07 gate created a synchronous PostgreSQL 17 physical
  standby, HAProxy, shared Redis and two same-image backends three times. It
  hard-killed the primary, observed a client outage before promotion, converged
  eight workers to one receipt in 5–6 seconds, kept both backends ready and left
  zero fixture residue. This is local physical-failover evidence, not managed
  multi-AZ fencing, provider acceptance, alert delivery or mixed traffic.
- the RS-06 local PostgreSQL 17 scale gate ran the exact generated retention
  candidate query three times over 100 sources, 10,000 eligible immutable
  snapshots, 10,000 artifacts and 10,000 reachability edges; single-source
  unique-reclaim preview measured 3.071–3.143 ms, 100 bounded batches measured
  2.150–2.213 seconds end-to-end with 23.232–23.649 ms p95, and exact
  candidates/bytes/cleanup passed; this is not a 10,000-user, two-replica or
  failover result.
- the RS-06 shared-artifact PostgreSQL matrix passed three runs: eligible-only
  sharing counts once, any retained same-source or cross-source edge excludes
  the body, prune records newly unreachable bytes in a verified hash-chain
  event, and the next preview recomputes from the remaining graph. A companion
  case passed three runs with snapshot publication holding its production
  shared artifact lock: prune waited, the retained edge committed, and the
  prune audit recorded zero newly unreachable bytes.
- the RS-06 purge-receipt gate migrated a disposable PostgreSQL 17 database
  through 387 and passed the ordinary five-pass, response-lost retry and
  expired-lease ambiguity state machines three consecutive times. Direct SQL
  proved three complete and three lower-bound v2 receipts, exact-key absence,
  zero residual intents and the immutable trigger. V1 remained verifiable;
  downgrade with v2 rows failed closed and an empty-ledger 387 down/up passed.
  The companion S3 transport test simulated timeout after mutation with SDK
  retries disabled. This is still local single-primary evidence and does not
  prove candidate-provider deletion, real process kill, failover or billed
  savings.
- the RS-02/RS-06 live object-store gate used an exact source-built MinIO
  `RELEASE.2025-10-15T17-29-55Z` binary on an isolated one-drive network. A
  prefix-scoped application identity passed multi-version/delete-marker purge,
  late-write retry and admin-applied legal-hold failure closure; a second
  identity explicitly denied version deletion failed with the retained exact
  version byte-readable. Final independent listings contained no validation
  versions or markers. This is real local protocol evidence, not a candidate
  vendor, topology, durability, receipt-correlation or billing result.
- the original RS-06 packaged DR gate passed three formal fresh runs plus one
  smoke. The expanded gate then passed three more fresh runs with a signed
  25-table manifest and two artifacts totaling 67,108,902 bytes. The packaged
  restore container was killed at 1,605,632/3,276,800/1,703,936 partial bytes;
  no canonical partial object appeared, retry reclaimed deterministic staging,
  restored both bodies and a second restore was idempotent. An invalid dump
  kept `INCOMPLETE`; archive tamper, missing/changed object and a changed row
  failed with exact redacted findings. Bundles were
  67,548,367–67,548,555 bytes and the full local matrices took 14–30 seconds.
  A final post-guard smoke killed at 4,030,464 bytes and passed in 12 seconds
  with a 67,548,340-byte bundle. These local two-object timings are not
  inventory capacity, RPO/RTO, candidate-provider, response-loss or KMS
  evidence.

The latest full run did not reproduce the historical `pkg/agent` instability;
that does not erase prior evidence, so it remains release-environment debt
rather than a role-source regression.

## Required external evidence before any production cohort

1. Gate A–D: PostgreSQL 17 isolated-schema migration up/down, tenant isolation,
   lock-order races, duplicate/idempotency/commit-timeout injection, primary
   failover and two-replica API behavior.
2. Gate E: candidate-image run with the approved 10,000-user cardinality model,
   two-replica 1,000-role/10,000-skill create/update/adoption contention,
   10,000-snapshot retention candidate race/scale repeat, attestation restart
   burst, API/S3 percentiles, WAL/CPU/pool/lock metrics and alert-series
   cardinality. The local single-primary baselines are retained in
   `RS-04-production-scale-apply-2026-08-14.md` and the RS-06 review and are not
   the production SLO.
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
