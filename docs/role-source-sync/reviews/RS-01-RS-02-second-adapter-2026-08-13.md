# RS-01/RS-02 second adapter conformance review

Date: 2026-08-13

Gate: design and merge evidence

Final decision: **GO for merge behind disabled flags; NO-GO for broad customer configuration**

## Customer and product outcome

Teams that can publish Multica's normalized role manifest no longer need to adopt the AgentWaker directory layout. A daemon can scan `multica_manifest_directory` through the same opaque config ID, queue/lease, immutable snapshot, content-addressed artifact and audit-plan pipeline used by AgentWaker. The source file remains a controlled engineering interface; no create/edit UI is exposed.

## Architecture review — 2/3

- The server catalog registers descriptor metadata only; filesystem authority remains in the selected daemon.
- Both real adapters share `rolesource.Registry`, `ScanRequest`, canonical validation/hashing, `ArtifactOpener`, daemon queue/lease and missing-only artifact upload. No source-kind branch was added to the control plane.
- The adapter uses the common root-confined `BoundedFS`, strict JSON with unknown/trailing-field rejection, bounded files and exact artifact digest/size reopen checks.
- Its descriptor advertises no binary or secret-transfer authority; manifests with binary media, configured environment values or MCP servers are rejected.
- Open objections: the adapter trusts publishers to maintain normalized artifact references; there is no signature verification, remote transport, change hint, managed configuration lifecycle or independent schema-version package.

## Product review — 2/3

- This is a credible ecosystem seam: another source can emit one documented neutral contract instead of copying AgentWaker conventions.
- Redacted configuration exposes only final root/manifest names and never absolute paths.
- Open objections: hand-authoring digest/size references is too technical for general users; validation diagnostics need source-line locations; product naming, ownership guidance and a publisher CLI are not complete.

## Test review — 2/3

- Tests prove descriptor/registry compatibility, immutable snapshot creation, artifact retrieval, changed-body rejection, config redaction, traversal/unknown/trailing JSON rejection and absence of secret export.
- Daemon composition tests load and scan both adapters from the same private config document and generic scanner.
- A bounded parser fuzz run and 10,000-role in-process scan fixture pass.
- Open objections: add source swap races at manifest-plus-artifact scale, live daemon/server artifact upload, signature fixtures and cross-platform filesystem evidence. The 10,000-role fixture is not a database or end-to-end SLO result.

## CEO review — 2/3

- A second real adapter proves the investment is a reusable role-distribution platform rather than an AgentWaker-only fork.
- Direct normalized manifests lower the cost for future Git, signed archive and managed catalog adapters.
- Open objections: ecosystem value is not yet commercially measured. Require one independent publisher integration, onboarding-time reduction and maintenance-cost evidence before positioning this as a marketplace platform.

## Security, privacy and data-loss decision

- The normalized manifest type structurally contains no plaintext environment or MCP values.
- The adapter rejects authority it cannot safely deliver instead of accepting incomplete credentials.
- Source-provided content remains data and is never executed during scan/apply.
- Broad rollout remains blocked on signatures/attestation, managed config lifecycle, live upload evidence and operator recovery.

## Rollout decision

Merge and internal controlled evaluation are allowed behind `role_source_sync` plus `role_source_scan`. Customer-facing source creation and all apply controls remain disabled.
