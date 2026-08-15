# RS-03 controlled outbox replay review

Date: 2026-08-14

Gate: design, implementation and live PostgreSQL evidence

Decision: **GO to merge behind default-off role-source apply; NO-GO for a
production replay until two named operators complete the hardware-backed
signing/key-rotation rehearsal and Gate B4 passes on two replicas.**

## Customer outcome

An apply that committed successfully but exhausted realtime delivery can now
recover its notification without changing business state. Operators requeue
the original database event UUID; clients continue to deduplicate it and refetch
the authoritative receipt-backed state. The procedure cannot execute apply,
accept a replacement event body or expose a replay button to workspace users.

## Architecture review — 2/3

The replay boundary is a separate offline binary rather than a server route.
Inspection and execution independently verify the succeeded apply receipt, its
plan/snapshot/mode commitments, the exactly matching success audit and every
audit-chain event from sequence one through that success. A serializable
transaction locks the dead outbox row, writes an immutable replay receipt and
returns the same row to pending. The receipt chain, event replay count and
generation unique index make the three-generation limit durable under
concurrency. Exact authorization retry reconciles commit ambiguity by its UUID.

The schema contains typed identifiers, closed reason codes, key IDs and
SHA-256 commitments only. It does not store an event payload, incident text,
authorization JSON, signature bytes, private keys or customer data. Settled
cleanup preserves replayed outbox rows so receipts are not orphaned; guarded
workspace teardown removes receipts before events. DR exports and semantically
verifies both tables.

Open objection: the command still depends on direct database reachability and
an externally governed signing/audit system. PostgreSQL cannot prove that two
key IDs belonged to two different humans; production identity separation,
hardware protection, key revocation and historical public-key retention are
organizational and KMS/HSM controls. The full audit-chain verification is
bounded at 100,000 events; sources approaching that bound need checkpointed
audit-chain verification before replay can remain available.

## Product review — 2/3

The operator now has three explicit phases: inspect, prepare and execute. The
15-minute authorization binds the exact event, next generation, receipt and
incident commitment. Closed reason codes keep reporting comparable. A returned
receipt gives an auditable resolution artifact, while an ambiguous response is
resolved by repeating the same signed request rather than creating a new one.

This deliberately is not a self-service workspace feature. Delivery replay is
rare incident recovery, not a routine retry experience, and a maximum of three
generations prevents a broken dependency from becoming an unbounded operator
loop. The runbook requires dependency recovery and post-replay client/state
confirmation.

Open objection: the production support model still needs named requester,
approver and incident-owner roles, expected approval time, escalation coverage
and an evidence-retention period. Without that RACI, the binary alone does not
reduce mean time to recovery.

## Test review — 2/3

Passing evidence includes:

- all 375 migrations applied in order to disposable PostgreSQL 17;
- a real state-machine test proving altered commitments fail closed, two
  concurrent executions consume one generation, exact retry remains idempotent
  after authorization expiry, changed signatures conflict, three chained
  generations succeed and a fourth is refused;
- real database enforcement rejecting replay-receipt update/delete outside
  teardown, followed by successful guarded workspace teardown;
- unit tests for exact canonical authorization, 15-minute lifetime, malformed
  and duplicate keyring input, aliased public keys, wrong signatures, strict
  files, symlinks, exclusive output, strict JSON and missing database target;
- an inspect/prepare/two-signature/execute/inspect production-binary exercise
  showing the original event UUID move from dead attempt 20/replay zero to
  pending attempt zero/replay one;
- a real DR manifest export/verify round trip including outbox and replay rows;
- sqlc regeneration/compile and focused race-tested Go packages.

Open objection: local keys and one PostgreSQL container do not prove the
production KMS/HSM, two-human approval, key rotation, network interruption,
operator-process death, primary failover or two-replica Redis behavior. Those
remain mandatory Gate B4/B5 staging evidence.

## CEO review — 2/3

The slice closes a material enterprise support gap without granting a broad
mutation capability. It preserves trust in a committed apply, reduces pressure
for dangerous manual SQL or duplicate apply, and creates a bounded evidence
trail suitable for incident review. Its adapter-neutral event identity scales
with applies rather than the number of roles or materialized objects.

The economic claim remains unproved. Before a large cohort, measure dead-letter
frequency, approval/recovery time, operator load, retained-row growth,
PostgreSQL verification cost and customer-visible stale-state duration. If dead
letters are common, replay is masking realtime reliability debt and rollout
must stop until Gate B4 improves.

## Rollout and rollback

- Keep `role_source_apply` default-off and do not publish replay credentials to
  workspace, application-server or ordinary support identities.
- Provision 2–32 approved Ed25519 public keys; keep private operations in the
  KMS/HSM and enforce requester/approver separation outside Multica.
- Rehearse inspect/prepare/sign/execute/inspect on staging, including expired
  exact retry, key rotation and all three generations, before production use.
- On any commitment, signature, generation or serialization error, preserve
  evidence and stop; never edit the tables or replay apply.
- A server rollback may leave the offline binary unused, but migrations and DR
  table coverage must remain while any replay receipt exists. Do not downgrade
  through migration 369 until every affected workspace has completed its
  approved retention or teardown lifecycle.
