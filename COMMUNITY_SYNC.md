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
  batch résumé drafts with original-file and follow-up Issue references;
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

| 2026-10-14 | Own repository `dunzic/aiwork`: `origin/devNew` at `6459e1c6f`, contained by `origin/feat/personal-global-assistant` at `26bc8ebeb`; workspace Chat v2 snapshot `8c056adb2`, batch attachments `4933639fd`, DOCX preview antecedent `0eecaf3c1` | Semantically adapted the workspace group/topic collaboration model, batch-file composer, and protected DOCX body-text preview as `4f87657cc` on `codex/restore-chat-collaboration-20261014`, based on `c3ab9a93e4d337f265ace65be49586b8ce6b670a`. User authorized commit and push to the fork; see publication scope below. No wholesale branch merge, main landing, release, installer or deployment. Rejected the superseded `feat/conversations-platform` schema at `ad74f43f9`; deferred unrelated personal/global assistant, voice and permission-overhaul changes. | Real isolated PostgreSQL fixtures cover upload, message binding, complete daemon claim, multiple agents, transactional completion, cancellation, privacy and cleanup. Four review fixes plus authenticated PDF preview and commit-ordered daemon wakeup are covered by the second-pass checks below. Real Chrome/Codex HR feedback and UI-sent follow-up passed; unrelated full-suite failures remain recorded separately. |

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
7. Migrations 564–575 are additive: no new foreign keys/cascades; each index
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
