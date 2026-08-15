# RS-07 ambiguous-send reconciliation review

Feature: Freeze provider-acceptance ambiguity before any retry

Date: 2026-08-15

Decision: CONDITIONAL GO — the no-blind-resend safety boundary is suitable for
the Slack/DingTalk pilot; general availability still requires an authorized
resolution workflow, remaining connector coverage and target-topology load and
failover evidence

## High-value outcome and non-negotiable invariants

A network timeout is not proof that a notification failed. Slack or DingTalk
may accept a request before Multica loses the response or fails to commit the
receipt. Retrying that task can duplicate a customer-visible notification. This
change makes “provider acceptance unknown” a first-class, tamper-evident state
instead of mislabelling it as a retryable failure.

The approved invariants are:

1. Only a connector-proven rejection may enter `failed` and automatically earn
   a later lease.
2. Transport loss, gateway/5xx uncertainty, partial chunk delivery, an empty
   provider receipt, receipt-persistence failure and process death enter
   `ambiguous`.
3. `ambiguous` cannot be claimed by a replay, an expired-lease path or an
   installation cleanup helper.
4. Caller cancellation cannot cancel the bounded five-second evidence write.
5. Evidence contains no message body, credential or raw provider error. It
   binds the row identity, payload digest, attempt, optional provider ID,
   reason and timestamp under contract `2.0`.
6. Operators can see why resend is blocked, but no UI or ordinary API can
   mutate the state or issue a blind retry.

Slack's official `chat.postMessage` reference documents method errors but does
not establish a connector-neutral exactly-once contract that Multica can rely
on. The current `slack-go` client path also has no stable application-supplied
idempotency key for this call. DingTalk's current robot send body returns a
`processQueryKey`, but the implementation has no verified provider query or
idempotency contract suitable for automatic resolution. Therefore the design
does not claim exactly-once delivery.

References:

- <https://docs.slack.dev/reference/methods/chat.postmessage>
- <https://open.dingtalk.com/document/orgapp/custom-robot-access>

## Implemented contract

- migrations 388-389 add `ambiguous_at`, the status and evidence-shape guard,
  and validate new checks in a second migration so the existing-row scan does
  not extend the first migration's access-exclusive metadata lock;
- sqlc claims only `failed` rows for a normal retry. An expired `pending` row is
  reserved exclusively by the reconciler and cannot be reclaimed by a task
  replay;
- the reconciler re-fences each expired row and writes canonical
  `lease_expired` ambiguity evidence rather than manufacturing a timeout
  failure;
- `delivery.Send` uses a detached, bounded outcome context. Unknown errors are
  ambiguous by default; connectors must explicitly wrap a rejection as a
  definite failure;
- Slack treats rate limiting, authentication errors and ordinary `{ok:false}`
  API rejections as definite, while transport errors and documented
  `fatal_error`/`internal_error` uncertainty remain ambiguous. If an earlier
  chunk has a timestamp, the partial provider ID is retained;
- DingTalk treats pre-send token failures and explicit 4xx/429 message
  rejections as definite. Transport errors, gateway/5xx and success-response
  decode uncertainty remain ambiguous. A successful earlier chunk is retained;
- API and installed-client schemas validate v2 ambiguity evidence against all
  row identities and strip malformed results from the audit list;
- the settings view adds an `ambiguous` filter and explicit “resend blocked”
  warning without exposing channel or provider routing IDs;
- bounded metrics distinguish `failed` from `ambiguous`; any new ambiguity
  alerts immediately, while reconciler query/write failures have a separate
  alert and runbook.

## Architecture expert review

Score: 3/3 for the safety boundary; 2/3 for full RS-07 general availability

Accepted:

- unknown is the default, so a new connector cannot accidentally turn a
  transport error into a retryable state;
- normal retry and crash reconciliation use disjoint SQL paths;
- compare-and-set lease tokens protect every terminal write;
- replay after ambiguity validates evidence and returns `ErrAmbiguous` before
  provider invocation;
- evidence remains canonical and content-free, with database shape constraints
  plus application-level digest/identity validation;
- removing the unused installation-detach query eliminates a future path that
  could have converted an in-flight send to a false failure.

Open architecture gates:

- a controlled resolution state machine is not implemented. Provider-native
  query evidence or two-person manual evidence must resolve or supersede a row
  without rewriting history;
- Feishu, WeCom and richer outbound/attachment paths are not covered by this
  ledger;
- multiple message chunks are represented by only the last confirmed provider
  ID, so `partial_delivery` cannot prove which complete subset arrived;
- target two-replica PostgreSQL failover and connector-worker fencing evidence
  remain external production gates.

## Product expert review

Score: 2/3

Accepted:

- the customer-facing risk is now described truthfully: “may have been
  accepted” is distinct from “failed”;
- there is no tempting resend button;
- operators have a bounded reason code, correlation identity, evidence digest
  and runbook response without a second copy of customer content;
- ordinary explicit rejections remain retryable, so safety does not freeze
  routine rate-limit or credential repair.

Open product gates:

- authorized users cannot yet attach provider reconciliation evidence, request
  approval, mark a superseding notification or close the incident;
- the current safe outcome can remain frozen indefinitely and has no in-product
  escalation/SLA owner;
- attachments and per-chunk outcomes are not visible;
- terminology and workflow still need customer-support usability validation.

## Test expert review

Score: 2/3

Passing evidence on 2026-08-15:

- Go unit and integration packages: delivery, Slack, DingTalk, handler, metrics
  and migration lint;
- Slack explicit authentication rejection is `failed/authorization`; a
  connection loss after the first chunk is `ambiguous/partial_delivery` with
  the first provider ID retained;
- DingTalk explicit 429 is `failed/rate_limited`; connection loss and HTTP 502
  are ambiguous;
- generic fault tests cover response timeout, partial send, empty provider ID,
  receipt write failure, expired lease, row tampering, definite-failure retry
  and duplicate-event suppression;
- the complete core suite (121 files / 1,365 tests) and views suite (320 files /
  3,812 tests) pass, including the focused API (87) and audit component (2)
  tests. Package typecheck reports only the three pre-existing Chat Quick
  Actions errors, not a new delivery error;
- an isolated PostgreSQL 17 schema clone applied migration 388, ran 32
  concurrent claims with exactly one sender, froze replays after ambiguity,
  proved an expired lease cannot be event-reclaimed, rejected an incomplete
  ambiguity row, and passed down/up migration round trip. The final 388-389
  split was then reapplied in a fresh clone; both constraints reported
  `convalidated=true`, the live gate passed again, and the 389→388 rollback
  path completed before the temporary database was removed;
- on a separate PostgreSQL 17 gate containing 500,000 historical failed
  delivery rows, migration 388 completed in 0.07 seconds and migration 389
  validated both checks in 0.14 seconds; both reported `convalidated=true` and
  all 500,000 rows remained before the temporary database was removed.

Missing evidence:

- the all-package Go command is not a clean release signal in the available
  `golang:1.26-alpine` harness: delivery and every changed package pass, but
  unrelated daemon/execenv tests require missing `bash`/`git`, non-root
  execution and host permission semantics. These environmental failures must
  be rerun in the repository's CI-equivalent image;

- actual process kill after provider acceptance, followed by restart on a
  second backend replica;
- PostgreSQL primary failover during claim and receipt commit;
- real provider sandbox exercises and provider audit-export reconciliation;
- 10,000-user queue/index latency, ambiguity alert threshold and on-call drill;

## CEO review

Score: 2/3

The change removes a high-reputation-risk behavior: Multica will no longer
convert uncertainty into a duplicate customer notification simply to make a
queue look green. It also creates truthful evidence for enterprise support and
audit. That value justifies shipping the safety freeze to the controlled pilot.

General availability remains a NO-GO. An enterprise product cannot leave
incidents frozen forever or ask support staff to edit a database. Fund the
controlled reconciliation workflow and production fault/load exercises before
marketing delivery assurance or exactly-once behavior.

Final strategy decision: enable the freeze with the existing Slack/DingTalk
pilot after migrations 388-389 and runtime checks pass. Keep the broader
RS-07/10,000-user gate CONDITIONAL, and never describe this as exactly-once.
