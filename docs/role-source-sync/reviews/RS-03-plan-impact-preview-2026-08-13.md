# RS-03 and RS-06 plan impact preview review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for read-only merge behind disabled flags; NO-GO for production approval/apply**

## Outcome and semantics

The latest immutable plan now has a separate operational impact preview. The plan remains deterministic and content-addressed; mutable task state is not inserted into its digest. The impact endpoint binds to the exact plan digest and reads mappings, version pins and task states in one repeatable-read, read-only database transaction.

The contract separates:

- mandatory pre-start cancellation when a role mapping advances to the target snapshot;
- conditional pre-start cancellation only if an archive candidate is explicitly approved for archive;
- running or local-directory-waiting work that continues with its already captured version;
- new roles and existing roles missing a materialization mapping.

Aggregate counts include all matching current-version work. Worker and task details are capped at 200 each and say when details are truncated. Task detail contains only task ID, source role ID, agent ID, status, effect and creation time—never prompt, issue body, result, error, context or secret material.

## Architecture review — 2/3

- Keeps immutable plan evidence and mutable operational evidence in separate contracts.
- Uses the mapping's current snapshot and object digest to exclude old terminal history.
- Starts from the bounded active-task set, then resolves each task through its unique immutable pin; a dedicated concurrent partial index excludes terminal history so query cost follows current concurrency rather than years of retained tasks.
- Rechecks the plan's digest and semantic invariants before using it.
- Open objection: the endpoint has no response cache or query latency telemetry, and PostgreSQL execution plans have not been measured with production-shaped history.

## Product review — 2/3

- Makes the most important upgrade consequence visible before any approval control exists.
- Explains that queued state is rechecked at apply and running work continues on the captured version.
- Separates mandatory and archive-conditional consequences, avoiding the false claim that every archive candidate will be applied.
- Open objections: add role ownership explanations, refresh control/freshness policy, re-enqueue workflow and clearer localized state labels before customer rollout.

## Test review — 2/3

- Unit tests cover mandatory, conditional and continuing effects, already-advanced mappings and missing mappings.
- Handler tests cover default-off behavior and content-free serialization.
- UI tests cover affected worker/task rendering while retaining the no-approve/no-apply assertion.
- Race tests, Go vet, focused frontend static rules, core TypeScript and locale JSON checks pass.
- Open objections: live PostgreSQL tests must prove repeatable-read consistency during concurrent apply, query plans with large retained history, cancellation counts versus trigger writes, timeout behavior and tenant isolation.

## CEO review — 2/3

- This closes a major enterprise trust gap: a bulk role upgrade can show operational blast radius before permission to mutate exists.
- The distinction between cancelled queued work and continuing running work is commercially meaningful and reduces surprise-driven incidents.
- Open objection: measurable customer value still needs upgrade lead-time, avoided rerun cost and incident-rate baselines.

## Rollout decision

Merge behind `role_source_sync` plus `role_source_scan`. Internal read-only use remains conditional on a live database/browser exercise. Approval/apply UI remains prohibited until impact refresh/revalidation is part of the approval workflow and the production database/failure/recovery gates pass.
