# Four-perspective feature review strategy

Each feature slice must pass a strategy review before implementation, a design review before merge, and an evidence review before rollout. The same four perspectives attend every gate. A review record names the decision maker, evidence, unresolved objections, owner, and due date. “No objection” is not approval.

## Shared scorecard

Each perspective scores 0–3:

- 0: unacceptable or no evidence;
- 1: directionally useful but material gaps remain;
- 2: merge-ready behind a disabled flag;
- 3: rollout-ready for the stated cohort.

A slice cannot merge with any 0, cannot enter production with any score below 3, and cannot average away a security, data-loss, tenant-isolation, audit, or rollback blocker.

## RS-01 Adapter registry and source registration

- Architecture expert: verify compile-time trusted registry, duplicate-kind rejection, adapter/version negotiation, tenant-scoped configuration, redaction, limits, and no source-specific control-plane branching.
- Product expert: verify administrators understand source ownership, supported modes, path/credential visibility, detach behavior, and which fields the source controls.
- Test expert: require registry concurrency tests, malformed config, unsupported version, duplicate registration, permission matrix, cross-tenant access, feature-flag behavior, and API compatibility tests.
- CEO: verify the abstraction creates ecosystem leverage rather than a one-customer fork; approve the first two likely follow-on source kinds and ongoing adapter maintenance cost.
- Go/no-go: at least one AgentWaker adapter and one fake reference adapter pass the same contract suite.

## RS-02 Bounded scan and contract validation

- Architecture expert: verify daemon ownership, canonical path resolver, immutable normalized snapshot, content-addressed transfer, resource quotas, stable hashing, and absence of mutation.
- Product expert: verify diagnostics are actionable, previews distinguish errors/warnings/skips, and a failed scan leaves the current worker unaffected.
- Test expert: require traversal, symlink, race/swap, device file, encoding, manifest bomb, oversized file/count, duplicate IDs, source changes during scan, daemon offline, and deterministic hash tests.
- CEO: verify scan cost and support burden are bounded, and the preview reduces deployment risk enough to justify the workflow.
- Go/no-go: automated evidence proves no agent/skill/config write and no plaintext secret persistence on every scan outcome.

## RS-03 Snapshot, diff, approval and atomic apply

- Architecture expert: verify deterministic diff, immutable decision record, stale-plan rejection, per-source serialization, idempotency, one transaction, outbox/after-commit events, and last-known-good behavior.
- Product expert: verify the plan explains create/update/conflict/archive consequences and allows safe explicit adoption without forcing users to understand database concepts.
- Test expert: require repeated apply, concurrent apply, transaction failure at every step, audit failure, stale snapshot, conflict decision replay, no-op write-count, retry after timeout, and server restart tests.
- CEO: verify bulk upgrade and rollback are differentiated value; require metrics for time saved and incident reduction.
- Go/no-go: one failure-injection suite proves either the whole plan and receipt commit or none of the workspace changes commit.

## RS-04 Role, skill, capability and automation materialization

- Architecture expert: verify stable mappings, ownership masks, dependency resolution, immutable capability versions, runtime digest pins, cleanup order without FKs, and preservation of user-managed state.
- Product expert: verify imported workers are runnable, affected-role blockers are visible, and source-managed versus workspace-managed fields are understandable and reversible.
- Test expert: require rename, delete candidate, same-name conflict, user-added skill preservation, capability profile/version mismatch, automation enable-state preservation, task pinning, and workspace teardown tests.
- CEO: verify shared capabilities reduce duplication and support a marketplace/ecosystem path without creating ungoverned execution authority.
- Go/no-go: all supported runtimes receive identical normalized role intent and exact pinned package digests.

## RS-05 Secret and MCP synchronization

- Architecture expert: verify separate secret channel, envelope binding, nonce/expiry/replay defense, encrypted storage with key metadata, least-privilege reveal, runtime-only decryption, rotation, and fail-closed audit.
- Product expert: verify missing/changed/removed keys are clear without exposing values, ownership policy is explicit, and rotation/recovery workflows are usable.
- Test expert: require logs/snapshots/events/cache/analytics exfiltration scans, replay, wrong source/workspace/role, expired transfer, key rotation, partial secret failure, sentinel collision, and compromised agent-token authorization tests.
- CEO: accept residual credential liability only after security sign-off, incident runbook, and customer-facing responsibility boundary are documented.
- Go/no-go: an independent security review finds no plaintext at rest outside the approved secret store and every reveal/mutation is attributable.

## RS-06 Versioning, provenance and rollback

- Architecture expert: verify append-only history, forward rollback, capability consumer resolution, retention/GC, backup restore, and historical task evidence.
- Product expert: verify users can compare versions, identify affected roles, choose rollback scope, and understand non-restorable secret changes.
- Test expert: require compatible/incompatible upgrade, same-version content change, removed profile, rollback during queued/running tasks, artifact GC reachability, and restore-from-backup tests.
- CEO: verify rollback materially lowers enterprise adoption risk and storage/retention cost is controllable.
- Go/no-go: a production-like disaster exercise restores source history, active workers, audit receipts, and pinned task evidence.

## RS-07 Delivery receipts and external readback

- Architecture expert: verify generic receipt schema, connector correlation IDs, deduplication, retries, signed/hashed evidence, attachment authorization, and late readback reconciliation.
- Product expert: verify users can distinguish attempted, accepted, delivered, externally observed, and business-confirmed outcomes.
- Test expert: require duplicate callbacks, out-of-order events, connector timeout, partial delivery, revoked access, evidence tampering, and manual correction audit tests.
- CEO: verify receipts support trust, compliance, and commercial differentiation instead of becoming connector-specific status noise.
- Go/no-go: at least two different connector types satisfy the same receipt/readback contract and audit view.

## Strategy meeting record template

```text
Feature:
Gate: strategy | design | evidence | rollout
Date:
Participants and accountable approvers:

Customer problem and measurable outcome:
Decision and alternatives rejected:
Architecture score/evidence/open objections:
Product score/evidence/open objections:
Test score/evidence/open objections:
CEO score/evidence/open objections:
Security/privacy/data-loss blockers:
Production scale evidence:
Rollout and rollback decision:
Actions, owner, due date:
Final decision: NO-GO | CONDITIONAL | GO
```
