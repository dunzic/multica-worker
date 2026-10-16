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
- workspace group conversations, participant-scoped messages/files/runs, and
  generic multi-file drafts with original-file and follow-up Issue references;
  business-specific prompts belong to agent/user instructions, not built-in
  actions in the shared composer;
- shared attachment previews in issue descriptions, comments and replies,
  including authenticated Office-to-PDF conversion and copied file links;
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

   Update `server/internal/buildinfo/distribution.json` with the reviewed
   absorbed release, release commit, intake commit, CLI minimum and required
   capabilities. Run `node scripts/check-distribution.mjs` locally. Do not
   derive the compatibility floor from GitHub's live latest release, relabel an
   old binary, or remove a capability check to make packaging pass. Follow the
   offline/publication gates in `.github/RELEASING.md`.

8. Add a row to the decision ledger below. Include both source and resulting
   commits so a later scan can detect rebases and squash merges correctly.

## Decision ledger

| Date | Source | Decision and local result | Verification |
| --- | --- | --- | --- |
| 2026-10-16 | Official `v0.6.3` at `18fa9f728`; owned Releases API returned an empty list | Assessed only; no new upstream merge. Current absorbed baseline remains `v0.6.2` at `7ab92d7622ecd81d179a8d83ae2dea74835a5069`, through `52b26900c51f759b87707b4ea35fa9f141b83b52`. On `codex/fix-cli-installer-20261016`, repair malformed no-release redirects and add a checked-in offline compatibility record plus owned CLI/desktop update gates. Read AuditWorker Platform's updater as a reference only; no edits there. These are uncommitted local changes, not published packages or deployment. | Local Git ancestry and official/owned release checks; installer, semantic-version, compatibility and desktop/CLI regression checks. Full verification and publication/native acceptance boundaries are recorded below. |
| 2026-09-28 | `upstream/main` through `fa5d470ae40a` | Absorbed by merge `4a70ce5dc9cf`; retained fork-specific features during conflict resolution. | Full Go suite, TypeScript typecheck, desktop/core tests, views tests and locale parity, Helm lint/render, Mac arm64 packaging. |
| 2026-09-28 | Hermes development chain `be10b427d`..`4bd1b3344` | Skipped as already equivalent to official squash `138134d34104` on `upstream/main`; combined stable patch ID matched. | Patch-equivalence and ancestry checks. |
| 2026-09-28 | Attachment-viewer chain `f5c91a9e0`..`26e473c10` | Skipped as already equivalent to official squash `6dc6a0b9a2db` on `upstream/main`; combined stable patch ID matched. | Patch-equivalence and ancestry checks. |
| 2026-09-28 | Persistent local-daemon launch `fe09778253d9` | Absorbed as `8db04400e751`; `make daemon` now launches a persistent binary with auto-update disabled, while existing role-source and delivery operator build targets remain intact. | Dry-run command inspection, complete `make build`, real `make multica --version`, and Go suite; process-tree packages were rerun serially in an init-enabled container. |
| 2026-10-06 | `upstream/main`, `fa5d470ae40a`..`b4ca5b4a23e6` (15 commits; v0.6.0/v0.6.1 baseline) | Absorbed by merge `6759f9244` on `codex/integrate-upstream-20261006`, starting from fork `ffe3bbe9b`. No textual conflicts; reviewed overlapping realtime, daemon, task-service, and protocol changes. Retained role-source/DR/replay, delivery reconciliation, SQL/migrations, and persistent operator build targets. Original dirty `main` was not staged, committed, reset, or overwritten; integration is not yet landed there or pushed. | Go default test wrapper; core 2,222, views 5,995, desktop 670 unit tests; whole-workspace TypeScript check; focused PostgreSQL fixture tests for working-agent facets and builtin skills; five-language new-key parity; changed-file lint and build checks recorded below. |
| 2026-10-06 | Legacy builtin skill redirect retirement `b4ca5b4a23e6` | Absorbed with the official baseline, not cherry-picked a second time. Current `multica-platform` skill replaces the old redirect; retained advertised `platform-skill-v1` capability. Do not reintroduce a legacy shim during future intake. Very old daemons whose briefs name the retired skill require an upgrade before deployment. | Database-backed inline and skill-reference claims tested both with and without the capability; skill routing/parser/scoping and fail-closed bundle-read tests passed. |
| 2026-10-06 | Integration tip `46509903c` plus the primary checkout's pending TuringOmni distribution patch | Prepared together as uncommitted changes on `codex/prepare-packaging-20261006`, reusing the attached integration worktree. Kept the executable `multica` / `multica.exe`; omitted the alternate launcher, retained explicit `--profile turingomni` credential isolation, corrected the runtime update feed, and required a fresh Go CLI build for desktop packaging. Original 45 pending files remain unchanged; no new commit, main landing, tag, push, package, deployment, or publication in this preparation step. | Distribution/package/CI-scope Node tests, stubbed shell installer, complete desktop suite, focused views/web tests, non-mobile and mobile typechecks, default regular Go wrapper and fresh guarded CLI/update tests. Exact results and remaining release gates below. |
| 2026-10-06 | Prepared worktree at `46509903c` plus uncommitted distribution changes | User authorized local Mac and Windows packaging. Built `0.6.1-119-g46509903c-dirty`: Mac ARM64/x64 DMG and ZIP, Windows ARM64/x64 desktop EXE, and Windows amd64/arm64 CLI offline ZIP. Delivered under the primary checkout's ignored `dist/installers/20261006-0.6.1-119-g46509903c-dirty/`; no new commit, main landing, push, tag, release upload or deployment. | Twelve packaging/distribution checks rerun; all four desktop bundles' versions/targets/CLI architectures/notices; mounted DMG and ZIP integrity; deep strict Mac signature verification; Windows PE metadata/icon/ASAR-header checks; EXE archive and extracted-payload equality; CLI ZIP/current desktop-binary equality; eight SHA-256 hashes. Signing/native-acceptance limits and workaround below. |
| 2026-10-15 | `upstream/main` at `52b26900c` plus fork candidate `origin/codex/restore-chat-collaboration-20261014` at `036c65a85` | Absorbed as merge `a662d83da` on `main`. Retained the email plugin, private deployment/role-source capabilities, upstream fixes, group/topic collaboration, Office previews, attachment-origin routing, and distribution changes. Because the email and chat additions had not been applied to the primary database and collided with the official migration sequence, renumbered them to `573–584` (chat) and `585–610` (email), then regenerated sqlc and registered concurrent-index retry cleanup. | `pnpm typecheck`; core config/auth tests (198); views chat/Office/link tests (129); Go migration-prefix/email/chat/concurrent-index tests; `make sqlc`; `git diff --check`. Full Go/frontend suites still report the pre-existing role-source symlink, local-storage/navigation, and installer-command baseline failures. |

| 2026-10-14 | Own repository `dunzic/aiwork`: `origin/devNew` at `6459e1c6f`, contained by `origin/feat/personal-global-assistant` at `26bc8ebeb`; workspace Chat v2 snapshot `8c056adb2`, batch attachments `4933639fd`, DOCX preview antecedent `0eecaf3c1` | Semantically adapted the workspace group/topic collaboration model, batch-file composer, and protected DOCX body-text preview as `4f87657cc` on `codex/restore-chat-collaboration-20261014`, based on `c3ab9a93e4d337f265ace65be49586b8ce6b670a`. User authorized commit and push to the fork; see publication scope below. No wholesale branch merge, main landing, release, installer or deployment. Rejected the superseded `feat/conversations-platform` schema at `ad74f43f9`; deferred unrelated personal/global assistant, voice and permission-overhaul changes. | Real isolated PostgreSQL fixtures cover upload, message binding, complete daemon claim, multiple agents, transactional completion, cancellation, privacy and cleanup. Four review fixes plus authenticated PDF preview and commit-ordered daemon wakeup are covered by the second-pass checks below. Real Chrome/Codex HR feedback and UI-sent follow-up passed; unrelated full-suite failures remain recorded separately. |
| 2026-10-14 | Own repository `dunzic/aiwork`: Office preview `0eecaf3c1e9e`, current-modal port `aa599dd5fab5`, copied-file-card fix `a7628d0f9b0c`, slides-to-PDF `fee0d16b8da3` | Additive semantic adaptation committed as `a1bd2d551` on `codex/restore-chat-collaboration-20261014` after `535630ecc44f`; fork publication authorized on 2026-10-15 as recorded below. Keep the upstream viewer and original MD/CSV/JSON/PDF renderers; add private on-demand Word/Excel/PPT PDF rendering, shared across descriptions/comments/replies. Resolve copied references from trusted app/API origins through authenticated attachment ownership checks. Do not revive old browser parsers, synthesize attachment records, rewrite foreign/localhost hosts, create derived public files, or import the old FK/cascade migration/upload goroutines. | See Office preview verification and deployment gates below. No production access, migration, release, installer or deployment in this change. |

## 2026-10-16 CLI distribution repair and acceptance boundary

The owned `releases/latest` redirected to the release listing because there
were no public releases. The shell installer previously treated that entire
Location header as a version. It now rejects listing/malformed/foreign
redirects before downloading and reports the absence of an owned release.
The official `v0.6.3` has CLI assets, but is not an authorized substitute for
this fork's binaries and has not been merged by this repair.

The local distribution manifest records actual absorbed ancestry. Packaging
checks it offline; Go CLI and desktop share its version/capability requirements.
Downloaded CLI bytes must pass checksum and executable metadata checks before
replacement. Desktop bootstrap pins one release for both manifest and archive,
and keeps the working CLI on failure. Desktop automatic updates stay on the
owned architecture-specific channels without downgrade or prerelease opt-in.
Development bundles retain their real version and do not self-update.

Local verification completed on 2026-10-17 (no production traffic):

- Shell installer suite passed, including missing release, malformed/foreign
  redirects, unavailable assets, checksum failure and incompatible executable.
  Downloads and user home directories are test fixtures, not a public install.
- Four Node suites passed 78 tests: offline compatibility/ancestry, distribution
  targets, Windows archive packaging and CI scope/cache wiring.
- Desktop: 62 suites / 714 tests passed; node and renderer TypeScript checks
  (`--composite false`, matching package scripts), changed-file ESLint and
  Electron main/preload/renderer production build passed. Build warnings for
  `::highlight` CSS and a mixed static/dynamic emoji-picker import remain;
  these files were not changed here. Used installed test/tool binaries because
  the pinned pnpm bootstrap was unavailable, not a successful root-wide pnpm run.
- Go 1.26.9, isolated no-network container: complete `internal/buildinfo`,
  `internal/cli` and `cmd/multica` packages passed with `-race`; focused daemon
  update/capability tests passed. The real download/verify/replace path ran
  against a loopback fixture, replacing only a temporary test executable;
  corrupt bytes, mismatched versions and missing capabilities preserved it.
- A native macOS ARM64 development CLI was compiled and its JSON metadata
  executed locally; its public-release compatibility check correctly rejected
  the development version. Windows amd64 CLI cross-compilation and PE inspection
  passed, but the Windows executable was not run. No daemon/login/agent CLI was
  started. Output is confined to ignored `.context/cli-distribution-20261016/`.
- Offline source ancestry check and `git diff --check` passed. PowerShell tests,
  full backend/database suites, real-agent tests, public installation and native
  signed-desktop update tests were not run.

At that preparation checkpoint, no tag, commit, push, release upload,
signing-account change, installer rollout, server deployment, or scheduled
community tracking had been performed.
Local tests are not public installation acceptance. Real Windows PowerShell
installation, signed/notarized macOS/Windows desktop updates, and the prior
mobile/native permission gates remain outstanding. Publishing requires separate
approval after review and the checks in `.github/RELEASING.md`.

### 2026-10-17 review and CLI-only publication authorization

The user authorized review, fixes, regression, main integration and push to the
owned fork, then CLI publication and real one-line install/update verification.
This does not authorize desktop/container publication, server deployment,
mobile release or any change to signing accounts.

Review findings fixed before main integration:

- P1: version tags previously started CLI, desktop and container publishers
  together. Annotated tags now require explicit `Release-Scope: cli|full`;
  all non-CLI publishing jobs require `full`. A failing workflow-routing
  regression was added first and passed after the fix.
- P2: the POSIX stable installer accepted prerelease/leading-zero tags, unlike
  Windows. Added failing cases and aligned stable-only validation before any
  archive download.
- P2: all 12 desktop bundler Node tests broke when the offline gate was added
  without updating their source fixtures. They now exercise the real gate and
  assert missing ancestry prevents compilation. These tests are included in
  POSIX installer CI and the release gate, not only the desktop Vitest suite.

Post-fix local results: 81 Node tests plus 12 bundler Node cases, the shell
installer suite, 714 desktop Vitest cases (including 39 packaging cases), both
desktop TypeScript projects and changed-file ESLint passed. Both workflow YAML
files parsed with resolved job dependencies; offline ancestry and diff checks
passed. The default complete Go wrapper with `--race` passed in a non-root,
no-network Go 1.26.9 container; DB-backed cases skip without a test database.
`go tool govulncheck ./...` completed with no vulnerabilities. An initial root
container run hit privilege-dependent fixture failures, then passed after
using a non-root container; no product sandbox gate was disabled.

The existing main CI is not all green: its database migration/cleanup fixtures,
frontend checks, sqlc drift and self-host script checks have prior failures.
These are not waived as server/full-product acceptance. CLI publication retains
the full default Go/vulnerability checks and adds native CLI metadata and
installer checks on Linux, macOS and Windows before uploading. Online acceptance
then installs from the public main-branch command in isolated profiles, repeats
the install, upgrades the real pre-repair owned build and checks profile
preservation. Its completion must be recorded separately from local mocks.

Select the unused `v0.6.4` fork tag: local `v0.6.3` is an imported upstream tag
and must not be moved. This fork release does not claim to absorb upstream
`v0.6.3`; the recorded source baseline remains `v0.6.2` plus `52b26900c`.

The reviewed repair landed on owned main as `fa0004baacdd250d32c7dfab9d2b1b7c03666182`.
The first `v0.6.4` tag run stopped before publication: checkout's implicit
event-SHA handling replaced the runner's annotated tag with a lightweight ref.
The source/tag themselves were correct; no artifact or image publisher ran.
The follow-up explicitly checks out the tag, verifies its commit against the
event SHA, and adds the checkout contract regression. Preserve `v0.6.4`
unchanged as failed-attempt evidence and retry with the new `v0.6.5` patch tag.

Native Windows CI then exposed CRLF sensitivity in the new workflow-routing
test (not the product installer): literal LF job separators did not recognize
Windows checkouts. Normalize CRLF in that test. The CLI release remains gated
on Windows tests; do not bypass the test or claim publication from the tag.

## 2026-10-15 Permission candidate assessment and authorized adaptation

After the assessment below, the user authorized local adaptation and regression
for both permission candidates, including links embedded in issue descriptions,
comments and replies. The execution plan is
[Permission revocation and embedded attachment links](docs/engineering/permission-revocation-execution-plan.md).
The user also authorized a separate architect agent to review the completed
implementation after required self-tests, followed by fixes and independent
re-review before any merge request. This is an explicit gate in the execution
plan. Independent review identified cross-workspace and topic-scoped delivery
gaps (R1), native-client URL compatibility (R2), and ongoing broad CDN cookie
issuance (R3). The independent final review closed the R1/R3 application fixes
for scoped handoff into controlled platform acceptance, not production release.
The user explicitly limited this iteration to server and
web/desktop work: mobile adaptation is not implemented, and R2 remains a
release blocker, not an accepted compatibility regression.
See the [verification record](docs/engineering/permission-revocation-verification.md)
for exact results, the independently reproduced baseline test failure and native
client/storage deployment gates. After that technical handoff, the user
authorized committing this work, merging it into `main`, and publishing it to
the owned `dunzic/multica-worker` repository. The reviewed implementation is
commit `e869e6197a5824fb8e47af67f79fefae0cc4fc48`; the plan, verification record,
and independent review accompany it in a separate documentation commit.
The Git delivery does not authorize a tag, installer, or deployment and does
not close R2. The source-only assessment below is retained as the intake
baseline, not a passing test record.

| Date | Source | Decision and local result | Verification |
| --- | --- | --- | --- |
| 2026-10-15 | PR 9249 at `f05b5519e4aac8e4dcd396e6ac0e63fe44d85ffe` | Semantically adapted as `e869e6197` on `codex/next-iteration-20261015`: primary-DB send authorization, post-commit revocation, bounded cohorts and relay revalidation. Extend beyond the candidate to per-frame source workspace/group/topic checks, topic-only task lifecycle context, and ID-only self-removal cleanup. Preserve participant-scoped and durable delivery. | Baseline socket failures and two independent replay failures reproduced and fixed; 101 passing realtime/DB race cases. Final tracked diff SHA-256 `1b2932517593d30c4b84e363e4bc6062f81fd55da62bcf7aa19b8e5e7a474a34` and all 13 new server files independently reviewed and matched again before commit; R1 technically closed. |
| 2026-10-15 | PR 8896 at `1801c01ca9fa12eadb9782beec44e77167ad02fa` | Semantically adapted in the same permission change `e869e6197`: user and verified task-token binding; fresh source workspace/group checks for capability, stable and preview paths; private local paths and avatar boundaries. Stop all five broad CDN cookie issuance/renewal paths and expire browser cookies, retaining checked public-avatar signing. Mobile R2 explicitly deferred as a release blocker. | Independent review closed the R3 application change. Final shared frontend 167 passed, login/session/avatar race regression 38 passed, five Go packages 5,369 passed / 0 failed / 71 environment skips, with only the proven baseline manifest failure explicitly excluded; Go vet passed. Cleanup lifecycle instability remains recorded despite later passing. No production CDN or native-client acceptance claimed. |

The fork remote was refreshed before this Git delivery: `origin/main` and local
`main` were both `025e0e40e547c51e8bfc3724bb6788903dad1f70`, allowing a
fast-forward without conflict resolution or changes to the reviewed code.
Only `main` and `codex/next-iteration-20261015` are publication targets;
`upstream`, unrelated branches, dependency caches, installers, and ignored test
logs are excluded. Existing test evidence applies to the identical reviewed
snapshot; this commit/merge step does not claim a new full test run.

Do not reintroduce direct storage signatures or uploader-bound capability URLs
in broadcasts to work around R2. Clearing browser cookies does not invalidate
copied credentials: private storage/CDN policy retirement and mixed-version
rollout remain deployment gates. Preserve source identity on any new
topic-scoped task event; recipient snapshots are routing, not authorization.

The fork at `025e0e40e` contains official `upstream/main` at `52b26900c`;
the freshly fetched ancestry comparison reports zero missing official commits.
The following are open community candidates, not omitted official updates.
This assessment inspected the current source, PR patches and public discussion;
it did not run new permission regressions or inspect the production deployment.

| Date | Source | Decision and local result | Verification |
| --- | --- | --- | --- |
| 2026-10-15 | [Revoke workspace WebSocket access](https://github.com/multica-ai/multica/pull/9249), head `f05b5519e4aac8e4dcd396e6ac0e63fe44d85ffe` | High-priority candidate; not integrated. Existing removal revokes runtime/task access, invalidates membership cache and notifies clients; official `52b26900c` also gates new inbox recipients. These do not replace server-side revocation of already-open browser sockets. Preserve the fork's participant-scoped group delivery when adapting. | Current `hub.go` checks membership at connection authorization but its write pump does not revalidate it; member handlers publish removal without the candidate's socket invalidation. PR is open, authored by a repository collaborator, with no submitted review returned by the API. That is an intake signal, not acceptance or a merge-time promise. Require isolated real WebSocket/DB tests, missed-relay and rejoin cases, and group-privacy regression before integration. |
| 2026-10-15 | [Make attachment download capabilities revocable](https://github.com/multica-ai/multica/pull/8896), head `1801c01ca9fa12eadb9782beec44e77167ad02fa` | High-priority semantic-adaptation candidate; not integrated. The fork already protects authenticated attachment reads and Office previews with workspace/group participation checks. Existing signed-download capabilities are a separate path: their user-unbound v1 token is checked without rechecking membership at redemption. The candidate's workspace-membership check alone is insufficient for this fork's group-removal case; also preserve/revalidate group/topic authorization for the bound principal. | Current capability handler and signing helpers compared with the PR patch; Office revocation test and topic access helper inspected, not rerun. Candidate changes capability format and routes downloads through the API in all storage modes. Require signed-link revocation, group-only removal, cross-workspace, native download/Range and coordinated-version rollout checks. Previously issued CDN preview links and already received bytes have separate boundaries. |

No product source, tests, deployment, installer, commit or push was changed by
this assessment. Public discussion reviewed here does not establish that either
candidate is withheld for commercial reasons. Do not label existing group/Office
fixes as missing, or label these additional revocation paths as already tested.

## 2026-10-15 Generic composer main integration

| Date | Source | Decision and local result | Verification |
| --- | --- | --- | --- |
| 2026-10-15 | Fork `origin/main` at `ff59571ee` and composer correction `bea521a84` | Integrated as merge `efebb9997` on `codex/restore-chat-collaboration-20261014`. Resolved the two composer/test conflicts semantically: retain main's one-click conversation-starter sends and draft/upload guards, while preserving multi-file paste, generic append requests, adaptive height, and removal of HR-only controls. User authorized main integration and pushing all local branches to `dunzic/multica-worker`; no upstream push, force push, tag, installer or deployment is included. | Post-merge views regression: 139 tests across 7 files; core uploads/drafts: 42 tests across 4 files. All non-mobile workspace TypeScript project checks (including both desktop configs), changed-view-file ESLint, and the diff check against `origin/main` passed. |

- These checks used the existing installed dependencies. A frozen-lockfile
  reinstall, production build, full frontend/Go suites, mobile checks, real-agent
  smoke and native installer acceptance were not repeated for this publication.
  The patch relative to the fetched fork main is limited to the composer,
  regression fixtures, translations and this record; backend and migration
  contents remain identical to that main baseline.
- The primary checkout's 43 pre-existing dirty entries were not staged,
  committed or included in the publication. They are separate from the reviewed
  composer correction. Updating that dirty local main checkout requires a
  separate preservation decision; publishing the integrated tip to remote main
  does not require overwriting those local files.

## 2026-10-15 Generic composer correction

User-authorized local correction on `codex/restore-chat-collaboration-20261014`,
based on `036c65a8545fa60517cdb670e951cca2d89bccd1`; no new upstream intake,
commit, push, deployment or installer in this step.

- A three-file clipboard paste was reproduced with three successful uploads
  but only the final file card retained. Inserting an atomic file card left it
  selected; the next sequential insertion replaced it, and draft reconciliation
  discarded the now-unreferenced attachment record. Move the caret into a
  paragraph after each upload placeholder before awaiting upload completion.
  Retain separate paste, picker and drop regressions: successful picker-based
  HR acceptance did not cover this clipboard path. Do not infer production
  storage deletion or recovery from this local reproduction.
- Remove the HR-only action from group and generic composers. Keep generic
  conversation starters and mentions; the HR prompt now lives solely in the
  explicitly gated acceptance fixture. Multi-file work remains available to
  any agent/task without a built-in business workflow.
- Let editor content determine input height, capped by the available chat
  height and the existing 384 px maximum. Keep the toolbar in normal flow and
  scroll long content internally. Group inputs must remain direct children of
  the height-constrained conversation, not an indefinite-height wrapper.
- The existing 20-attachment group message limit is unchanged. A 21-file draft
  retains all files and receives the existing visible send rejection; automatic
  batching and backend-limit changes are not part of this fix.
- The displayed "Untitled" chip is a topic title, not the group title. Group
  creation and the current new-topic action both create an empty topic title;
  the backend rename endpoint has no corresponding frontend control or
  automatic naming. This was diagnosed only; no topic data or naming workflow
  was changed.

Verification: 129 focused views tests and 42 focused core tests passed. The
broader editor/chat run passed 1,947 tests and failed three existing
`chat-message-list.test.tsx` cases missing `NavigationProvider`; the same three
failures were reproduced against the immutable pre-change `036c65a85` source.
Views, web and both official desktop TypeScript targets passed, as did
changed-file ESLint and whitespace checks.

Real Chrome with the shared components, actual editor and synthetic HTTP
upload responses retained 3/20/21 pasted files in both group and compact inputs.
Group sends carried all 3/20 attachment IDs; 21 was rejected without clearing
the draft. Compact input callbacks received all IDs. Height grew from 74.75 px
to 271.25 px for seven lines, capped at 384 px, and shrank after clearing;
small-window toolbar visibility and internal scrolling passed. Evidence lives
in the worktree's ignored `.context/composer-investigation/` directory. These
are browser integration checks with synthetic uploads, not production data,
real-agent HR acceptance, native Windows/macOS acceptance or a release.

## 2026-10-14 Office preview restoration

### User-visible scope and constraints

- Word (`.doc`, `.docx`), Excel (`.xls`, `.xlsx`) and PowerPoint (`.ppt`,
  `.pptx`) open as PDF in the existing viewer. This replaces the recently
  restored DOCX body-text-only **UI**, not the existing `/content` text API.
  Existing MD, CSV, JSON, PDF, text, HTML and media renderers stay in place.
- Descriptions, uploaded comment files and threaded replies use the same
  `Attachment` dispatcher. Canonical `/api/attachments/<uuid>/download` links
  copied into text or file cards open the authenticated preview page, including
  when that comment has no attachment metadata of its own. Only relative or
  same-deployment links qualify; external URLs are not silently rehosted.
- Preview remains read-only. Original-file download is unchanged. Layout and
  fonts can differ from Microsoft Office; slides do not play animations or
  embedded videos. Each spreadsheet sheet is one PDF page, including hidden
  sheets, regardless of print ranges. The viewer explicitly states this.
  Large sheets may require zooming; this is not an editable spreadsheet grid.
- Source files are limited to 20 MiB, derived PDFs to 40 MiB, OOXML expanded
  entries to 100 MiB/4,096 entries, and a conversion to 60 seconds. Macro-enabled
  filename extensions are not enabled. Damaged/encrypted/unsupported documents,
  service congestion and missing server configuration have explicit errors;
  retry and original download remain available. A transient converter failure
  gets one automatic retry after 500 ms, never an unbounded retry loop.

### Privacy, resource limits and deployment

- `GET /api/attachments/{id}/office-preview` checks workspace membership and
  the original attachment's group/topic ACL before reading storage. It sends
  stored bytes, not user-supplied URLs, tokens or cookies, to an operator-owned
  conversion service. Every attempt rechecks permissions, with `no-store`
  responses and no persistent PDF cache or derived attachment record. Closing
  the viewer discards Query bytes and revokes its blob URL. Revocation cannot
  retroactively erase a document a user has already read/downloaded.
- There are no database migrations, new browser-parser dependencies, or calls
  to public Microsoft/Google online viewers. Two conversions per backend
  process may be in flight; excess requests fail visibly instead of forming
  an unbounded queue. Conversion receives cancellation and a deadline.
- Optional `docker-compose.office-preview.yml` adds pinned Gotenberg 8.37.0
  on an internal network, without host ports, source-storage mounts or app
  credentials. It limits CPU/memory/PIDs and denies external document fetches,
  API download-from, Chromium routes and webhook destinations. Do not expose
  the converter directly through the public reverse proxy or loosen those
  network restrictions to fix a connectivity issue.
- First build/select **this fork's backend and frontend images containing this
  change**, using the existing `MULTICA_BACKEND_IMAGE`,
  `MULTICA_FRONTEND_IMAGE` and `MULTICA_IMAGE_TAG` settings. The base compose
  file's community-image defaults do not contain this fork-only endpoint.
  Then, with the existing production secrets/configuration supplied securely:

  ```bash
  docker compose -f docker-compose.selfhost.yml \
    -f docker-compose.office-preview.yml up -d
  ```

  The overlay sets backend `MULTICA_OFFICE_PREVIEW_URL=http://office-preview:3000`.
  A non-Compose operator may configure an equivalent private HTTP(S) origin
  with the same isolation and resource limits; do not point it at a public
  third-party conversion service. Empty configuration disables Office
  conversion while leaving original downloads and other preview formats intact.
  Existing files are converted on demand; re-upload/backfill is unnecessary.
- Deploy the backend/converter before the new frontend. Already-installed
  desktop clients retain their existing behavior with the newer API; the new
  shared viewer needs a new desktop build if it is to appear there. Web users
  need a web deployment, not a CLI reinstall. No installer was built here.
- To disable: remove this overlay from the operator's compose invocation,
  clear any separately set `MULTICA_OFFICE_PREVIEW_URL`, recreate the backend
  using its base deployment configuration, and stop the dedicated converter.
  Do not delete upload volumes or databases. Office falls back to an explicit
  unavailable notice plus the unchanged original download, not a public viewer.
- Conversion options and hardening were checked against the primary
  [LibreOffice conversion documentation](https://gotenberg.dev/docs/convert-with-libreoffice/convert-to-pdf),
  [configuration reference](https://gotenberg.dev/docs/configuration) and
  [outbound URL filtering reference](https://gotenberg.dev/docs/outbound-url-filtering).

### Office verification and release gates

- Targeted core API tests and shared editor/rich-content/comment/preview-page
  tests cover PDF response validation, disabled/error states, bounded retries,
  blob cleanup, workspace separation, preserved original preview formats and
  copied-link/card routing. Initial feature checks: 153 core API tests; 1,738 shared views
  tests in 97 files (`editor`, `rich-content`, attachment preview page and
  comment card); `pnpm typecheck` with 10 successful non-mobile tasks; changed
  core/views lint; E2E TypeScript check; five-language parity (nine new keys
  each); Compose configuration validation and `git diff --check` all passed.
  These are scoped tests, not a rerun of the entire monorepo test suite.
- Go tests use the dedicated managed PostgreSQL test database and fake
  converter for deterministic ACL/revocation/size/invalid-input/error tests.
  Separate real-converter acceptance uses synthetic OOXML and existing
  synthetic résumé fixtures, never the user's production documents.
  Seven converter and ten handler top-level tests passed, including bounded
  concurrency, request cancellation, response-body timeout, zip expansion,
  original content compatibility and revoked group participation. Converter
  `go vet` passed. No SQL changed, so sqlc regeneration was not needed.
- `MULTICA_OFFICE_PREVIEW_ACCEPTANCE=1 pnpm exec playwright test
  e2e/office-preview.spec.ts` is opt-in and checks for a loopback API and an
  isolated `multica_*` database. Start the managed API/web environment and
  isolated converter first. The local acceptance helper uses a loopback-only
  ingress proxy to the converter's internal network; the converter itself has
  no external network. TestApiClient owns setup and exact fixture cleanup.
- Real Chrome acceptance covers DOCX body links and copied comment links/cards,
  XLSX comment attachments and two-page PPTX replies, plus original-file byte
  equality. The spreadsheet fixture includes a hidden sheet and Chinese text.
  An initial converter process crash after cancellation was observed; the
  bounded recovery path and repeated/cancelled conversion regression cover
  this failure mode. A converter failure must never be mistaken for success.
  Initial real-converter Chrome run: **3 passed** against a freshly
  restarted API built from the final Go source. It includes MD/CSV/JSON/PDF
  regression and twelve further PPT conversions alternating with cancellation.
  PDF assertions wait for the native viewer's nonzero page count, not only an
  iframe or plugin shell. Screenshots were visually checked; local evidence
  is under `.context/office-preview-acceptance-complete/` (ignored, no real
  customer content). Earlier failed locator checks are not counted as passes.
- Modern DOCX/XLSX/PPTX are the live acceptance fixtures. Legacy DOC/XLS/PPT
  have allowlist/signature/unit coverage, not native Office format acceptance.
  Native Windows/macOS desktop, mobile, complex customer Office documents,
  production deployment and real-agent HR screening are not re-accepted by
  these tests. Earlier HR results below describe a different completed run.

### Review fixes: trusted origins and attachment ownership

- Attachment links trust the app origin, the connected API base URL and the
  server's additive `attachment_api_origin` config field, derived from the
  same `MULTICA_PUBLIC_URL` used for markdown links. The daemon endpoint is
  deliberately not an attachment-origin authority. Spoofed hosts,
  credential-bearing URLs and arbitrary external deployments remain excluded.
  Both native anchor hrefs and editor/hover-card/copy-card navigation use this
  rule; missing config clears the prior deployment's origin.
- `GET /api/attachments/{id}/preview-location` resolves the stored workspace
  after the existing download membership and group/topic ACL checks. It returns
  only workspace ID/slug with `no-store`; task tokens and cloud machine PATs
  cannot inherit the owner's cross-workspace access. Header/query workspace
  hints do not override ownership. The preview page uses the platform adapter
  to switch workspace before fetching metadata/content; unresolved file cards
  do not prefetch metadata in the wrong workspace. Original download endpoints
  and their authorization behavior are unchanged.
- Regression tests first reproduced the independent-origin failure and the
  premature metadata request. Final checks: **1,751 views tests / 97 files**;
  **360 core API/schema/config/auth-initializer tests / 4 files**; **60 handler
  and 7 converter top-level Go tests**; converter `go vet`; `pnpm typecheck`
  (**10 successful non-mobile tasks**); changed core/views lint; E2E TypeScript
  check and `git diff --check` passed. This is scoped regression coverage,
  not the entire repository's test suite.
- Final real Chrome run: **4 passed**. It uses the actual backend-returned
  absolute markdown URL with distinct web/API origins, covers Office files in
  bodies/comments/replies, copied links/cards across two workspaces, misleading
  workspace query hints, denial after removing membership, original download
  preservation, MD/CSV/JSON/PDF and repeated/cancelled PPT conversion.
  Synthetic-only evidence is retained under
  `.context/office-preview-routing-acceptance/`; cross-workspace preview and
  denial screenshots were visually checked.
- The acceptance setup must emit protected `/api/attachments/.../download`
  links, not public `/uploads/...` storage URLs. The spec asserts this
  prerequisite instead of manufacturing a relative URL. With managed local
  storage, start the isolated environment with `LOCAL_UPLOAD_BASE_URL=/`
  (normalized by LocalStorage to no public base) and the loopback test
  converter URL. Earlier public-storage/locator failures are not passes.
  External/public storage-link resolution is not widened by this change.
- Deploy the new backend before the new viewer: its copied-link page needs
  `preview-location`, and Office conversion still needs the private converter.
  No production deployment or installer was performed. Git publication is
  recorded separately below. Native desktop/mobile and real customer
  documents remain outside this acceptance.

### Authorized Office preview branch publication (2026-10-15)

- Implementation, deployment configuration and tests are committed as
  `a1bd2d55111241241b942e890ee49a8fb0c2f343`
  (`feat(attachments): add private Office previews and safe copied-link routing`),
  based on `535630ecc44f42d37a4add9e930f1209b7a87b2b`. This documentation
  follow-up records provenance, review fixes, acceptance and release gates.
- The user authorized commit and push to our own repository. The verified
  destination is `origin`, `git@github.com:dunzic/multica-worker.git`, branch
  `codex/restore-chat-collaboration-20261014`. The remote branch matched the
  parent before committing. Use an explicit non-force push of this branch and
  compare the remote SHA with local HEAD at handoff. The community `upstream`
  repository is not a publication target.
- Pre-commit repeat checks passed: **360 core tests / 4 files**, **159 focused
  preview/link views tests / 5 files**, and the staged whitespace check.
  The broader regression and four real Chrome acceptance results above are
  from the preceding implementation/review stage; they were not rerun during
  this Git-only publication step.
- Only this worktree's Office preview source, synthetic test fixtures, tests,
  deployment configuration and decision record are included. `.env.worktree`,
  `.context` evidence, credentials, daemon profiles and installation artifacts
  remain excluded. The primary checkout's 43 unrelated pending distribution
  files remain untouched. Publishing this branch does not land `main`, deploy
  the converter/backend, build an installer or publish a release.

## 2026-10-14 collaboration restoration and future intake gates

### Scope and user workflow

- Web and desktop share the group page at `/{workspace}/chat/groups`, linked
  from Chat. Create a group with people and agents, add topics, invite/remove
  participants, promote an admin, rename or leave the group. Only admins
  manage membership; the last admin must hand over before leaving.
- Existing single chat and the assistant popover retain their current API
  and drafts. Their file picker already had `multiple`; the screenshot alone
  does not establish the production single-file cause. Added visible selected
  file counts and a user-inserted batch-screening action which appends to the
  draft without replacing résumé links or pending uploads.
- A group message may contain up to 20 attachments. The UI explains the cap
  and preserves oversized drafts. Mentioning group agents starts one run per
  distinct agent; mentioning people is ordinary shared conversation content.
  An agent reply is delivered by task completion, not by recursive dispatch of
  mentions in its output.
- The batch action requests the JD if missing, per-file evidence, missing
  information, original-file preview links, processed/failed totals, a parent
  Issue and one child Issue per résumé in the current workspace. This is an
  explicit Agent instruction, not a deterministic screening engine or an
  automatic task creator on upload. HR makes final hiring decisions.
- PDF and other existing supported previews remain available. DOCX adds a
  bounded, protected body-text preview (2 MiB archive/document limit), not a
  full Office renderer, OCR or guaranteed parsing of every résumé format.
  Unsupported files must be downloaded or reported as unprocessed.

### Non-negotiable integration invariants

1. Keep the platform name `Multica`, executable `multica`, existing TuringOmni
   production defaults and the fork's installer/update targets. Do not
   replace current role-source, channel-delivery, runtime or private-agent
   authorization flows with the older branch's versions.
2. New participant, message, topic and run queries are workspace-scoped.
   Validate UUID inputs, workspace membership, group participation, agent
   visibility and invocation permissions. Canonicalize UUIDs before principal
   deduplication; duplicated creators must not overwrite the admin role.
3. Message, all attachment bindings and all requested runs commit together.
   Sending and participant removal serialize on the conversation row. Leaving
   cancels the sender's active runs; removing an agent cancels its group runs.
   Claims use the pinned trigger message, not whichever message is newest.
4. API file access and realtime payloads are participant-scoped, including
   draft uploads. Recipient queries also require current workspace membership.
   Group file uploads refuse declared public remote storage without a signed
   CDN; raw local-upload URLs are denied for topic files. If attachment metadata
   cannot be saved, do not return a raw storage link. Operators must still
   verify their bucket/CDN is genuinely private; an API cannot revoke an
   externally public bucket.
   Reject non-canonical raw local paths before the ACL lookup. Match storage
   paths independently of the configured public origin and case-fold the
   lookup for case-insensitive filesystems. Legacy task history, snapshots and
   transcript APIs must apply topic participation too, before pagination/latest
   selection; user PATs do not inherit dedicated daemon execution authority.
5. Task completion, reply creation and outbound attachment binding share the
   terminal-transition transaction, so duplicate callbacks cannot duplicate a
   reply. Provider-session reuse is topic/agent/runtime scoped. Recent topic
   context is bounded to 20 messages (2,000 runes each). Managed-environment
   provenance and task markers both carry topic ownership; Codex/Hermes session
   stores and local worktree ownership distinguish topics from DMs/issues.
   Resume requires the same workspace, agent and topic. Additional upstream
   rollout-recovery features and automatic ZIP extraction are not ported.
6. TanStack Query owns group API state; keys include workspace IDs. WebSocket
   refreshes for another workspace mark its cache stale without refetching
   under the current workspace's headers. Group drafts and upload ownership
   follow the document held by the editor even during topic switches.
   A topic from another conversation must not enable its queries or composer;
   pin the default topic in the URL so activity reordering cannot retarget a
   draft. After the send transaction commits, call `NotifyTaskEnqueued` for each
   created task to invalidate empty-claim state and wake its daemon.
7. Migrations 573–584 are additive: no new foreign keys/cascades; each index
   has its own single-statement `CONCURRENTLY` migration. History projection
   preserves empty/archived sessions and does not repoint existing/in-flight
   tasks. A unique legacy session ID prevents duplicate topic backfills.
   Do not add dual writes to the old and group endpoints.
8. Workspace teardown explicitly removes the collaboration graph and keeps
   other workspaces intact. Every newly owned table must be classified in the
   deletion manifest and covered by database fixtures.

### Initial restoration verification and release boundary

- Reused cached dependency installations and cached Go/sqlc images because
  host Go was unavailable and the pinned pnpm bootstrap/offline install could
  not complete. No package versions or lockfile were changed to bypass this.
- The managed environment script created a dedicated test database,
  `multica_multica_chat_20261014_hrdmfb_389`, on the existing shared PostgreSQL.
  The primary database and production service were not migrated. Full migration
  up through 575 succeeded; backfill rerun/empty/archive/in-flight preservation
  is tested using temporary tables.
- Core full suite passed 2,237 tests, including cross-workspace collaboration
  refresh and existing queued-chat promotion. Views passed
  6,005 tests in 477 files with four workers when explicitly excluding the
  unchanged `onboarding/steps/cli-install-instructions.test.tsx`. The unexcluded
  run fails that Windows installer-command expectation; it is not an all-green
  result. Initial high-concurrency role-source timeouts passed on rerun.
- All affected non-mobile typechecks passed: core, views, web (after its normal
  Fumadocs generated types step), and desktop node/renderer using the manifest's
  `--composite false` setting. Changed-file lint, five-language new-key parity,
  sqlc regeneration and whitespace checks passed.
- Default Go tests ran behind `go-test-with-agent-cli-guard.sh`. Full service,
  migrations, daemon/execenv/processtree/repocache and server command suites
  passed. Fifteen focused handler collaboration tests and DOCX tests passed
  against PostgreSQL, including old-daemon rejection and concurrent removal.
  The full handler suite has one remaining failure:
  `TestWorkspaceDeletionManifestCoversPublicSchema`, for the existing
  `role_source_runtime_attestation_observation` classification and ten missing
  legacy plugin tables. The three new collaboration tables are classified;
  this unrelated schema/manifest discrepancy was not hidden or patched away.
- No browser E2E, real Agent smoke, actual JD/résumé screening, task-link
  business acceptance, Windows/macOS native acceptance or new installer build
  ran. Before release, use sanitized JD/resumés to verify every file's outcome,
  per-file child Issue and original preview, mixed-member collaboration,
  failure/retry, and nonparticipant access denial with the real service.
- Deploy updated backend migrations/API before exposing group UI, and upgrade
  the daemon/CLI with topic-task support. Claims require `group-topics-v1`;
  older daemons receive no group-task payload and the run fails with explicit
  upgrade-and-resend guidance instead of executing as a generic issue task.
- Application rollback must hide/revert the group routes and keep the additive
  tables/data. Data-bearing `down` files deliberately refuse destructive
  removal; concurrent index downs can remove indexes independently. Back up
  before migration and do not roll all migrations down blindly. Recover a
  failed concurrent index build by inspecting/removing only its invalid index,
  then rerunning its migration; preserve successful migration records.

### Review fixes and second-pass verification (2026-10-14)

- Fixed all four review findings on the same uncommitted restoration branch:
  raw-storage path aliases bypassing group privacy; legacy task-list/snapshot/
  transcript exposure; cross-conversation topic URLs enabling the composer;
  and group follow-ups losing managed workdir/provider-session ownership.
  Regression tests cover active/removed/nonparticipant callers, pagination,
  latest-visible-task selection, path/case/origin aliases, pending/foreign topic
  selection and positive/negative conversation ownership proofs.
- Real-browser checks exposed two additional integration gaps: private PDFs
  bypassed the authenticated media loader, and topic task enqueue omitted the
  post-commit daemon wakeup. PDF preview now uses the existing authenticated
  blob loader; a component regression verifies the loading gate. Enqueue now
  wakes only committed tasks; a PostgreSQL test checks committed visibility and
  absence of wakeups after rollback.
- Final repeated frontend scopes: Core **2,237 / 173 files**; Views chat/editor
  **1,900 / 111 files**, including **80** preview-modal cases. Shared Views
  typecheck and changed-file lint passed; earlier in this review pass core,
  web and both desktop typechecks also passed. No mobile or native Windows test.
- Repeated Go scope: `./internal/handler ./internal/daemon/...
  ./internal/service ./internal/migrations ./cmd/server`, behind the normal CLI
  guard, with the dedicated database. **8,855 test/subtest passes, 73 skips,
  one failure**: the pre-existing workspace-deletion manifest drift listed
  above. New privacy, wakeup and resume regressions passed. Full evidence is
  retained at `.context/hr-uat/go-regression-final.jsonl`; this is not an
  all-green backend suite. `make sqlc` could not resolve the uncached generator
  with `GOPROXY=off`; regeneration succeeded using the cached official
  `sqlc/sqlc:1.31.1` image with networking disabled. `git diff --check` passed.
- Permission scope: the user explicitly authorized reading only the specified
  local `multica-backend-1` database connection configuration for the dedicated
  test database. Credentials stay in ignored local environment files and are
  not included in reports. The shared/production database was not migrated or
  used for test fixtures.
  The managed `make status` command can echo a credential-bearing database URL
  when reporting an unknown database state: redact connection URLs before
  displaying or retaining that output. Do not rotate shared credentials without
  separate authorization.
- Live HR acceptance is gated by `agentintegration`,
  `MULTICA_RUN_REAL_AGENT_SMOKE=1`, and `MULTICA_HR_ACCEPTANCE=1` in
  `TestHRBatchResumeAcceptance`. It requires this checkout's managed registry
  and isolated environment; the Playwright test rejects non-local API/database
  hosts and the default database. PDF/Word skills were used to validate three
  fictional source documents before upload. This is not real hiring data or
  an automated hiring decision. Results and screenshots are saved per run under
  `.context/hr-uat/acceptance/`; failed and interrupted attempts remain separate.
  The first complete Claude attempt read the files but returned a plan instead
  of linked outcomes, so it failed business acceptance. Codex results follow;
  no provider-independent success is implied.
- Verified in a real Chrome page with the real local API: a single multi-select
  retains all three files when inserting the HR batch prompt; protected Word
  body text loads; protected PDF renders (visual evidence:
  `.context/hr-uat/acceptance/2026-10-14T06-55-12-940Z/last-page.png`);
  all three original downloads exactly match the PDF/DOCX fixtures.
- Codex attempt `2026-10-14T07-08-28-928Z` really created **one parent and three
  child issues**, each child retaining its corresponding original-file link.
  It was still running when the harness's initial six-minute run deadline
  expired, so it was cancelled and this attempt **did not pass**. Issue/message
  snapshots are in that attempt's `runs-and-messages.json`. No final reply or
  second-turn session/workdir reuse was accepted from this partial run.
- The longer-window rerun `2026-10-14T07-17-14-202Z` failed at the model provider
  with `exceeded retry limit, last status: 429 Too Many Requests`. The error
  reached the topic run as `failed`; the test did not turn it into a success.
  See `.context/hr-uat/hr-batch-codex-acceptance-extended.log` and the attempt's
  `failure.txt` / `runs-and-messages.json`. **That attempt was incomplete**:
  final feedback return and real follow-up/session reuse were still missing.
  The supplemental acceptance below closes these gates for the tested Codex
  runtime. Default fake-provider resume tests do not satisfy live acceptance.
- Each normally completed acceptance attempt removes its dedicated workspace
  through `TestApiClient`; the latest test also verifies the workspace row is
  gone. The acceptance daemon is stopped in the gated runner cleanup. Managed
  API/web processes are stopped at handoff; the dedicated database/profile and
  ignored evidence remain for reproducibility. No real résumé or hiring
  decision was used.
- No commit, push, primary-checkout landing, production deployment or installer
  was performed in this review pass. Backend/daemon rollout remains necessary
  before the restored group UI is released.

### Supplemental real HR acceptance (2026-10-14)

- Scope: rerun the two outstanding gates after the provider's `429`: final
  feedback returning to the group page and a real follow-up in the same topic.
  The same restoration worktree, dedicated local database and three fictional
  PDF/DOCX fixtures were used; no production records or account settings changed.
- Strengthened `e2e/hr-batch-resumes-live.spec.ts`: wait for actual rendered
  feedback; send the second turn through the editor's `@` picker and Send button
  instead of posting directly to the API; verify exactly one parent plus three
  child issues; verify follow-up message visibility before and after reload;
  and compare the complete issue-ID set to reject duplicate creation. Failure
  evidence now includes the page accessibility snapshot before cleanup.
- Attempt `2026-10-14T07-26-56-994Z` completed the real first turn and displayed
  all three résumé results and task references. It then failed in the test
  locator: the member button's accessible name includes the avatar/presence
  prefix. The screenshot shows the correct `@` candidate. The locator was fixed
  without changing product code or bypassing the picker; this attempt is kept
  as a failed run, not counted as a full pass.
- **Passed** attempt `2026-10-14T07-31-51-935Z`: real Chrome + API + Codex,
  one Playwright scenario passed in **5.4 minutes**; gated Go runner
  `TestHRBatchResumeAcceptance` passed in **332.06 seconds**. Both task runs
  completed; no provider `429` occurred in this attempt. This verifies this
  run, not a permanent resolution of provider throttling or other providers.
- Verified: three-file single selection; HR batch prompt preserving all files;
  authenticated DOCX body text and PDF preview; all three original downloads
  byte-identical to fixtures; one parent and three linked child issues; final
  per-file feedback and original-file references visible on the group page.
  The UI-sent follow-up correctly identified **Candidate A / 120 pytest API
  checks**, returned the same original attachment ID, and remained visible
  after reload. Database assertions proved **session ID and workdir reuse**
  and **no additional issues**. These are real-provider results, not mocks.
- Evidence: `.context/hr-uat/hr-batch-supplement-20261014-ui-mention-rerun.log`
  and `.context/hr-uat/acceptance/2026-10-14T07-31-51-935Z/` containing
  `result.json`, `runs-and-messages.json`, `04-feedback-and-links.png`, and
  `05-follow-up.png`. These ignored local artifacts contain synthetic test
  data. The workspace is deleted after assertions, so its links are not
  durable demo links; retain screenshots/JSON as acceptance evidence.
- Cleanup readback confirmed zero `hr-acceptance` workspace rows. The managed
  API/web/daemon were stopped, and API/web ports no longer responded. The
  dedicated database, profile and local evidence remain for reproducibility;
  the synthetic workspace can be recreated by rerunning the gated test.
- Supplemental static checks passed: ESLint using the repository's existing
  Views config, strict TypeScript checking of the E2E entrypoint, and
  `git diff --check`. This rerun did not repeat the entire earlier regression
  suite or clear its pre-existing workspace-deletion-manifest failure. No
  mobile/native installer acceptance, commit, push or production deployment.

### Authorized branch publication (2026-10-14)

- Implementation and tests are committed as `4f87657cc`
  (`feat(chat): restore groups and batch resume review`). This follow-up
  documentation commit records source provenance, acceptance evidence and
  remaining release boundaries.
- The user authorized commit and push to the fork. The verified destination
  is `origin`, `git@github.com:dunzic/multica-worker.git`, branch
  `codex/restore-chat-collaboration-20261014`. Use an explicit non-force push
  of this branch and compare the remote SHA with local HEAD at handoff.
  `upstream` is the community repository and is not a publication target.
- Pre-commit checks added fixture-local Git binary attributes for PDF/DOCX
  and removed a trailing blank SQL line. The three accepted fixture SHA-256s
  stayed unchanged. `make sqlc` still could not resolve its uncached module
  with `GOPROXY=off`; the cached official `sqlc/sqlc:1.31.1` image regenerated
  successfully with networking disabled and produced zero generated changes.
  The staged whitespace check passed after these non-behavioral changes.
- Only this worktree's collaboration source, migrations, synthetic fixtures,
  tests and record are included. Credentials, `.env.worktree`, `.context`
  evidence, daemon profiles and installation artifacts are excluded. The
  primary checkout's unrelated pending distribution changes remain untouched.
  Publishing this branch does not land `main`, deploy services, create a
  release or build an installer; earlier verification limits still apply.

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
