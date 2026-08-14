# RS-03 two-control-plane apply concurrency review

Date: 2026-08-14

Gate: implementation and live PostgreSQL evidence

Decision: **GO to merge behind default-off apply; NO-GO for production cohort
expansion until primary failover, process-death, Redis publish/ack and
production-shaped contention gates pass.**

## Customer outcome

Two Multica server instances can receive the same or competing apply requests
without duplicating role materialization or silently advancing the wrong
snapshot. Independent role sources can still progress concurrently, so the
safety boundary does not serialize all synchronization work for a workspace.

## Architecture expert — 3/3 for this slice

- Exact duplicates serialize on PostgreSQL transaction state and converge on
  one immutable receipt, audit event and outbox event.
- Competing plans for one source serialize and re-evaluate the current-snapshot
  CAS after the winner commits; the loser records bounded failure evidence.
- Separate sources complete in parallel while one source transaction is held.
- The exercise uses two independent `pgxpool` instances and observes the real
  `transactionid` wait; it does not depend on an in-memory mutex shared by one
  process.
- Test cleanup is exact-key scoped and reports every cleanup failure, avoiding
  false green runs or deletion of another concurrent fixture's evidence.

Open objection: this is one PostgreSQL primary. It does not prove failover
fencing, replica routing, Redis event delivery or behavior while PostgreSQL is
unavailable.

## Product expert — 3/3 for duplicate/competing request behavior

The product contract is now crisp: an exact retry converges to the first
receipt; a different plan loses with a recoverable state conflict; unrelated
sources are not blocked for the duration of another source's materialization.
This supports safe browser retries and horizontally scaled API servers without
asking operators to deduplicate business objects manually.

Open objection: the operator UI still needs controlled-cohort validation for
long waits, process loss and failover messaging. No broader production promise
is authorized by this slice.

## Test expert — 3/3 for same-primary two-control-plane concurrency

Passing live PostgreSQL 17 evidence:

- exact duplicate: second pool observed `transactionid`, one committed apply;
- competing same-source plan: second pool observed `transactionid`, one winner
  plus one hash-only `state_conflict` failure record;
- different sources: second apply completed while the first transaction was
  held (6.69 ms in the recorded local run);
- all random workspace, actor, source and artifact-intent fixture counts were
  zero after cleanup;
- the existing 13-case atomic failure/commit-ambiguity matrix still passed
  after cleanup was made strict and fixture-scoped.

The first run correctly failed because its observer identifier exceeded
PostgreSQL's 63-byte `application_name` limit and was truncated. The observer
now emits a short unique identifier and exact matching is proven by the
captured wait event. This was a test-observability defect, not a hidden retry.

Open objection: repeat under candidate images with large writes, process kill,
primary failover, pool saturation and network fault injection.

## CEO — 2/3

This removes a costly horizontal-scaling risk: duplicate or racing approvals
cannot create duplicate Agents, Skills, automations, receipts or notifications,
and unrelated sources retain useful parallelism. The result strengthens the
case for a controlled pilot without coupling the product to AgentWaker.

The business score remains 2 because no production cohort throughput, outage
rate, recovery time, support effort or infrastructure cost has been measured.
Those data are required before promising a 10,000-user SLA.

## Rollout rule

- Keep role-source apply default-off.
- Run Gate B6 and Gate B7 on every candidate PostgreSQL/driver change.
- Preserve the exact request key on client retry; never generate a new key for
  an ambiguous response.
- Do not raise the overall production-readiness score until Gate D and Gate E
  produce signed candidate-topology evidence.
