# RS-04 automation title race policy review

Date: 2026-08-13

Gate: implementation evidence; live concurrency evidence remains open

Decision: **GO to merge behind the default-off `role_source_apply` flag; NO-GO
for production apply until the PostgreSQL race matrix passes.**

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

## Concurrency logic

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

## Evidence completed

- lock helper unit test proves canonical deduplication and database transaction
  lock usage;
- query contract test proves claims are derived only from active Role Source
  mappings and do not impose global title uniqueness;
- source-order tests prove create/update lock and recheck before write, and the
  materializer locks and revalidates before its batch statement;
- focused `autopilotlock`, `rolesource`, and `handler` Go packages pass.

## Evidence still required

Run a disposable PostgreSQL 17 matrix with separate connections for:

1. ordinary create first, Role Source apply second;
2. Role Source apply first, ordinary create second;
3. ordinary rename into and away from the desired title;
4. managed-target edit racing source update;
5. winner rollback, waiter continuation and statement timeout;
6. two large applies with overlapping titles in reverse manifest order.

Each case must prove bounded waiting, no deadlock, the intended winner, complete
rollback and a stable conflict response. This review does not claim that live
database evidence yet.
