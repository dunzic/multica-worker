# RS-03 transactional apply-event outbox review

Date: 2026-08-13

Gate: design and implementation evidence

Follow-up: the controlled replay objection below is implemented and reviewed in
[`RS-03-controlled-outbox-replay-2026-08-14.md`](RS-03-controlled-outbox-replay-2026-08-14.md).
Hardware-backed two-operator rehearsal remains a production gate.

Decision: **GO to merge behind the default-off `role_source_apply` flag; NO-GO
for production apply until two-replica Redis outage/failover, failure-at-every-
step apply injection and operator replay rehearsal pass.**

## Customer outcome

A successful apply or rollback now reaches every connected workspace through a
single source-neutral `role_source:applied` invalidation. The event refreshes
Role Source evidence and Agent, Skill and Autopilot projections without
publishing one frame per changed object. If the API process exits after the
database commit, a later worker still delivers the event; if Redis accepted an
event but the database acknowledgement failed, retries reuse the same durable
event ID and clients suppress duplicates.

## Architecture review — 2/3

The apply transaction writes the successful receipt, hash-chain audit and a
bounded outbox row before one commit. The row contains only source/apply IDs,
mode and three SHA-256 commitments in typed columns; it has no generic JSON
payload and excludes manifests, artifact bodies, paths, request keys, errors,
credentials and secret/MCP content. PostgreSQL constrains IDs, digests, event,
actor, state and retry fields.

Every API replica runs the same bounded dispatcher. `FOR UPDATE SKIP LOCKED`, a
30-second ownership lease and exact lease-token acknowledgement distribute 100
events per one-second pass without duplicate ownership. Expired work is
reclaimed; retry delay grows from five seconds to a capped 21m20s; attempt 20 is
terminal. Published rows remain seven days and dead rows 30 days for evidence,
then are deleted hourly in 500-row batches through status-specific partial
indexes. Workspace teardown explicitly deletes
outbox rows because the repository forbids foreign keys and cascades.

The realtime boundary now has a failure-reporting durable method. A
single-node Hub acknowledges local fanout. A Redis-backed dual-write first
publishes cross-node with the database UUID as `event_id`, returns Redis
failure to the dispatcher, and performs local fanout only after Redis accepts
the frame. The existing bounded client dedup cache
suppresses local/relay and ambiguous-ack repeats. The outbox does not replace
the authoritative query API: missed or expired browser events recover by
normal refetch/reconnect semantics.

Open objection: the current acknowledgement means “accepted by the selected
realtime broadcaster,” not “observed by every browser.” Multi-region replay,
operator-controlled dead-letter replay and PostgreSQL partitioning remain
future scale work. The seven/30-day policy must be reviewed against measured
event volume before a large cohort.

## Product review — 2/3

One event keeps the UI coherent after another administrator applies a source
without adding per-object notification noise. The UI still displays the
authoritative receipt and audit history; the event itself is not presented as
proof of success. A reconnect or missed event cannot corrupt state because it
only triggers refetches.

Open objection: there is not yet an authorized operations page for backlog,
dead-letter acknowledgement or controlled replay. Production support needs an
owner, escalation copy and rehearsal; ordinary workspace users must never gain
a blind replay control.

## Test review — 2/3

Passing evidence includes:

- all 368 migrations applied to a disposable PostgreSQL 17 database;
- a real generated-query test proving a live lease excludes a second worker,
  an expired lease is reclaimed, a stale token cannot acknowledge and attempt
  20 becomes dead;
- focused and full related Go tests plus race detection across realtime,
  service, rolesource, metrics, handler and server packages;
- a durable broadcaster test proving Redis errors propagate with an unchanged
  event ID;
- a frontend test proving one event invalidates Role Source, Agent, Skill and
  Autopilot query trees;
- sqlc v1.31.1 compile/generation, `go vet`, migration-policy checks, targeted
  ESLint and `git diff --check`.

Open objection: staging must still kill a server between commit/publish and
publish/ack, interrupt Redis before/after XADD, expire leases across two
replicas, exercise primary failover and measure backlog recovery at the
approved 10,000-user event rate.

## CEO review — 2/3

Reliable post-commit visibility removes an enterprise trust gap: source-driven
changes no longer appear “stuck” solely because the serving process died. The
implementation reuses the shared realtime platform, remains adapter-neutral and
adds one workspace event per apply, so ecosystem growth does not multiply fanout
cost by object count.

Production value is not yet proved. The cohort decision needs measured apply
frequency, incident reduction, Redis/PostgreSQL operating cost, dead-letter
rate and mean time to resolution. This slice does not change the branch-wide
production NO-GO.

## Rollout and rollback

- Keep `role_source_apply` disabled by default.
- The dispatcher is safe to run before apply enablement; without committed
  outbox rows it is idle.
- On sustained `ack_failed`, `release_failed`, dead-letter or five-minute
  backlog alerts, disable apply, preserve rows and repair PostgreSQL/Redis.
- Never replay the apply request to repair an event. Only an audited future
  outbox replay procedure may republish the stable event ID.
- Rolling back server code requires first draining active outbox rows; the new
  migration must not be removed while a candidate binary may still write it.
