# RS-07 process-kill and 10k-backlog evidence review

Feature: controlled channel-delivery ambiguity resolution

Date: 2026-08-15

Decision: **GO for the local PostgreSQL 17 and controlled-pilot gate; NO-GO for
10,000-user GA until candidate-image replicas, PostgreSQL failover, real
providers, KMS/HSM and alert delivery pass.**

This increment closes two local evidence gaps without changing the business
contract. It does not claim provider-level exactly once and it does not treat a
synthetic 10,000-receipt database as 10,000 active users.

## Evidence shape

### Real OS-process kill chain

The test runs three distinct child test-binary processes against one PostgreSQL
17 database. It advances lease-expiry timestamps directly, so it proves state
and ownership transitions without waiting through two production 30-second
leases; its 70 ms duration is not a recovery-time measurement.

1. Replica A claims the `retry_authorized` publication lease and is killed.
2. After the lease is made expired, replica B reclaims it, atomically consumes
   the signed non-delivery authorization, owns delivery attempt 2, and is killed
   before any provider call or receipt write.
3. Replica C claims the expired provider-send lease and freezes it as
   `ambiguous/lease_expired`.
4. The final row retains reconciliation generation 1 and its immutable original
   evidence, has new attempt-2 ambiguity evidence, requires generation 2, and
   cannot be reclaimed by an ordinary task event.

The complete chain passed six consecutive runs across two independent
`-count=3` invocations. This is stronger than a goroutine race, but it is not
yet two complete backend images with connector credentials, network traffic
and provider acceptance.

### 10,000 authorized-retry backlog

The fixture creates 10,000 content-free `retry_authorized` delivery rows and
10,000 generation-1 receipts. Every ambiguity body and reconciliation digest is
computed through the production canonical encoders; `CopyFrom` only accelerates
fixture insertion. Therefore the gate validates every audited receipt, but it
does not claim to have executed 10,000 KMS signatures or serializable approval
transactions.

Each of six runs across two independent invocations performs:

- 200 production `ListRecords(..., 100)` calls at concurrency 16, including
  complete application validation of the 100 returned receipt chains;
- query-plan checks for the workspace-delivery list, selected receipt chains and
  authorized-retry due queue;
- eight concurrent worker loops claiming batches of 200 through the production
  `FOR UPDATE SKIP LOCKED` update;
- exact uniqueness checks over all 10,000 delivery IDs, lease-state checks and a
  post-drain audit read;
- teardown under the immutable-receipt workspace mode and an independent zero-
  residue query.

Observed local PostgreSQL ranges:

| Measure | Six-run range (two independent `-count=3` invocations) |
| --- | ---: |
| Fixture insertion | 634.7–699.2 ms |
| Database growth | 31.35–32.19 MB |
| WAL during fixture | 33.97–34.49 MB |
| Audit p50 | 13.23–15.68 ms |
| Audit p95 | 33.68–44.95 ms |
| Audit p99 | 41.83–66.46 ms |
| 10k publish-lease claim total | 108.91–124.91 ms |
| Claim-batch p95 | 20.33–24.27 ms |
| Claim throughput | 80,058–91,816 rows/s |
| Duplicate claims | 0 |
| Residual rows after each run | 0 |

The planner used `idx_channel_delivery_workspace_listing`,
`channel_delivery_reconciliation_generation_unique`, and the new migration-398
partial index `idx_channel_delivery_retry_publish_due`. Migration 398 is one
`CREATE INDEX CONCURRENTLY` statement and its empty-ledger down/up check passed.

These numbers are a local single-primary baseline. They exclude provider API
latency, message reconstruction, Redis/realtime fanout, TLS, multi-AZ network,
connection-pool sharing with normal traffic and 10,000-user arrival patterns.

## Architecture expert review

Score: **3/3 for local lease/index architecture; 2/3 for target topology**

Accepted:

- publication ownership and provider-send ownership are separate leases, and a
  process death at either boundary cannot create two application send owners;
- consuming the authorization before a provider call remains atomic with the
  delivery attempt increment;
- death after consumption returns to ambiguity, preserving the receipt chain
  instead of silently manufacturing another retry;
- page-bounded audit reads do not scan all workspace receipts;
- the authorized-retry queue now has an ordered partial index matching its
  production claim path;
- the 10k gate proves `SKIP LOCKED` partitioning across worker instances with
  exact ID uniqueness, not only final row counts.

Open objections:

- repeat with two candidate backend images and a controllable Slack/DingTalk
  sandbox transport, killing the owning container after request write, provider
  acceptance and receipt commit independently;
- fail over PostgreSQL during authorization commit and both lease claims;
- measure pool, CPU, WAL, lock waits and alert delay while normal chat, audit and
  role-source traffic run concurrently;
- verify the in-process event bus/connector registration behavior under the
  approved multi-replica topology.

## Product expert review

Score: **2/3**

The controlled workflow now has evidence that a crashed operator retry does not
turn into either a silent loss or an automatic duplicate. A 10,000-incident
backlog remains inspectable and leaseable without changing the read-only UI.

The fixture is deliberately an extreme database backlog, not a forecast of
normal incident volume. Product GA still needs real support drills, ownership,
age/SLA policy, provider-export usability, escalation messaging and a decision
on who may request versus cryptographically execute reconciliation.

## Test expert review

Score: **3/3 for repeatable local gates; 2/3 for production evidence**

Accepted:

- process boundaries are real OS processes and each killed owner is observed in
  PostgreSQL before termination;
- six consecutive process-kill chains and six consecutive 10k scale runs pass
  across two independent invocations;
- every audit sample runs production digest and identity validation;
- query plans, exact unique claims, final lease state and cleanup are asserted;
- migration 398 follows the repository's concurrent-index rule and has an
  explicit rollback;
- the default delivery package and both opt-in live gates pass under Go's race
  detector; `go build ./...` passes with Go 1.26. The all-package test attempt
  passed every package except pre-existing environment-sensitive failures in
  unmodified `pkg/agent` and `pkg/redact`, so it is not reported as green;
- the `deaf76966` runtime image applied migration 398 and passed `/health` and
  `/readyz`; a temporary second same-commit image sharing PostgreSQL skipped the
  already-applied migration and also returned ready before being removed.

Missing:

- candidate-container kill points around a real provider request;
- primary/standby PostgreSQL failover and retry under lost commit responses;
- real KMS/HSM signing, key rotation/revocation and signature audit export;
- mixed 10,000-user workload, provider throttling and alert delivery;
- the complete green CI operating-system/service matrix and repository-wide
  race execution; the changed delivery/live concurrency paths are green, but
  the current Alpine full-suite failures and Redis/provider/KMS/failover jobs
  remain separate release gates.

## CEO review

Score: **2/3 for production launch; 3/3 for funding the next gate**

The work removes a plausible scale objection cheaply: the database paths are
bounded, indexed and uniquely fenced at a backlog much larger than a controlled
pilot should reach. It also proves that process death after authorization does
not force support to choose between duplication and data editing.

It does not yet price or prove a 10,000-user service. Commercial launch remains
NO-GO until real provider behavior, multi-AZ database failure, key custody,
support labor and alert/RTO evidence are measured in the candidate topology.

Strategy decision: merge and deploy migration 398 with the controlled-pilot
build. Keep broad connector claims disabled. Make the next paid infrastructure
exercise a two-backend, PostgreSQL-primary-failover and real-provider fault
matrix, not another synthetic unit benchmark.
