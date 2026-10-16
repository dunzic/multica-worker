import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { checkDistribution, manifestPath } from "./check-distribution.mjs";
import { compareVersions, isDevelopmentVersion, requireReleaseVersion, validateBundledCliReport, validateCliReport, validateManifest } from "./release-compatibility.mjs";

const readJson = (name) => JSON.parse(readFileSync(new URL(`../server/internal/buildinfo/${name}`, import.meta.url), "utf8"));
const baseline = readJson("distribution.json");
const report = () => ({ version: baseline.minimum_cli_version, build_commit: "a".repeat(40), distribution: structuredClone(baseline), capabilities: [...baseline.required_cli_capabilities] });

for (const entry of readJson("semver-cases.json")) {
  test(`semantic precedence ${entry.left} / ${entry.right}`, () => {
    if (entry.invalid) assert.throws(() => compareVersions(entry.left, entry.right), /semantic version/);
    else assert.equal(compareVersions(entry.left, entry.right), entry.result);
  });
}

test("a candidate carries distinct package version, upstream baseline and actual capabilities", () => {
  assert.equal(validateCliReport(report(), baseline).version, baseline.minimum_cli_version);
  const candidate = report();
  candidate.version = "9.0.0";
  assert.doesNotThrow(() => validateCliReport(candidate, baseline, "v9.0.0"));
  candidate.capabilities = [];
  assert.throws(() => validateCliReport(candidate, baseline), /missing required capability/);
});

test("old, mismatched, community and incomplete CLI reports cannot replace the current fork", () => {
  for (const mutate of [
    (x) => { delete x.distribution; },
    (x) => { delete x.build_commit; },
    (x) => { x.version = "0.1.0"; },
    (x) => { x.distribution.distribution = "multica-ai/multica"; },
    (x) => { x.distribution.upstream.version = "0.1.0"; },
    (x) => { x.distribution.upstream.commit = "unknown"; },
  ]) {
    const candidate = report();
    mutate(candidate);
    assert.throws(() => validateCliReport(candidate, baseline));
  }
  assert.throws(() => validateCliReport(report(), baseline, "v99.0.0"), /does not match release/);
});

test("prerelease and development builds are not relabeled as stable releases", () => {
  assert.throws(() => requireReleaseVersion(`${baseline.minimum_cli_version}-rc.1`, baseline), /below the recorded minimum/);
  const candidate = report();
  candidate.version += "-135-g73be2734f-dirty";
  assert.throws(() => validateCliReport(candidate, baseline));
  assert.doesNotThrow(() => validateBundledCliReport(candidate, baseline, candidate.version));
  assert.throws(() => validateBundledCliReport(candidate, baseline, "9.0.0-1-gabcdef"), /does not match/);
});

test("recorded minimum cannot be lower than the absorbed upstream version", () => {
  assert.throws(() => validateManifest({ ...baseline, minimum_cli_version: "0.1.0" }), /below the absorbed upstream/);
});

test("untagged and prerelease-based development builds stay out of public updates", () => {
  for (const version of ["0.0.0-gabcdef", "0.6.3-rc.1-7-gabcdef", "0.6.3-dirty+local.1"]) {
    assert.equal(isDevelopmentVersion(version), true);
    assert.throws(() => requireReleaseVersion(version, baseline), /development/);
  }
  assert.equal(isDevelopmentVersion("0.6.3-rc.1"), false);
});

test("offline packaging verifies ancestry, exact release tag and clean source without GitHub", () => {
  const cwd = mkdtempSync(join(tmpdir(), "multica-offline-baseline-"));
  const git = (...args) => execFileSync("git", ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", ...args], { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  try {
    git("init");
    writeFileSync(join(cwd, "source.txt"), "reviewed upstream source");
    git("add", ".");
    git("commit", "-m", "upstream fixture");
    const commit = git("rev-parse", "HEAD");
    const manifest = structuredClone(baseline);
    manifest.upstream.commit = commit;
    manifest.upstream.release_commit = commit;
    const path = join(cwd, manifestPath);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, JSON.stringify(manifest));
    git("add", ".");
    git("commit", "-m", "fork baseline record");
    git("tag", "v9.0.0");
    assert.doesNotThrow(() => checkDistribution({ cwd, release: true, version: "9.0.0" }));
    assert.throws(() => checkDistribution({ cwd, release: true, version: "0.1.0" }), /below the recorded minimum/);
    assert.throws(() => checkDistribution({ cwd, release: true, version: "9.0.1" }), /tag .* missing/);
    git("tag", `v${manifest.upstream.version}`); // Deliberately wrong upstream tag.
    assert.throws(() => checkDistribution({ cwd }), /does not match the local release tag/);
    git("tag", "-d", `v${manifest.upstream.version}`);
    writeFileSync(join(cwd, "source.txt"), "unreviewed change");
    assert.throws(() => checkDistribution({ cwd, release: true, version: "9.0.0" }), /clean reviewed checkout/);
    const sideCommit = git("commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "not merged");
    manifest.upstream.commit = sideCommit;
    writeFileSync(path, JSON.stringify(manifest));
    assert.throws(() => checkDistribution({ cwd }), /not contained in HEAD/);
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});
