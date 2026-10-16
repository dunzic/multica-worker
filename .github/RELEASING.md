# Release runbook

## Normal release

Release from a reviewed commit on `main` by creating and pushing a new semantic
version tag such as `v0.18.4`. The Release workflow intentionally has no manual
trigger: a tag push is the only event that can publish binaries and container
images.

Every release tag must be annotated with exactly one `Release-Scope: cli` or
`Release-Scope: full` line. Missing/ambiguous scope fails before publishing.
Use `cli` for a CLI-only release: Docker, Helm and desktop publishing jobs are
disabled for that tag. Use `full` only after separate authorization and all
desktop/mobile/server deployment gates are closed. For example, after approval:

```sh
git tag -a vX.Y.Z -m "Release CLI vX.Y.Z" -m "Release-Scope: cli"
```

Run `node --test scripts/release-scope.test.mjs` when changing this routing.

Before CLI publication, the workflow requires installer regression tests and a
native CLI build/metadata smoke on Linux, macOS and Windows. After publication,
`cli-acceptance` exercises the public one-line installer twice, upgrades a real
historical owned source build, checks an already-current update and verifies
that a synthetic private profile is unchanged. It uses isolated temporary home
directories and does not authenticate or start agents. The first-release upgrade
fixture is commit `73be2734f8e2cd096be5856e385ce8498151c798`, with its actual
Git-derived version; it is not an earlier public Release. Preserve this
distinction in acceptance reports. A published Release is not acceptance until
these jobs pass. These jobs cover their runner CPU only, not every target CPU.

## Fork distribution targets

Production CLI, desktop, and mobile builds default to
`https://workers.turingomni.com`. CLI installation/bootstrap, CLI updates,
desktop updates, and the web download page use only
`dunzic/multica-worker` Releases. Do not publish to the community repository or
Homebrew tap. This fork distributes CLI release archives, not a Homebrew formula.
User-selected private targets and local development/staging remain separate.

Before packaging, run
`node --test scripts/distribution-targets.test.mjs scripts/package-windows-cli.test.mjs`
on a POSIX host with `zip` and `unzip`. Windows CI runs the input validation
tests; the synthetic ZIP tests do not prove that a real Windows CLI installs.
After upstream intake, rerun it to catch restored community defaults. License,
NOTICE, source module imports, and intentional upstream reference links are not
distribution targets and must retain their attribution.

For a local Windows offline CLI package, cross-compile both Windows architectures
to `server/bin/windows-{amd64,arm64}/multica.exe` with the same version/commit/date
ldflags as the desktop bundler. Then run `node scripts/package-windows-cli.mjs`.
The output in `dist/cli/` includes a per-user installer and binary checksums.
Offline archives use `multica-cli-<version>-windows-<arch>-installer.zip`,
distinct from GoReleaser's CLI-only archives. If publishing both together,
merge their entries into the release's complete `checksums.txt`; never replace
the CLI-only checksum manifest with the two offline-package entries.
The executable remains `multica.exe`; use `multica --profile turingomni login`
and `multica --profile turingomni daemon start` to keep credentials isolated.
No renamed executable or alternate launcher is shipped. Run the PowerShell
installer tests on Windows before declaring Windows installation acceptance.

Desktop packaging requires Go and rebuilds the embedded CLI for each target.
The packaging wrapper stops before clearing old output when Go is unavailable;
it must not ship an old binary or defer its provenance to runtime downloads.
Before a successful build, preserve any earlier deliverables separately because
the existing wrapper clears its output directory. CLI and desktop versions are
derived from the same Git tag/commit; do not relabel old binaries as a new build.
Local dirty builds are test artifacts, not release candidates. A public release
requires a reviewed clean commit on this fork's main, an explicitly approved
new tag (default patch bump), and the platform signing/notarization checks.

Local packaging does not publish a GitHub Release. Own release assets and update
metadata must be uploaded before online installers or updates can succeed.
They must fail rather than fall back to community binaries.

### Offline compatibility baseline

`server/internal/buildinfo/distribution.json` is the reviewed distribution
record. It separates the fork's identity, absorbed upstream release and commit,
minimum CLI version, required CLI capabilities, and per-feature API capability
requirements. Update this record in the same reviewed change as upstream intake;
do not query GitHub during a build to guess what has been absorbed. An upstream
release published after intake is not automatically this fork's baseline.

Run `node scripts/check-distribution.mjs` before local packaging. The check uses
only local Git history: the absorbed commit must be an ancestor of HEAD, and the
recorded upstream release commit must be an ancestor of the absorbed commit.
If the upstream release tag exists locally, it must agree with the record. A
shallow/source-export checkout without the required history fails with guidance;
restore the history instead of skipping the check. Desktop packaging, bundled
CLI builds and the Windows offline-package entry point run this check.

For publication, run
`node scripts/check-distribution.mjs --release --version <approved-version>`.
The Release workflow and desktop publishing wrapper require an exact tag at
HEAD, clean source, and a package version no lower than the recorded minimum
(which must itself be no lower than the absorbed upstream release). Semantic
precedence includes prereleases: `0.6.3-rc.1` is below `0.6.3`. Git-describe/dirty
builds are not public release versions. This is a gate, not authorization to
create a tag or upload artifacts. Choose an unused fork release version after
review; never move an imported upstream tag to the fork commit.

`multica version --output json` includes the full build commit, embedded
distribution record and capabilities from the implemented CLI/daemon protocol.
SHA-256 checks precede extraction; release installers and CLI/desktop bootstrap
then check the executable's metadata before replacing an installed CLI. A high
version alone does not compensate for missing capability/source metadata. The
runtime metadata checks are not cryptographic proof of ancestry: ancestry is
checked on the source before publication, and downloads trust the owned release
channel and its checksums. Do not bypass the gate for an old binary without this
metadata; rebuild it from reviewed source.

Desktop accepts a development CLI only when bundled with the exact same
development version and baseline. Development apps/CLIs do not self-update from
public releases. Existing API capability flags remain independent, per-feature
negotiation; this manifest does not globally reject login because a server has a
different marketing version.

### Owned update-channel acceptance

Desktop pins the owned GitHub feed at runtime as well as in packaging. macOS
ARM64 uses `latest-mac.yml`, macOS x64 uses `latest-x64-mac.yml`, Windows x64
uses `latest.yml`, and Windows ARM64 uses `latest-arm64.yml`. Publish each
manifest together with its referenced installer/update archive and blockmap;
one architecture must not overwrite another's channel. Automatic download,
install-on-quit, preference persistence and OS compatibility checks remain in
place; selecting an architecture must not enable downgrades or prereleases.
Local packaging defaults to `--publish never`.

Before public rollout, verify installation from the owned Release, manual and
automatic update from an older owned package, corrupt/missing-asset recovery,
restart, and retained profiles on real supported Windows and macOS hosts.
Unit tests and a locally executed CLI do not establish signed/notarized desktop
update acceptance. The existing mobile/native permission-revocation and CDN
deployment gates in `docs/engineering/permission-revocation-verification.md`
remain separate release blockers; this installer repair does not close them.

Focused offline checks:

```sh
node --test scripts/release-compatibility.test.mjs scripts/distribution-targets.test.mjs scripts/package-windows-cli.test.mjs
bash scripts/install.test.sh
```

Also run `scripts/install.ps1.test.ps1` on Windows, the desktop tests/typechecks,
and guarded Go tests for `internal/buildinfo`, CLI updates and `cmd/multica`.

The verification job runs the Go tests and `govulncheck` before any publishing
job starts. The vulnerability scan is fail-closed by default.

CI and release jobs require Go `~1.26.9`: at least 1.26.9, while accepting
newer 1.26 patches. This security floor is independent of the development
minimum in `server/go.mod`. A plain `1.26.x` can resolve to an older patch
while the `setup-go` version manifest catches up with an official Go release.
When raising the security floor, update all Go workflows and the pinned
Go builder image in `Dockerfile` together so shipped binaries also receive
the fixes.

## Emergency vulnerability-scan bypass

Use the bypass only when `govulncheck` itself or its live vulnerability database
is unavailable, or when maintainers have documented a confirmed false positive
that blocks an urgent release. Never use it to publish a release with an
unresolved reachable vulnerability.

1. Record the reason and maintainer approval in the release issue or pull
   request, and confirm no other release is in progress.
2. In **Settings → Secrets and variables → Actions → Variables**, set the
   repository variable `ALLOW_VULN_BYPASS_FOR_TAG` to the exact release tag,
   for example `v0.18.4`.
3. Re-run the failed Release workflow for that tag. A different tag, an empty
   value, or any typo keeps the scan enabled.
4. Confirm the verification log contains the explicit bypass warning and retain
   the workflow URL in the incident record.
5. Delete `ALLOW_VULN_BYPASS_FOR_TAG` immediately after the release run
   completes. The tag-scoped value prevents a concurrent release with another
   tag from inheriting the bypass.

Every Go binary retains its compiler version in the standard Go build metadata;
use `go version -m <binary>` when auditing a downloaded release artifact.
