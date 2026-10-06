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

## Ledger entry template

```markdown
| YYYY-MM-DD | `<remote>/<branch>` / `<source commits>` | Absorbed, skipped,
deferred, or rejected; resulting local commit and conflict decisions. | Exact
checks run and any explicit verification boundary. |
```
