# RS-04 automation title race policy review

Date: 2026-08-13; live evidence updated 2026-08-14

Gate: implementation plus disposable PostgreSQL 17 concurrency evidence

Decision: **GO for a controlled default-off cohort; NO-GO for broad production
apply until candidate-topology timeout, process-kill and failover gates pass.**

## Product contract

Ordinary Multica Autopilots permit duplicate titles. A partial unique index on
active workspace titles would therefore be a breaking product change and could
also fail deployment on legitimate existing duplicates.

Role Source keeps its stricter namespace without changing that contract:

- ordinary Autopilots may still share titles with other ordinary Autopilots;
- a non-archived Role Source-managed Autopilot claims its exact workspace title;
- ordinary create or rename into a managed title returns HTTP 409 with
  `role_source_autopilot_title_conflict`;
- archived mappings and archived Autopilots do not retain a claim.

## Architecture expert review — 2/3

All participating writes take a PostgreSQL transaction-scoped advisory lock
derived from workspace ID plus exact title. Multi-title operations sort and
deduplicate titles before locking, preventing lock-order deadlocks.

The Role Source transaction locks every desired automation title, then repeats
the tenant-wide name conflict query before any automation batch write. Ordinary
create and rename take the same lock and check the active mapping ledger before
writing. A waiter therefore observes the winner's committed row or mapping and
fails cleanly; rollback releases the lock without leaving a claim.

Rename locks both old and new title namespaces. The existing Autopilot row lock
and `updated_at` comparison still reject stale edits after the title decision.
The live matrix proves independent sessions wait on the same PostgreSQL
`advisory` lock and reverse-order two-title requests converge on canonical order
without deadlock.

A 3 still requires two backend replicas, bounded statement timeout, process
death and primary failover under the approved cohort load. Out-of-band SQL
writers are outside the supported application contract and must not mutate
managed titles.

## Product expert review — 2/3

- Ordinary-to-ordinary duplicate titles remain valid; no migration or global
  uniqueness constraint changes the established Autopilot contract.
- A managed title produces one stable conflict decision for both ordinary
  create and rename. Rename-away releases the namespace for the waiting Role
  Source transaction after commit.
- Rollback never leaves a ghost title claim, target or mapping.
- A 3 requires cohort UX evidence for the 409 recovery message and explicit
  operator guidance when a long-running apply causes visible waiting.

## Test expert review — 2/3

- lock-helper tests prove canonical deduplication and transaction-scoped
  advisory-lock usage;
- query-contract tests prove claims derive only from active Role Source mappings
  and do not impose global title uniqueness;
- source-order tests prove ordinary create/update and the materializer lock and
  recheck before writing;
- a fully migrated PostgreSQL 17 database passed six live cases: ordinary-create
  commit wins, managed commit blocks create, managed commit blocks rename-in,
  rename-away lets managed continue, ordinary rollback lets managed continue,
  and reverse-order multi-title locking completes without deadlock;
- all six cases passed three consecutive runs, each observed native PostgreSQL
  `advisory` waiting and each left zero user/workspace/Agent/Autopilot/trigger/
  mapping fixture rows.

A 3 still requires managed-target edit/source-update overlap, statement-timeout
injection, two large overlapping applies, process kill and primary failover in
the candidate topology.

## CEO review — 2/3

- The policy avoids a breaking global title-uniqueness migration while giving
  managed automation a deterministic ownership boundary.
- Clean winner/loser semantics prevent silent duplicate scheduled work and the
  support cost of reconciling two rules that appear to be the same employee
  automation.
- The lock key is workspace-scoped, so unrelated tenants and titles retain
  parallelism; the gate adds no durable table or recurring worker cost.
- A 3 requires measured contention rate, p99 wait, support recovery time and
  lost/duplicated-run incidence in the approved cohort.

## Remaining production evidence

Execute Gate B10 plus the candidate two-replica/primary-failover matrix. Retain
statement-timeout, process-kill, overlapping-large-apply and managed-edit race
evidence with row/mapping counts and bounded lock-wait metrics.
