# Community update synchronization

This repository is a fork of `https://github.com/multica-ai/multica.git`. The
purpose of this document is to make upstream intake repeatable, reviewable, and
safe for the fork's own product work.

## Sources and policy

- `upstream/main` is the official community baseline. Review and absorb it as a
  merge so the relationship remains visible.
- Other `upstream/*` branches are candidates, not releases. Absorb one only
  after confirming that the change is useful, complete, tested, and not already
  present on `upstream/main` under a squash or rebased commit.
- `origin/main` is this fork's published branch. Do not push an integration
  without explicit approval.
- Preserve unrelated local changes. Start from a clean worktree or an isolated
  worktree; never clean, reset, or overwrite user work to make a merge easier.

The fork-specific capabilities below require deliberate semantic conflict
resolution rather than choosing an entire side:

- role-source ingestion, capacity, disaster-recovery, and outbox-replay tools;
- channel-delivery audit and reconciliation;
- private/self-hosted deployment behavior and configuration;
- existing database migrations, generated sqlc code, and operator build targets;
- desktop packaging, version provenance, and platform navigation behavior.
- TuringOmni distribution targets: production service defaults remain
  `https://workers.turingomni.com` and installer/update downloads remain in
  `dunzic/multica-worker`. Community code intake must not restore upstream
  login defaults, release feeds, bootstrap binaries, or installer snippets.
  Preserve explicit private deployments and development/staging environments.

## Intake procedure

1. Record the starting branch, commit, dirty state, and remotes.

   ```bash
   git status --short --branch
   git remote -v
   ```

2. Refresh the official baseline and tags without treating every remote branch
   as a release candidate.

   ```bash
   git fetch upstream main:refs/remotes/upstream/main --tags
   ```

3. Compare ancestry and content before deciding that anything is missing.

   ```bash
   git rev-list --left-right --count HEAD...upstream/main
   git log --left-right --cherry-mark --oneline HEAD...upstream/main
   git cherry upstream/main upstream/<candidate-branch>
   ```

   A `+` from `git cherry` is only a lead. Git cannot recognize a multi-commit
   development branch that was squash-merged as one official commit. For that
   case, compare stable patch IDs for the complete ranges:

   ```bash
   git diff <candidate-first>^ <candidate-last> | git patch-id --stable
   git show <official-squash> --pretty=format: | git patch-id --stable
   ```

   Matching patch IDs mean the feature is already absorbed. Do not cherry-pick
   it again.

4. Classify each item before integration:

   - **official baseline**: merge `upstream/main`;
   - **selected candidate**: cherry-pick the smallest complete commit chain;
   - **already equivalent**: record the official commit and skip;
   - **defer/reject**: record the reason, especially incomplete tests, product
     mismatch, or collision with a fork-specific capability.

5. Work on `codex/integrate-upstream-YYYYMMDD` or another scoped `codex/*`
   branch. Resolve conflicts by intent, preserving both applicable behaviors.
   Do not use whole-file “ours” or “theirs” resolution on protected areas.

6. Verify in proportion to the change:

   - run focused tests for every changed subsystem;
   - run `pnpm typecheck` for TypeScript changes;
   - run Go tests for backend changes, and `make sqlc` plus generated-file checks
     for SQL changes;
   - verify locale parity when user-facing copy changes;
   - lint/render deployment manifests when deployment files change;
   - finish with `git diff --check` and a clean status.

7. For a Mac deliverable, synchronize tags before deriving the version, build
   the target-architecture CLI into the app, then verify the DMG, code signature,
   bundle version, embedded CLI version, and SHA-256. Ad-hoc signing is not Apple
   notarization and must be reported as such.

8. Add a row to the decision ledger below. Include both source and resulting
   commits so a later scan can detect rebases and squash merges correctly.

## Decision ledger

| Date | Source | Decision and local result | Verification |
| --- | --- | --- | --- |
| 2026-09-28 | `upstream/main` through `fa5d470ae40a` | Absorbed by merge `4a70ce5dc9cf`; retained fork-specific features during conflict resolution. | Full Go suite, TypeScript typecheck, desktop/core tests, views tests and locale parity, Helm lint/render, Mac arm64 packaging. |
| 2026-09-28 | Hermes development chain `be10b427d`..`4bd1b3344` | Skipped as already equivalent to official squash `138134d34104` on `upstream/main`; combined stable patch ID matched. | Patch-equivalence and ancestry checks. |
| 2026-09-28 | Attachment-viewer chain `f5c91a9e0`..`26e473c10` | Skipped as already equivalent to official squash `6dc6a0b9a2db` on `upstream/main`; combined stable patch ID matched. | Patch-equivalence and ancestry checks. |
| 2026-09-28 | Persistent local-daemon launch `fe09778253d9` | Absorbed as `8db04400e751`; `make daemon` now launches a persistent binary with auto-update disabled, while existing role-source and delivery operator build targets remain intact. | Dry-run command inspection, complete `make build`, real `make multica --version`, and Go suite; process-tree packages were rerun serially in an init-enabled container. |
| 2026-10-06 | `upstream/main`, `fa5d470ae40a`..`b4ca5b4a23e6` (15 commits; v0.6.0/v0.6.1 baseline) | Absorbed by merge `6759f9244` on `codex/integrate-upstream-20261006`, starting from fork `ffe3bbe9b`. No textual conflicts; reviewed overlapping realtime, daemon, task-service, and protocol changes. Retained role-source/DR/replay, delivery reconciliation, SQL/migrations, and persistent operator build targets. Original dirty `main` was not staged, committed, reset, or overwritten; integration is not yet landed there or pushed. | Go default test wrapper; core 2,222, views 5,995, desktop 670 unit tests; whole-workspace TypeScript check; focused PostgreSQL fixture tests for working-agent facets and builtin skills; five-language new-key parity; changed-file lint and build checks recorded below. |
| 2026-10-06 | Legacy builtin skill redirect retirement `b4ca5b4a23e6` | Absorbed with the official baseline, not cherry-picked a second time. Current `multica-platform` skill replaces the old redirect; retained advertised `platform-skill-v1` capability. Do not reintroduce a legacy shim during future intake. Very old daemons whose briefs name the retired skill require an upgrade before deployment. | Database-backed inline and skill-reference claims tested both with and without the capability; skill routing/parser/scoping and fail-closed bundle-read tests passed. |
| 2026-10-06 | Integration tip `46509903c` plus the primary checkout's pending TuringOmni distribution patch | Prepared together as uncommitted changes on `codex/prepare-packaging-20261006`, reusing the attached integration worktree. Kept the executable `multica` / `multica.exe`; omitted the alternate launcher, retained explicit `--profile turingomni` credential isolation, corrected the runtime update feed, and required a fresh Go CLI build for desktop packaging. Original 45 pending files remain unchanged; no new commit, main landing, tag, push, package, deployment, or publication in this preparation step. | Distribution/package/CI-scope Node tests, stubbed shell installer, complete desktop suite, focused views/web tests, non-mobile and mobile typechecks, default regular Go wrapper and fresh guarded CLI/update tests. Exact results and remaining release gates below. |
| 2026-10-06 | Prepared worktree at `46509903c` plus uncommitted distribution changes | User authorized local Mac and Windows packaging. Built `0.6.1-119-g46509903c-dirty`: Mac ARM64/x64 DMG and ZIP, Windows ARM64/x64 desktop EXE, and Windows amd64/arm64 CLI offline ZIP. Delivered under the primary checkout's ignored `dist/installers/20261006-0.6.1-119-g46509903c-dirty/`; no new commit, main landing, push, tag, release upload or deployment. | Twelve packaging/distribution checks rerun; all four desktop bundles' versions/targets/CLI architectures/notices; mounted DMG and ZIP integrity; deep strict Mac signature verification; Windows PE metadata/icon/ASAR-header checks; EXE archive and extracted-payload equality; CLI ZIP/current desktop-binary equality; eight SHA-256 hashes. Signing/native-acceptance limits and workaround below. |

## 2026-10-06 verification and landing boundary

- The official tip was verified through GitHub's API and fetched through its
  SSH-over-443 endpoint after HTTPS Git transport failed. No remote was pushed.
- Core, views, and desktop ran their complete unit suites. The first broad views
  run had one unchanged chat-queue test failure; its isolated rerun and the whole
  suite at four workers passed. Desktop tests reused the installed Electron
  distribution because the offline dependency install skipped install scripts.
- The default Go wrapper passed, but database packages can skip when no database
  is reachable. That is not database acceptance. Separately, repository scripts
  generated `.env.worktree` and created the isolated
  `multica_multica_worker_289` database. Its schema was initialized and the
  affected handler/service fixture tests ran successfully over the PostgreSQL
  container's local socket, without changing the shared database password.
- Full frontend lint did not pass: three existing i18n placeholder errors remain
  in the unchanged `packages/views/settings/components/role-sources-tab.tsx`.
  Other pre-existing hook/disable warnings remain. This intake does not silently
  alter those unrelated files.
- Changed TypeScript files passed lint with five existing unused-disable
  warnings. `make build` compiled the backend, CLI, migrator, role-source DR,
  outbox replay, channel-delivery reconciliation, and capacity tools. Windows
  util/daemon test binaries cross-compiled for both amd64 and arm64; they were
  not executed on a Windows filesystem. These are build checks, not releases.
- No SQL, migrations, sqlc output, mobile source, or operator Makefile targets
  changed in this upstream range. No production login, real-agent smoke,
  browser E2E, Windows filesystem execution, release publication, or new
  installer packaging was performed.
- The original `main` at `ffe3bbe9b` contains 45 pending installer/default-target
  files. Preserve them byte-for-byte. They are not part of this merge commit.
  Before landing or packaging this branch, incorporate the pending TuringOmni
  target change deliberately: production service
  `https://workers.turingomni.com`, releases `dunzic/multica-worker`.
  Do not publish an installer from this code-only branch with community defaults.
  The CLI program remains `multica` / `multica.exe`; this intake does not rename it.

## 2026-10-06 packaging preparation and remaining gates

- The earlier integration branch was subsequently pushed, with user approval,
  to `dunzic/multica-worker` as `codex/integrate-upstream-20261006` at
  `46509903c`. That push did not change main. The preceding intake boundary
  describes what was true when the merge was first verified.
- Packaging preparation now lives on `codex/prepare-packaging-20261006` in the
  same attached worktree, based on that integration tip. It combines the
  community code and the pending distribution patch without changing the
  primary checkout. These preparation changes are not committed or pushed.
- Production defaults are `https://workers.turingomni.com`, WebSocket defaults
  are `wss://workers.turingomni.com/ws`, and release/update/bootstrap downloads
  use `dunzic/multica-worker`. Keep private overrides and development/staging
  targets. Do not rewrite old credentials to a different service. The named
  Windows profile uses the existing program's `--profile turingomni` option;
  there is no renamed program or additional launcher.
- The Windows ZIP producer validates both PE architectures before creating
  output, carries LICENSE/NOTICE, and records binary/archive SHA-256. Offline
  ZIP names end in `-installer.zip` so they do not collide with CLI-only
  release assets. Desktop packaging requires Go before clearing old output
  and compiles each target's embedded CLI; no stale-binary packaging fallback.
- Verification: 51 Node tests (distribution targets, synthetic Windows ZIPs,
  input rejection, and CI scopes); shell installer stub suite; 671 desktop
  tests across 59 files; 21 focused views and 8 web release-source tests;
  non-mobile typecheck (10 tasks) and separate mobile typecheck. The default
  Go wrapper's regular suite and a fresh guarded CLI/update-module rerun
  passed. Database cases may skip without a test database; this step did not
  repeat the earlier database fixture or agent-package suites.
- Full frontend lint still fails on three pre-existing placeholder errors in
  unchanged `packages/views/settings/components/role-sources-tab.tsx`.
  Changed views files passed focused lint; other existing warnings remain.
  Address this separately before claiming an all-green release gate.
- No new native installer was built. Windows PowerShell tests were not run
  locally because no PowerShell host is available; CI retains Windows 5.1/7
  checks. Synthetic PE ZIP tests are not physical Windows installation
  acceptance. No mobile native build, browser E2E, production login, real-agent
  smoke, signing/notarization, vulnerability scan, release upload, or server
  deployment ran during preparation.
- Before public release, review and commit the combined patch, deliberately
  land it on the fork's main, resolve the lint gate, and select an approved
  new release tag (default patch increment). Rebuild CLI and desktop from
  that same clean source, preserve old deliverables before successful packaging
  clears its output, and verify versions, architecture, embedded targets,
  checksums, platform signatures and notarization. Use the fork release
  runbook; pushing a tag publishes and requires separate authorization.

## 2026-10-06 local package delivery

- Eight deliverables are preserved in the primary checkout's ignored
  `dist/installers/20261006-0.6.1-119-g46509903c-dirty/`. Its `SHA256SUMS.txt`,
  `BUILD_QA.json`, and `PACKAGING_NOTES.txt` retain exact artifacts and checks.
  Earlier CLI candidates and an unsigned first Intel Mac build were preserved
  separately, not silently removed or published.
- Versions derive from synchronized Git tags, retaining the dirty suffix rather
  than pretending this uncommitted work is a new tagged release. Each CLI was
  compiled from this source with Go 1.26.6. Windows offline ZIPs contain the
  same final CLI bytes as their corresponding desktop bundles.
- The normal GitHub download host failed. Fixed Electron assets were retrieved
  from the same official repository's API and verified against official SHA-256.
  NSIS/resources/winCodeSign assets matched the installed tool SHA-512 constants.
  These are build dependencies, not community Multica distribution downloads.
- The Mac host lacks x64 execution support. A fixed official Linux x64 build
  image could run its tools, but Wine aborted under QEMU with a page-mapping
  assertion. Final Windows packaging used the installed cross-platform resedit
  library to write the same version/icon metadata, with architecture and all
  non-icon/version resources preserved. Standard NSIS templates and the
  installed native `UninstallerReader` produced installers without executing
  them under Wine. The fixed image digest and temporary inspection scripts are
  recorded in the delivery's notes/build-support; no product shim or host-system
  installation was added. Native Windows acceptance remains required before
  public release or adopting this local workaround as a standard workflow.
- All desktop bundles contain TuringOmni production defaults and the fork's
  GitHub update/bootstrap feeds. Windows PE metadata, icon count, manifest and
  ASAR header integrity were checked. EXE payload application/ASAR/update/CLI/
  license/notice bytes matched the prepared bundle. No nested previous build
  artifacts were found in the Windows ASARs. Both Mac DMGs mounted read-only,
  matched their built apps, and passed checksum/signature checks; both ZIPs
  passed integrity checks. The Apple Silicon CLI version command ran; Intel
  Mac and Windows binaries were not executed on their target OS.
- This is a local test build: both Mac apps are ad-hoc signed, not Developer ID
  signed or Apple-notarized; Windows desktop/CLI binaries are unsigned. There
  was no physical Windows installation, GUI startup, production login,
  browser E2E, real-agent smoke, public online-update test or deployment.
  Existing lint errors and the clean-commit/tag/signing release gates remain.
  The original primary checkout's 45 pending source files were not changed.

## Ledger entry template

```markdown
| YYYY-MM-DD | `<remote>/<branch>` / `<source commits>` | Absorbed, skipped,
deferred, or rejected; resulting local commit and conflict decisions. | Exact
checks run and any explicit verification boundary. |
```
