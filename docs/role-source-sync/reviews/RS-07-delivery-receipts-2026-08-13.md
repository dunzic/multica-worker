# RS-07 delivery receipt and external readback review

Feature: Delivery receipts and external readback

Date: 2026-08-13

Decision: CONDITIONAL — the shared backend contract is suitable for a Slack/DingTalk controlled pilot; provider ambiguity, attachment receipts, remaining connectors, operator retry UX and live evidence remain general-availability blockers

## Delivered customer outcome

Agent replies sent to Slack or DingTalk no longer disappear into transport logs. Before a connector sends, Multica creates or reclaims a task-scoped delivery claim. A duplicate completion event for an already delivered/read-back task returns the existing provider message ID without calling the provider again. A provider failure becomes a visible standard error code and may earn a fresh attempt while retaining the same correlation ID. A process crash leaves a 30-second pending lease; the independent reconciler converts abandoned claims to retryable timeout failures rather than leaving permanent “sending” state. Both connectors also send a separate `failure_notice` for a terminal task failure only when no automatic retry is pending. It has its own ledger identity, so it cannot collide with a normal chat reply; a failure to send that notice is recorded but never recursively emits another notice.

The ledger stores no message body. It records the SHA-256 payload digest, tenant/task/session/connector routing identities, provider message ID, attempt count, status and a canonical evidence document whose digest is verified again whenever history is listed. A conflicting replay that reuses the same task identity with different content fails closed.

When a later inbound message explicitly replies to the delivered provider message ID, the shared channel router advances `delivered` to `readback` once and records the inbound message ID in new digest-bound evidence. This is evidence of an explicit reply, not a claim that the platform exposed a passive read receipt.

Workspace members can list the audit history. The response exposes payload and evidence digests but no message body, idempotency key, credential, token or provider error text. The shared Integrations settings page now renders the newest 100 verified rows with status filtering, connector type, task/correlation identity, attempts, safe error code, evidence commitment and time. It deliberately does not display channel-chat or provider-message routing IDs and has no resend action.

## Architecture expert review

Score: 2/3

- one source-neutral schema and service is used by two different production adapters rather than duplicating provider tables;
- `(installation, task, operation)` identity, payload-digest comparison and a fenced lease suppress concurrent/duplicate sends in the normal event-replay case;
- status transitions are monotonic after delivery, and explicit readback is matched by installation plus provider message ID;
- canonical evidence covers delivery/correlation/tenant/task/session/connector/payload/provider IDs, attempt count and delivered/readback timestamps;
- terminal history is refused when evidence or its indexed row identity is altered;
- installation teardown detaches preserved audit evidence and fails pending claims; workspace deletion removes all owned delivery and role-source records explicitly.

Residual architecture work:

- a provider can accept a message and the database completion write can then fail. Slack/DingTalk do not currently receive a provider idempotency key, so this ambiguous window cannot guarantee exactly-once delivery;
- Feishu, WeCom and richer adapter-specific outbound paths are not yet on the ledger;
- the cross-platform envelope is text-only, so attachment identity, authorization-at-send and attachment-missing evidence are not represented;
- there is no durable outbox that reconstructs and safely retries a failed completion independent of another event/task rerun.

## Product expert review

Score: 2/3

- delivered, readback, failed and in-flight states are understandable and correlated to the originating task;
- the history endpoint is useful for support and customer audit without disclosing conversation content;
- a failure carries a stable correlation ID, attempt count and safe error category.

Open product work:

- terminal task failures now notify the originating Slack or DingTalk conversation after retries are exhausted; there is still no in-app escalation or one-click controlled retry for transport failures;
- the settings UI explicitly describes `readback` as an inbound message that replied to the delivered provider message, not passive “read” telemetry;
- no attachment-level delivery detail;
- operators still need task/API knowledge to act on a failed row.

## Test expert review

Score: 2/3

- tests cover duplicate completion suppression, payload conflicts, provider failure and retry, correlation stability, empty provider IDs, duplicate/out-of-order readback and evidence tampering;
- Slack and DingTalk outbound tests prove both adapters supply the exact workspace/installation/task/session/operation/payload identity to the same recorder and persist the returned provider ID;
- connector tests prove terminal failure notices use the independent operation identity and retry-pending failures stay silent;
- router tests pin the rule that only an explicit reply with both provider and inbound message IDs creates readback;
- handler tests reject unverifiable terminal rows and pin the content-free response surface;
- installed-client schema validation fails closed on malformed digests and terminal evidence that disagrees with row task/correlation/status/payload/attempt identity; component tests prove filtering, explicit-reply copy, routing-ID suppression and absence of a resend control;
- migration policy and generated-query compilation cover concurrent index conventions and deletion ownership.

Missing evidence:

- live PostgreSQL duplicate-event races, lease expiry under replica failover and migration round trip;
- real Slack/DingTalk timeout, 401/429 and provider-message readback exercises;
- crash injection specifically after provider acceptance and before receipt commit;
- attachment deletion/permission-loss cases once attachment delivery is modeled;
- 10,000-user queue/index/load behavior and production metrics/alerts.

## CEO review

Score: 2/3

This is a meaningful enterprise trust feature: support can answer “was it sent, where, under which task, and did the recipient explicitly respond?” without retaining a second copy of the conversation. The same contract across two ecosystems is also evidence that the capability is a product primitive rather than a one-off integration patch.

General availability would create avoidable reputation risk until ambiguous provider acceptance is reconciled, the major connectors share the contract, failures have an operator workflow, and live/load evidence supports the stated scale.

Final decision: CONDITIONAL
