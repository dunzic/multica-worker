# RS-06 immutable provenance and forward rollback review

Feature: Versioning, provenance and rollback

Date: 2026-08-13

Decision: CONDITIONAL — backend and controlled operator workflow are complete enough for staging; live recovery and cross-system validation remain production blockers

## Delivered customer outcome

An administrator can choose a previously successful snapshot and create a rollback plan. The server refuses the active snapshot, an untrusted snapshot, or any snapshot without a cryptographically valid successful apply receipt. Rollback is a new deterministic forward plan from the current immutable snapshot to that historical target. Its `rollback` mode participates in the plan digest, so it cannot be confused with ordinary synchronization or used to rewrite old history.

The owner/admin operator surface now exposes the action only beside a historical successful receipt and only while controlled apply is enabled. A confirmation explains that plan creation changes no managed object. The resulting plan is visibly labelled `Forward rollback` and must pass the same impact preview, exhaustive archive/retain decisions, exact approval, short-lived daemon secret-transfer checks and separate apply confirmation as an ordinary plan. The active snapshot is not offered as a rollback target; the server remains authoritative if client state is stale or manipulated.

The existing approval and atomic apply gates are reused. A successful rollback records mode, from/to snapshot digests, exact approval, mappings, secret-transfer evidence and a new receipt digest; advances the source current-snapshot CAS only after all writes succeed; and appends `rollback_succeeded` to the hash-chained audit log. Members can list validated successful apply/rollback history without seeing request/idempotency keys.

Historical secrets are deliberately not retained in recoverable role-source storage. A rollback target containing environment or MCP values requires fresh one-time transfers matching that historical snapshot. In practice the operator must restore the source checkout to that version and rescan/re-export; otherwise rollback fails closed. This avoids silently restoring stale credentials.

## Architecture expert review

Score: 3/3 for history and rollback semantics

- snapshots, plans, capability versions, applies and receipts are append-only evidence;
- rollback mode is digest-bound and cannot mutate an old snapshot/apply row;
- the target must have a validated prior successful receipt in the same workspace/source;
- current snapshot compare-and-swap keeps the last-known-good active until the rollback transaction commits;
- idempotent retries require the same plan, actor, approval, mode and secret-transfer set;
- no artifact GC is introduced yet, which favors rollback reachability over premature storage reclamation.

Residual architecture work:

- live PostgreSQL evidence is still required for task-pin/apply/retention races and bounded-query SLOs;
- read-only capability bindings use RS-04's owned package namespace and RS-06's exact runtime pin; write-capable and required-adapter bindings remain blocked;
- reachability-aware artifact GC and retention remain independently default-off until live object-store failure and restore exercises pass.

## Product expert review

Score: 3/3 for a controlled staging workflow

- users can distinguish apply versus rollback and see exact from/to provenance;
- rollback reuses familiar diff, explicit archive/retain decisions and approval instead of providing a dangerous one-click toggle;
- non-restorable secrets fail with an explicit need for a fresh transfer.
- historical successful receipts are the only operator-visible target choices, and current-snapshot/no-op rollback is not offered;
- the confirmation explicitly separates creation of a proposal from later application.

Open product work:

- the receipt list is a bounded recovery selector, not yet a rich version timeline with paging, affected-role comparison or a guided restore-source workflow;
- no bulk scope selector beyond the exact source plan;
- old-version source checkout restoration is still an external operator procedure when fresh secret transfer is required.

## Test expert review

Score: 2/3

- deterministic tests prove rollback mode changes the digest, validates canonically and rejects rollback to the active snapshot;
- receipt validation rejects tampered historical evidence;
- handler tests prove the independent apply feature gate, exact target propagation and history redaction;
- normal apply and rollback idempotency share the receipt verification path.
- client contract tests prove the dedicated endpoint and fail closed on malformed plan evidence;
- operator tests prove the extra confirmation, exact historical target propagation and suppression of the active snapshot action.

Missing evidence:

- live PostgreSQL failure-at-every-step rollback transaction tests;
- queued/running-task behavior with persisted version pins;
- artifact retention/GC reachability and restore-from-backup tests;
- production-like disaster exercise.

## CEO review

Score: 2/3

The implementation materially lowers enterprise adoption risk: a bad mass role update has a reviewed recovery path with stronger evidence than a mutable “activate old row” toggle. It also makes version provenance an auditable product surface rather than an internal database detail.

The workflow no longer requires API expertise and materially improves staged operability. General availability remains blocked until the product demonstrates recovery under database/object-store restore, cross-runtime behavior and production-like failure injection with measured RPO/RTO.

Final decision: CONDITIONAL
