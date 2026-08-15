# RS-01 unloaded-attestation incident recovery review

Date: 2026-08-14

Feature: runtime loaded-configuration attestation

Decision: **GO for the corrective change and rebuilt local environment; the
feature remains NO-GO for broad production until candidate-topology failover,
restart-burst and capacity gates pass.**

## Incident evidence

The running local backend returned HTTP 500 for both registered runtime
heartbeats every 15 seconds. Each failure was PostgreSQL SQLSTATE `22023`:
`cannot get array length of a scalar`. A legal unloaded attestation omits the
wire `sources` field, which decodes to a nil Go slice. The persistence boundary
encoded that slice as JSON `null`, while the table constraint unconditionally
called `jsonb_array_length(sources)`.

This was not a daemon authentication, migration-version or historical-data
problem. Both evidence tables contained zero rows before the repair, and the
failure reproduced on every first attestation retry.

## Architecture expert review — 2/3

- The storage boundary now canonicalizes an empty validated source list to the
  JSON array `[]`; loaded statements still require 1–512 strictly validated
  entries before this boundary.
- The database fix is independent defense. A CASE-guarded constraint evaluates
  array length only when `jsonb_typeof(sources) = 'array'`; a scalar now fails
  closed as SQLSTATE `23514` rather than aborting through an unsafe function
  call.
- Migration 376 adds the replacement constraints `NOT VALID`, migration 377
  validates existing rows with the lower-lock validation phase, and migration
  378 removes the unsafe originals only after validation. Upgrade ordering
  therefore never leaves either table without a state/length constraint.
- Existing no-foreign-key workspace/runtime lock ordering, atomic current plus
  observation write, and daemon acknowledgement-after-commit semantics are
  unchanged.
- A 3 still requires a two-backend restart burst during PostgreSQL primary
  failover and recorded lock/latency evidence on the approved cohort shape.

## Product expert review — 2/3

- A runtime with no configured role sources again reports the deliberate
  `not_loaded` state instead of appearing as a failing daemon. This preserves
  the product distinction between unattested, unloaded and loaded/drifted.
- The corrective path is transparent to users and does not expose config IDs,
  paths, source content or credentials.
- Local desktop and daemon sessions reconnected automatically after the
  backend-only rebuild; no database, frontend or user-data reset was required.
- A 3 still requires guided operator UX for persistent attestation failures and
  cohort evidence that restart recovery meets the product SLO.

## Test expert review — 2/3

- A dedicated, fully migrated PostgreSQL 17 database passed the legal unloaded
  write and both-table scalar-rejection tests three consecutive times.
- The role-source migration suite completed a real isolated-schema up/down
  round trip with migrations 376–378 included.
- The rebuilt backend applied 376–378 to the existing local database. Two real
  runtime rows were then persisted with `loaded=false`, `jsonb_typeof(sources)
  = 'array'` and `sources = '[]'`; the observation table contained the two
  matching first observations.
- Subsequent 15-second runtime heartbeats no longer emitted attestation
  persistence warnings or `/api/daemon/heartbeat` 500 responses. `/health` and
  `/readyz` both returned HTTP 200, with database and migration checks `ok`.
- A 3 still requires fault injection during commit acknowledgement, primary
  failover and sustained 10,000-user restart bursts rather than a single-node
  local PostgreSQL result.

## CEO review — 2/3

- The change removes a retry storm that otherwise generates one failed
  transaction per runtime every heartbeat interval and makes the newly built
  audit feature look like a daemon outage before customers configure it.
- The repair is adapter-neutral and prevents the same empty-state defect for
  AgentWaker, manifest-directory, signed-remote and future adapters.
- No destructive migration, data rewrite or customer-visible workaround is
  required.
- A 3 requires measured fleet incident-rate reduction, recovery time and
  database cost under the approved production cohort.

## Release rule

Ship the corrective migrations and persistence normalization together. Do not
raise RS-01 above 2/3 or enable a production cohort on this local recovery
alone. Gate B must retain the unloaded-array and scalar-rejection regressions;
Gate E must include a first-attestation/restart burst and primary failover.
