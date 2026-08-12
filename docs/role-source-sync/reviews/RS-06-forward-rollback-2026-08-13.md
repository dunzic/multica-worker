# RS-06 immutable provenance and forward rollback review

Feature: Versioning, provenance and rollback

Date: 2026-08-13

Decision: CONDITIONAL — backend contract is complete enough for controlled staging; disaster recovery, runtime task pinning and UI remain production blockers

## Delivered customer outcome

An administrator can choose a previously successful snapshot and create a rollback plan. The server refuses the active snapshot, an untrusted snapshot, or any snapshot without a cryptographically valid successful apply receipt. Rollback is a new deterministic forward plan from the current immutable snapshot to that historical target. Its `rollback` mode participates in the plan digest, so it cannot be confused with ordinary synchronization or used to rewrite old history.

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

- tasks do not yet persist a role-source snapshot/capability digest pin for historical execution evidence;
- capability binding resolution remains blocked by RS-04's unfinished runtime-pin contract;
- retention tiers and reachability-aware artifact GC are not implemented.

## Product expert review

Score: 2/3

- users can distinguish apply versus rollback and see exact from/to provenance;
- rollback reuses familiar diff, explicit archive/retain decisions and approval instead of providing a dangerous one-click toggle;
- non-restorable secrets fail with an explicit need for a fresh transfer.

Open product work:

- no visual version timeline, affected-role comparison or guided restore-source workflow;
- no bulk scope selector beyond the exact source plan;
- no operator-facing retention policy controls.

## Test expert review

Score: 2/3

- deterministic tests prove rollback mode changes the digest, validates canonically and rejects rollback to the active snapshot;
- receipt validation rejects tampered historical evidence;
- handler tests prove the independent apply feature gate, exact target propagation and history redaction;
- normal apply and rollback idempotency share the receipt verification path.

Missing evidence:

- live PostgreSQL failure-at-every-step rollback transaction tests;
- queued/running-task behavior with persisted version pins;
- artifact retention/GC reachability and restore-from-backup tests;
- production-like disaster exercise.

## CEO review

Score: 2/3

The implementation materially lowers enterprise adoption risk: a bad mass role update has a reviewed recovery path with stronger evidence than a mutable “activate old row” toggle. It also makes version provenance an auditable product surface rather than an internal database detail.

General availability remains blocked until the product can demonstrate recovery under database/object-store restore, identify affected active work, and present the workflow without API expertise.

Final decision: CONDITIONAL
