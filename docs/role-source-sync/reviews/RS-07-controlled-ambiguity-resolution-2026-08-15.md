# RS-07 controlled ambiguity-resolution review

Feature: Two-person, evidence-bound resolution of uncertain connector sends

Date: 2026-08-15

Decision: CONDITIONAL GO for a controlled Slack/DingTalk pilot. This closes the
product dead-end where an `ambiguous` send could remain frozen forever, without
weakening the no-blind-resend boundary. General availability and the broader
10,000-user production claim remain NO-GO until the external gates below pass.

## High-value outcome

Provider acceptance can be unknown after a timeout, partial chunk send,
receipt-commit failure or process death. The previous RS-07 safety boundary
correctly froze the row, but offered no authorized way to finish the incident.
This increment adds a narrow control plane whose value is:

- preserve the original ambiguity evidence instead of rewriting history;
- require two distinct operator keys over the exact same 15-minute canonical
  authorization;
- commit only a digest of external provider/incident evidence;
- close a confirmed delivery or business-superseded message permanently;
- authorize a retry only when provider non-delivery is positively established;
- reconstruct that retry from the already-redacted database outcome and require
  its payload digest to equal the quarantined send;
- consume the authorization once behind the normal delivery lease fence.

This remains an at-least-once connector integration with evidence-based duplicate
prevention. It is not marketed or documented as provider-level exactly once.

## Approved state model

| Current state | Signed outcome | Next state | Provider call |
| --- | --- | --- | --- |
| `ambiguous` | `confirmed_delivered` | `reconciled` | Never |
| `ambiguous` | `closed_no_retry` | `reconciled` | Never |
| `ambiguous` | `confirmed_not_delivered` | `retry_authorized` | Background worker may publish the original digest-bound outcome once |
| `retry_authorized` | connector atomically claims | `pending` | Exactly one Multica sender owns the lease |
| controlled `pending` | definite rejection | `failed` | Ordinary safe retry policy resumes |
| controlled `pending` | unknown acceptance | `ambiguous` | Frozen again; a new signed generation is required |

At most three reconciliation generations are allowed. `partial_delivery` can
never be resolved as `confirmed_not_delivered`, because at least one chunk has a
provider receipt. This is enforced by the command, transaction and database
constraint—not only by an operator instruction.

## Implemented control and evidence contract

- migrations 390–398 add `retry_authorized` and `reconciled`, bounded generation
  state, a separate append-only `channel_delivery_reconciliation` chain, four
  reconciliation indexes, a bounded assistant-result lookup index, a mutation
  trigger and deferred existing-row validation;
- each receipt binds delivery/workspace identity, generation, outcome/reason,
  original canonical ambiguity evidence, original evidence digest, external
  evidence digest, requester/approver key IDs, both signature digests,
  authorization digest, previous receipt digest and its own digest;
- requester and approver key IDs must differ; aliases of one Ed25519 public key
  are rejected; authorizations expire after exactly 15 minutes but an already
  consumed authorization remains idempotently queryable;
- the offline `channel_delivery_reconcile inspect|prepare|execute` command
  requires an explicit PostgreSQL 17 URL, mode-0600 private inputs, a protected
  public-key file and exact canonical bytes. It cannot accept message content or
  call a provider;
- confirmed non-delivery creates `retry_authorized`; a 30-second publish lease
  fences multiple backend replicas. A crash before publish is reclaimable, while
  a crash after provider acceptance returns through the existing expired-send
  ambiguity freeze;
- retry publication loads the terminal task and assistant message, verifies
  task/session/operation/status, rebuilds the exact chat reply or failure notice,
  and compares SHA-256 with `payload_digest` before publishing;
- the workspace audit response exposes outcome, bounded reason, generation and
  evidence/receipt digests, but omits authorization ID, signer key IDs and
  signature digests. There is no ordinary HTTP/UI mutation or resend button;
- audit retrieval first selects at most 100 delivery rows and then loads only
  those delivery IDs' reconciliation generations (at most three each), rather
  than scanning every receipt ever written for the workspace;
- workspace teardown explicitly removes receipts under the same scoped teardown
  mode that permits other immutable audit records to be removed;
- a dedicated alert separates expired-lease freeze failures from an authorized
  retry that cannot be reconstructed or consumed.

## Architecture expert review

Score: 3/3 for the state/evidence design; 2/3 for target-topology proof

Accepted:

- operational state and immutable history are separate; an authorized retry may
  overwrite the mutable row receipt later, while the original ambiguity body and
  digest remain reconstructable in the append-only chain;
- the signed authorization binds the current evidence digest and next generation,
  so stale or cross-delivery approvals fail compare-and-set validation;
- the receipt is inserted before the state transition in one serializable
  transaction, and authorization/generation uniqueness makes uncertain command
  responses safely idempotent;
- `retry_authorized` avoids converting uncertain delivery into a generally
  retryable `failed` row. The event claim atomically consumes the one approval;
- the background worker stores no duplicate message body and cannot substitute
  new content; database reconstruction plus payload digest is the authority;
- normal failed retries, controlled retries and expired pending leases remain
  distinct SQL paths.
- the persisted assistant-result lookup has a partial `(task_id, created_at
  DESC)` index and the audit receipt lookup is page-ID bounded, so both recovery
  and operator reads have bounded database work at large workspace history.

Follow-on evidence: [RS-07 process-kill and 10k-backlog review](./RS-07-process-kill-10k-scale-2026-08-15.md)
and [RS-07 PostgreSQL primary-failover review](./RS-07-postgres-failover-2026-08-15.md).

Open architecture gates:

- repeat the now-passing three-process lease-kill chain with two complete
  candidate backend images and real provider transport/acceptance kill points;
- repeat the now-passing local PostgreSQL 17 physical-failover transaction in
  the candidate managed multi-AZ topology and prove fencing/pool recovery;
- provision the production KMS/HSM keyring, rotation, revocation and audit export;
- add provider-native read/query adapters where a verified contract exists;
- extend the same ledger to Feishu, WeCom, attachments and per-chunk evidence.

## Product expert review

Score: 3/3 for incident truthfulness and operator decisions; 2/3 for support GA

Accepted:

- operators see three understandable business outcomes instead of editing rows;
- confirmed non-delivery earns one controlled retry; confirmed delivery and
  supersession both close the incident without creating another notification;
- the UI remains deliberately read-only and does not tempt a workspace admin to
  bypass platform incident review;
- the current ambiguity and prior reconciliation can coexist after a controlled
  retry becomes uncertain, so support sees the real sequence rather than a
  misleading last-write-only status;
- external provider evidence is referenced by digest, avoiding a second copy of
  customer content in the product database.

Open product gates:

- conduct a support usability drill with real Slack/DingTalk provider exports and
  measure time-to-decision, wrong-outcome rate and escalation clarity;
- assign an SLA owner and escalation age for `ambiguous` and `retry_authorized`;
- decide which customers may request a reconciliation while keeping cryptographic
  execution a platform-operator action;
- add chunk-level presentation before supporting long-message partial delivery as
  a routine incident workflow.

## Test expert review

Score: 3/3 for deterministic code/database gates; 2/3 for real infrastructure

Passing evidence on 2026-08-15:

- Go unit tests cover decision/reason compatibility, generation limit, distinct
  keys, duplicate-key aliases, signature verification, receipt tampering,
  persisted-output digest reconstruction, failure-prefix reconstruction and one
  background authorization consumption;
- API/UI tests cover malformed reconciliation fail-closed behavior, sanitized
  response fields, new status filters, outcome rendering and locale parity;
- a fresh PostgreSQL 17 database applied all migrations through 397. The
  reconciliation segment rolled 396→390 down, proved the table/columns were
  removed, and reapplied 390–396 with all four channel-delivery checks
  validated; migration 397 was then independently rolled down/up and its retry
  lookup produced `Index Scan using idx_chat_message_assistant_task`;
- the live gate ran 32 concurrent delivery claims with one sender, then 16
  concurrent executions of one signed reconciliation authorization. Every caller
  received the same generation-1 receipt, exactly one immutable row existed, one
  retry publish lease was claimed, and one delivery claim advanced to attempt 2;
- the terminal outcome could not be event-reclaimed; direct receipt update and
  delete were rejected by the mutation trigger;
- a three-process OS-level gate passed six consecutive runs across two
  independent `-count=3` invocations: a killed publish-lease owner was
  reclaimed, a killed controlled-send owner became attempt-2
  `ambiguous/lease_expired`, generation 1 remained valid and generation 2
  became mandatory;
- a 10,000-row authorized-retry fixture passed the same six-run pattern. Two
  hundred concurrent audit samples validated complete receipt chains at
  41.83–66.46 ms p99; eight workers claimed all 10,000 publish leases uniquely
  in 108.91–124.91 ms with zero duplicates and zero cleanup residue. Migration
  398's due-queue partial index was used and its down/up check passed;
- with 500,000 historical failed delivery rows, migration 390 completed in 0.05
  seconds and migration 396 validation in 0.20 seconds. All 500,000 rows remained,
  all four constraints were validated and the partial-delivery database guard was
  present. The final live concurrency gate passed again on that database before
  the temporary database was deleted;
- focused core API tests passed 87/87; the audit component plus locale parity
  passed 162/162. The complete Core suite passed 121 files / 1,365 tests and the
  complete Views suite passed 320 files / 3,812 tests. TypeScript reports only
  the three pre-existing Chat Quick Actions errors and no new channel-delivery
  error;
- all changed Go packages and `go build ./...` passed, and the delivery package
  plus both opt-in process/scale gates passed under Go's race detector; the
  current non-root Go 1.26 Alpine all-package run passed every package except
  pre-existing environment-sensitive failures in unmodified `pkg/agent` and
  `pkg/redact`; Helm lint/config rendering passed, and both backend and Web
  production images built. The
  backend image contains the executable reconciliation command and migration
  397; the Next.js production build completed its TypeScript, page-generation
  and trace phases.

Missing evidence:

- two candidate-backend replicas with real provider kill points and managed
  PostgreSQL failover/fencing fault injection;
- real Slack/DingTalk sandbox sends plus provider audit-export reconciliation;
- KMS/HSM signing, rotation and emergency revocation drill;
- 10,000-user mixed connector traffic and alert delivery under the candidate
  production topology; the passing 10,000-receipt local database gate is not a
  user-cardinality or provider-throughput result;
- a green complete CI operating-system/service and repository-wide race matrix;
  the Alpine all-package attempt is not a release green because unmodified
  `pkg/agent` process-cleanup/tight-timeout cases and `pkg/redact`'s synthetic
  HOME assertion failed; that all-package attempt also does not provision the
  separate Redis/provider/KMS/failover service matrix.

## CEO review

Score: 3/3 for risk-adjusted product value; overall production decision remains 2/3

This increment converts a safe but operationally incomplete freeze into a
governed enterprise incident workflow. It protects customer trust twice: it
does not duplicate a message merely to clear a queue, and it does not leave
support with an indefinite unresolved record or a database-edit runbook. The
cryptographic separation of requester and approver creates procurement and audit
value beyond the connector feature itself.

The cost is justified for the Slack/DingTalk controlled pilot. It does not justify
an exactly-once or 10,000-user GA claim yet. Real provider evidence, production
key custody, two-replica fault behavior and capacity/DR gates are business launch
requirements, not optional engineering polish.

Final strategy decision: ship migrations and the offline reconciliation command
to the controlled pilot after candidate deployment smoke tests pass. Keep the
ordinary product UI read-only. Do not enable broad customer claims until every
external gate above has an owner, dated evidence and an accepted rollback drill.

## Local candidate deployment evidence

The controlled-pilot implementation was committed as `cfb0f4f29`; the process-
kill/scale/index increment was committed as `deaf76966` and installed in the
local self-host Compose environment on 2026-08-15. The PostgreSQL volume and
existing frontend were preserved while the backend was recreated.

- the running backend binary contains commit `deaf76966`;
- `/health`, `/readyz` and the frontend `/login` route each returned HTTP 200;
- the existing deployment had migrations 390–397; this startup applied 398
  once, and `schema_migrations` records
  `398_channel_delivery_retry_publish_due_index`;
- all four channel-delivery constraints report `convalidated=true`, the four
  reconciliation indexes, `idx_chat_message_assistant_task` and
  `idx_channel_delivery_retry_publish_due` exist;
- `/app/channel_delivery_reconcile` is executable and migration 398 is present
  in the runtime image;
- a temporary second `deaf76966` backend image joined the same Docker network
  and PostgreSQL database, skipped already-applied migration 398 and returned
  `/readyz` with both database and migrations `ok`; it was then removed to
  restore the standard single-backend local environment;
- a separate isolated gate then exercised two backend containers, shared Redis,
  HAProxy and a synchronous PostgreSQL 17 physical standby. Three fresh runs
  hard-killed the primary, withheld promotion until a client error, converged
  eight reconciliation workers to one receipt in 5–6 seconds, kept both
  backends ready and left zero fixture residue. This remains local evidence,
  not provider delivery, cross-replica event routing or managed multi-AZ proof.
