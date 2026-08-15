# RS-03 read-only operator audit UI review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for merge behind disabled flags; NO-GO for production approval/apply operations**

## Customer and product outcome

Workspace users can inspect configured role sources and the latest deterministic plan without using database or daemon tooling. The surface shows source identity, adapter version, snapshot and plan digests, create/update/archive/blocked counts, blocking diagnostics and individual proposed actions. It deliberately does not create sources, request scans, approve decisions or apply changes.

## Architecture review — 2/3

- Reuses the existing tenant-scoped member read APIs and source-neutral plan contract; no AgentWaker-specific control-plane branch was added.
- Requires both `role_source_sync` and `role_source_scan`, matching the server read-endpoint gates and preventing a visible tab that can only return 404.
- Uses React Query for server state and adds no view-owned server cache.
- Keeps all mutation authority server-side and exposes no mutation client method from this slice.
- Open objection: response decoding is typed but not runtime-schema validated in the web client. Production rollout should add bounded decoding and a stable corrupt-response error surface.

## Product review — 2/3

- Gives operators a useful first audit view before exposing high-consequence controls.
- Empty, loading, failure and blocked states are explicit in all supported locales.
- A prominent notice explains why approval/apply is unavailable.
- Open objections: raw operation/state codes need product labels; affected workers, queued/running tasks, archive consequences, ownership boundaries and recovery readiness are not yet presented.

## Test review — 2/3

- Tests prove default-off behavior, the two-flag gate, immutable-plan/blocker rendering, empty state and absence of approve/apply/configure buttons.
- Core TypeScript passes. Views TypeScript reaches only two pre-existing missing type declarations (`hast` and `mdast`) outside this slice.
- Open objections: add malformed/oversized response tests, accessibility interaction coverage, browser visual verification and a live server/database integration test.

## CEO review — 2/3

- The view makes governed bulk role management demonstrable without increasing execution authority or incident exposure.
- It supports the enterprise trust story: users can see what an external source proposes before any controlled write path is enabled.
- Open objection: it is not yet a complete sellable workflow. Production value requires impact preview, approval ownership, recovery confidence, measured upgrade time saved and incident-reduction evidence.

## Security, privacy and data-loss decision

- No secret values, artifact bodies or task content are requested or rendered.
- Digests and normalized diagnostics are evidence, not authority; the server remains responsible for tenant checks and integrity revalidation.
- The feature remains disabled by default. No UI mutation path may be added until live PostgreSQL atomicity/fault evidence, impact preview and recovery gates are accepted.

## Rollout decision

Merge is allowed behind both disabled flags. Internal read-only evaluation may begin after browser verification against a live test server. Customer rollout and every mutation control remain NO-GO.
