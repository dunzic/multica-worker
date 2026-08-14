# RS-03/RS-04 explicit adoption review

Date: 2026-08-13

Gate: design and implementation evidence

Decision: **GO to merge behind the default-off `role_source_apply` flag; NO-GO
for production apply until the live PostgreSQL race matrix and operator
accessibility rehearsal pass.**

## Architecture review — 2/3

The source-neutral plan contract now represents one eligible same-name Multica
Agent, Skill or Autopilot as an immutable adoption candidate. The plan digest
binds source object identity, target kind, target UUID and a SHA-256 commitment
to the target row version. Approval requires every candidate in canonical
order and rejects missing, extra or substituted target data.

Apply serializes the Role Source, loads its mapping ledger, locks every approved
target row, and revalidates tenant, kind, name, target UUID, version commitment
and the absence of any current or historical source mapping. Only then does it
stage the adopted mapping and run the ordinary bounded name and materialization
contracts. A changed, deleted, renamed, archived or newly managed target fails
the whole transaction. Ambiguous and ever-managed same-name targets are plan
blockers rather than silent creates or overwrites.

Adoption does not broaden field authority: role, Skill and Autopilot updates use
the same narrow ownership masks as normal source updates. Existing permission,
owner, lifecycle, model, secret, MCP, enablement and unrelated Skill-file state
remain workspace-owned. An Autopilot is eligible only when it is already
assigned to the exact Agent mapped or adopted for its source role; otherwise
the plan blocks instead of changing the workspace-owned assignment or failing
after approval. The apply receipt contract advances to 1.2 with a
separate adopted count; strict validation still accepts historical 1.0/1.1
receipts without changing their digest serialization.

A disposable migrated PostgreSQL 17 test now proves the generated set query
executes against the real schema, resolves Agent/Skill/Autopilot rows, preserves
two same-title Autopilots as an ambiguous pair and rejects an exact cross-tenant
Skill UUID. Independent UPDATE and DELETE transactions both hit PostgreSQL
SQLSTATE `55P03` while the approved Skill row is locked. After release, a
rename changes the version commitment and removes the old approved identity;
an authorized delete removes the renamed identity. System or archived
same-name Agents participate in the namespace check but are marked ineligible,
so the plan blocks before the database name constraint can fail at apply time.
The same live matrix now holds an uncommitted winner mapping, observes the
losing materializer wait on `transactionid`, commits the winner and verifies
the loser returns typed `ErrApplyConflict` rather than a raw SQL error. The
winner is the only persisted mapping and a later plan-resolution query reports
its exact source as manager. Only the target-mapping unique index is translated
to this state conflict; unrelated unique and serialization errors remain
distinct.

Open objection: the remaining live matrix must still cover ordinary same-name
creation, adopted domain-write rollback, primary failover and
production-shaped contention. Target-row and mapping-index evidence are not a
substitute for the rest of that matrix.

## Product review — 2/3

The operator sees the exact existing object type and immutable ID, must confirm
each adoption separately, can undo confirmation before approval, and cannot
approve until every candidate is confirmed. The receipt distinguishes adopted
objects from new creates and normal updates. The flow avoids database terms in
the decision copy and makes post-plan changes fail closed.

Open objection: a production cohort needs keyboard/screen-reader validation,
human-readable owner/last-modified context and a large-candidate review/search
experience. Raw UUID remains necessary audit evidence but should not be the
only identity cue.

## Test review — 3/3 for target identity and row-lock races; 2/3 overall

Passing local evidence covers exact candidate approval, substituted-target
rejection, unique/ambiguous/managed candidate resolution, plan revalidation,
version mutation detection, synthetic mapping identity, historical receipt
compatibility, query lock/provenance contracts, API schemas, and a 30-case
settings suite including confirm/undo/exact payload behavior. The opt-in live
PostgreSQL test passes three consecutive runs with cross-tenant rejection,
native lock-timeout assertions for edit/delete, post-plan rename/delete
invalidation, real unique-index mapping contention, narrow `state_conflict`
classification and zero fixture residue. Cleanup is strict, exact-workspace
scoped and runs before the pool closes. Focused `rolesource`, `handler`, core
API and views tests pass.

Missing evidence: ordinary same-name creation, transaction failure injection
after every adopted domain write, end-to-end Agent/Skill/Autopilot apply
fixtures, primary failover, candidate-scale contention and browser
accessibility/performance profiling.

## CEO review — 2/3

Explicit adoption removes a major migration obstacle: teams can bring existing
workers under governed source control without duplicate objects or manual data
recreation. Binding approval to one immutable target and preserving workspace
authority keeps the feature enterprise-safe and source-neutral.

Production rollout remains unsupported until the race matrix, incident recovery
and operator evidence are recorded. The feature does not change the branch-wide
NO-GO for customer mutation.

## Required live race matrix

1. ~~target edit/rename/delete before and after the apply row lock~~ — covered
   for exact Skill identity with native PostgreSQL lock-timeout evidence;
2. ~~a second Role Source mapping the target before and after plan creation~~ —
   covered by managed-candidate resolution plus real unique-index wait/winner;
3. ordinary same-name Agent/Skill/Autopilot creation during apply;
4. winner rollback and waiter continuation;
5. statement timeout, primary failover and retry with the same idempotency key;
6. 1,000 adopted roles and 10,000 adopted Skills under the candidate-image SLO.
