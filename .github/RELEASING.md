# Release runbook

## Normal release

Release from a reviewed commit on `main` by creating and pushing a new semantic
version tag such as `v0.18.4`. The Release workflow intentionally has no manual
trigger: a tag push is the only event that can publish binaries and container
images.

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
