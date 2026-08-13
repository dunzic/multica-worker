# RS-04 production-shape apply evidence review

Date: 2026-08-14

Gate: implementation and local PostgreSQL scale evidence

Decision: **GO to merge behind default-off role-source apply; NO-GO for broad
production until candidate-image topology, contention, failover and operator
SLO evidence pass.**

## Customer outcome

The largest supported single-source create shape is no longer justified only
by unit-test batch counts. Against all 375 migrations on PostgreSQL 17.10, the
real control plane materialized 1,000 private Agents, 10,000 Skills, 10,000
Agent-to-Skill bindings and 11,000 provenance mappings from 11,000 distinct
verified 8 KiB artifacts. The same request through a newly constructed control
plane returned the exact committed receipt without a second materialization.
A second snapshot then renamed and version-updated every mapped Agent and Skill
while retaining sampled user-managed state.

## Architecture review — 2/3

- The fixture calls the production `ApplyPlan` path. `CopyFrom` is used only to
  seed the immutable artifact and integrity ledgers; every business target,
  mapping, receipt, audit and outbox row is written by production code.
- Each artifact body has a real path-derived SHA-256 and exact 8 KiB body. The
  preflight therefore performs 11,000 body reads and verifies 90,112,000 bytes
  before mutation locks.
- Persisted exact counts prove that all bounded Agent, Skill, association and
  mapping batches complete in one transaction. Retry through a new control
  plane proves durable request idempotency.
- Four successful runs measured 4.12–14.76 seconds for first apply,
  51.8–59.1 milliseconds for idempotent retry, 10.5–17.1 MB database growth,
  22.7–29.2 MB WAL, 259–276 MB peak Go heap and a 1,514,740-byte receipt.
- The prior fixed eight-slot admission policy was unsafe against the measured
  heap. Per-node concurrency now defaults to two, is explicitly configurable
  from one to eight, fails startup/chart rendering outside that range and
  retains immediate HTTP 429 overload behavior instead of unbounded queuing.
- Two full update runs changed all 11,000 mapped objects in 12.65–12.76 seconds,
  generated about 22.5 MB WAL and peaked at 280–281 MB Go heap. The v2 Agent
  and Skill names plus Agent version description changed, while an operator-set
  permission mode, concurrency, model, environment, MCP configuration, Skill
  config and disabled association remained intact. Retry took 64.6–66.7 ms.

Open objections: this is one API process, one local container and cached local
artifact reconstruction. It does not measure S3 latency, CPU, statement timing,
lock waits, two-replica admission, primary failover or 50 concurrent applies.
The 1.51 MB receipt and roughly 1.35 GB cumulative allocation per run warrant
profiling and possibly a compact/paged operator response before broad rollout.

## Product review — 2/3

The controlled cohort can now use an evidence-backed upper-bound create case
instead of extrapolating from small imports. A repeated response after timeout
returned in 51.8–59.1 ms and cannot duplicate the 11,000 business objects.

Open objections: three slower runs took 11.99–14.76 seconds while one warmed
run took about four seconds, so neither range is a production promise. Queue wait,
progress feedback, gateway timeouts, operator cancellation and large-receipt UI
behavior still need candidate-environment validation.

## Test review — 2/3

Passing evidence:

- all four create runs and both all-object update runs passed with exact
  business and evidence-row counts;
- each run cleaned workspace, source, actor, artifacts and deletion intents;
- an independent database query after the suite confirmed zero residue;
- ordinary role-source tests, server configuration tests, Helm lint, default
  rendering and fail-closed concurrency rendering pass;
- the adjacent PostgreSQL fault matrix already covers nine ordered rollback
  stages, a real Agent-plus-mapping rollback, ambiguous commit, cancellation
  and process-restart retry.

Open objections: the update fixture changes every mapped role and skill, but it
does not yet combine 1,000/10,000 scale with explicit adoption,
immutable-version mismatch, same-title races, concurrent user edits,
rollback-duration measurement or a hard-killed process.

## CEO review — 2/3

This evidence materially lowers pilot risk and exposes the true infrastructure
cost of the headline source size: about 12 seconds cold, up to 276 MB measured
heap and a 1.51 MB receipt on this machine. The safer two-slot default fits the
chart's 1 GiB backend limit better than the historical eight-slot assumption.

Broad commercial claims remain premature. A local create benchmark does not
prove the stated 10,000-user fleet target, support staffing, cloud cost,
availability during failover or acceptable operator experience under
contention. Keeping apply default-off is still the correct rollout boundary.

## Evidence and next gate

Command:

```bash
MULTICA_LIVE_ROLE_SOURCE_SCALE_TEST=1 \
DATABASE_URL='postgres://multica:***@127.0.0.1:55440/multica?sslmode=disable' \
go -C server test -count=3 \
  -run '^TestRoleSourceProductionScaleApplyPostgres$' -v ./internal/rolesource
```

Observed apply durations: `12.064040459s`, `11.993792291s`, `4.118714209s`,
`14.756004125s`. Observed peak heap: `264866872`, `259244872`, `276388048`,
`259944536` bytes. Observed WAL: `29173848`, `24549872`, `25043128`,
`22746480` bytes. PostgreSQL reported version 17.10;
post-suite workspace/source/actor/artifact/delete-intent residue was zero.

Observed all-object update durations: `12.764764s`, `12.646844292s`; peak heap:
`280129864`, `281395984` bytes; WAL: `22492168`, `22536784` bytes; retry:
`64.561709ms`, `66.686166ms`. Both runs reported `source_fields_updated=true`,
`protected_agent=true`, `protected_skill=true` and `disabled_binding=true`.

The next gate must run the candidate image with real object storage and two API
replicas, collect p50/p95/p99 and queue wait, exercise mixed update/adoption and
same-title/user-edit contention, kill one API process during apply, fail over
the database primary, and demonstrate a memory-safe per-Pod concurrency value.
