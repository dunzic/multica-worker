# RS-01 controlled role-source lifecycle review

Date: 2026-08-13

Final decision: **GO for merge behind `role_source_sync` and `role_source_scan`; NO-GO for broad production until the live PostgreSQL lock/failure exercises and daemon restart runbook pass**

## Architecture expert

- The explicit state machine is narrow: `registered|active|error -> paused`,
  `paused -> active|registered`, `paused -> detached`, and `detached ->
  paused` through rebind. There is no force flag and no direct active-to-detached
  or detached-to-active edge.
- Every mutation is one transaction with workspace lock, source row lock,
  expected-version compare-and-swap, dependent-work cancellation, state/binding
  update and hash-chained audit event.
- Pause atomically cancels queued/claimed scans and fails pending/claimed/
  submitted secret transfers. Stored envelope bytes are removed and the
  encrypted private key is overwritten before commit.
- Scan and secret-transfer claims now lock the source before the child work
  row. Secret-transfer reporting follows the same source-before-transfer order,
  eliminating the inverse lock order that lifecycle cleanup would otherwise
  introduce.
- Detached sources retain immutable snapshots, mappings, task pins, receipts
  and audit history. They no longer pin the old runtime against cleanup; the
  last runtime ID remains audit context until an explicit rebind.
- Rebind takes the destination runtime's shared registration lock, verifies
  workspace ownership, changes only runtime/config binding and leaves the
  source paused. The old binding's redacted attributes are cleared so the UI
  cannot present stale source labels. No role materialization occurs.

Open objections: the runtime availability check for resume uses the durable
database heartbeat rather than Redis. This is deliberately fail-closed and the
150-second threshold is shared with the sweeper, but failover timing needs a
real cluster exercise.

## Product expert

- The settings surface exposes only transitions valid for the current state.
  An administrator cannot skip pause before detach.
- Confirmation copy explains that pause cancels scans and destroys unconsumed
  secret transfers while preserving materialized workers and history.
- Rebind requires the destination runtime ID and daemon-local config handle,
  then visibly remains paused. Resume is a separate act and requires new loaded
  evidence for the destination binding.
- Members retain the read-only audit view; only workspace owners/admins see
  lifecycle controls, matching server authorization.

Open objections: runtime selection is an exact-ID entry rather than a guided
eligible-runtime picker, and lifecycle audit events are not yet rendered in the
settings history. These are usability gaps, not reasons to weaken the server
transition contract.

## Test expert

- Handler tests cover strict unknown-field rejection, expected-version input,
  safe conflict mapping and response redaction.
- Contract tests cover the shared strict attestation evaluator, tampering,
  cross-runtime evidence, missing/kind/version drift and unloaded evidence.
- Persistence tests assert source-before-child lock ordering, scan cancellation,
  ciphertext clearing, detached-only rebind and claim state filtering.
- Core typecheck, API client PATCH test, settings lifecycle state tests, focused
  Go tests, race tests and vet form the local gate.
- An opt-in live PostgreSQL test performs pause, evidence-gated resume, pause,
  detach, runtime-release verification, rebind and rejection of resume without
  destination evidence.

Open objections: the live test is skipped on this workstation because no
PostgreSQL is available. Staging must additionally inject concurrent claim,
report, pause, runtime deletion and workspace deletion transactions under
timeouts and primary failover.

## CEO / rollout owner

- Value: planned maintenance and daemon replacement no longer require unsafe
  database edits or leaving an intentionally retired source in a permanent
  alert state.
- Risk containment: lifecycle operations cannot apply role changes, erase
  historical evidence, silently overwrite a concurrent admin or auto-resume a
  moved source.
- Operating cost: fixed state transitions and one bounded transaction per
  human action are negligible at 10,000-user scale; no tenant-labelled metric
  series are added.

Decision: merge behind the existing disabled role-source rollout. Permit an
engineering cohort only after the live PostgreSQL lifecycle test, alert pipeline
and restart/rebind runbook are captured. Keep `role_source_apply` disabled.
