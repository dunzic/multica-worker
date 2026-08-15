# RS-06 purge ambiguity local validation evidence — 2026-08-15

Scope: implementation commit `6abd7070c` on local macOS/OrbStack, disposable
PostgreSQL 17 gate, and the retained self-host Docker Compose environment

Decision: **local default-off merge gate passed; customer production remains
NO-GO.** See the four-perspective decision in
[`RS-06-purge-ambiguity-evidence-2026-08-15.md`](../reviews/RS-06-purge-ambiguity-evidence-2026-08-15.md).

## Build and deployed runtime

- backend image: `multica-backend@sha256:1cfef60441fde3d7388c63c81b0ed87294aa484e5bff7eb63d02fb903937402c`;
- frontend image: `multica-web@sha256:d9aa4a647f831c255c95b8897b1a3c870cb71f9d9f09e1de69fffde8bc247125`;
- container binary: `multica 6abd7070c (commit: 6abd7070c, built:
  2026-08-15T19:31:00+08:00)`, Go `1.26.6`, Linux arm64;
- backend `/health`: `200`, body `{"status":"ok"}`;
- backend `/readyz`: `200`, database and migrations both `ok`;
- frontend `/`: `200`;
- retained primary database: migration 387 applied at
  `2026-08-15T11:32:45.621928Z`; all four ambiguity fields are non-null with
  the intended defaults; the pre-existing 1 user and 1 workspace remained.

The frontend production build completed Next.js compilation, TypeScript, page
data generation and all 21 static pages. Its two CSS optimizer warnings concern
the existing `::highlight(multica-find*)` rules and are unrelated to this
feature.

## Disposable PostgreSQL 17 evidence

1. A fresh database migrated from 001 through 387.
2. Three consecutive runs passed:
   - ordinary five-pass intent-to-receipt completion;
   - provider mutation followed by a lost response and empty-inventory retry;
   - expired `deleting` lease reclaim.
3. Direct SQL after the first three-run gate showed:
   - 3 complete v2 receipts (`ambiguous_attempts=0`);
   - 3 incomplete v2 receipts (`ambiguous_attempts=1`);
   - every receipt had exact-key absence verified;
   - incomplete receipts recorded zero provider versions/bytes as lower bounds;
   - zero residual deletion intents; and
   - `trg_guard_role_source_artifact_purge_receipt_mutation` enabled.
4. The reclaim query returned `purge_evidence_ambiguous=false` for an ordinary
   claim and `true` exactly once for the expired lease, so the alert counter
   covers process interruption without treating ordinary retries as incidents.
5. `migrate down` with v2 rows failed as designed:
   `cannot remove artifact purge ambiguity fields while v2 receipts exist`.
6. On a separate empty ledger, migration 387 down removed all four fields and
   up restored them with the intended non-null defaults.

## Code, API and UI gates

- related Go tests passed for migration command, storage, service, handler,
  role-source persistence, DR and metrics packages;
- related Go vet passed;
- deterministic S3 tests passed preflight/non-mutating classification, partial
  delete output, timeout after provider mutation with SDK retries disabled and
  empty-inventory convergence;
- Core API/schema tests: 87/87 passed;
- Views role-source settings tests: 30/30 passed;
- changed Core and Views files passed ESLint;
- all 4 modified locale JSON files parsed;
- UI review against the current Vercel Web Interface Guidelines led to
  locale-aware `Intl.DateTimeFormat`, semantic `<time>`, `translate="no"` for
  the backend identifier, numeric alignment and narrow-screen wrapping. The
  warning conveys incomplete/lower-bound semantics in text, not color alone;
- `git diff --check` passed.

Package-level Core and Views typecheck still reproduce only the existing Chat
Quick Actions updater errors in unmodified files:
`packages/core/chat/mutations.ts:401` and
`packages/core/realtime/use-realtime-sync.ts:229,318`. They are not counted as
a green release gate and are not caused by this slice.

## Evidence boundary

This record proves local protocol, persistence, parsing, rendering, migration
and single-primary runtime behavior. It does not prove a candidate S3 vendor,
real process kill, two API replicas, PostgreSQL failover, KMS/HSM, 10,000-user
burst SLO, provider accounting, signed RACI or Gate F restore RPO/RTO. Artifact
GC remains default-off until those external gates pass.
