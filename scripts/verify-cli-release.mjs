#!/usr/bin/env node
// Explicit online acceptance only. No login, daemon start, or real agent use.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { validateCliReport } from "./release-compatibility.mjs";

const [tag, previousCli] = process.argv.slice(2);
if (!/^v\d+\.\d+\.\d+$/.test(tag ?? "") || !previousCli) {
  throw new Error("Usage: node scripts/verify-cli-release.mjs vX.Y.Z /absolute/path/to/previous-owned-cli");
}
const baseline = JSON.parse(readFileSync(new URL("../server/internal/buildinfo/distribution.json", import.meta.url), "utf8"));
const expectedCommit = execFileSync("git", ["rev-parse", `${tag}^{commit}`], { encoding: "utf8" }).trim();
const root = mkdtempSync(join(tmpdir(), "multica-public-cli-"));
const testHome = join(root, "home");
const windows = process.platform === "win32";
if (windows && process.env.CI !== "true") {
  throw new Error("Windows online acceptance is restricted to disposable CI runners because user PATH is registry-backed");
}
const binDir = windows ? join(testHome, ".multica", "bin") : join(root, "bin");
const binName = windows ? "multica.exe" : "multica";
const installed = join(binDir, binName);
const profileDir = join(testHome, ".multica", "profiles", "acceptance");
mkdirSync(profileDir, { recursive: true });
const profile = join(profileDir, "config.json");
const originalConfig = JSON.stringify({ server_url: "https://private.example.invalid", token: "test-only-placeholder" });
writeFileSync(profile, originalConfig);
const env = { ...process.env, HOME: testHome, USERPROFILE: testHome, MULTICA_BIN_DIR: binDir };
// Do not inherit an operator's configured update mirror or self-host mode.
for (const key of Object.keys(env)) {
  if (key.startsWith("MULTICA_") && key !== "MULTICA_BIN_DIR") delete env[key];
}
const run = (command, args, options = {}) => execFileSync(command, args, {
  env, encoding: "utf8", timeout: 600_000, stdio: ["ignore", "pipe", "inherit"], ...options,
});
const inspect = (binary) => {
  const report = JSON.parse(run(binary, ["version", "--output", "json"]));
  validateCliReport(report, baseline, tag);
  assert.equal(report.build_commit, expectedCommit);
  return report;
};
const install = () => {
  if (windows) {
    // The Windows user PATH is registry-backed even with USERPROFILE isolated.
    // Restore it after testing; CI runners are disposable, not user machines.
    run("powershell.exe", ["-NoProfile", "-Command", `
      $ErrorActionPreference = 'Stop'
      $savedPath = [Environment]::GetEnvironmentVariable('Path', 'User')
      try { irm https://raw.githubusercontent.com/dunzic/multica-worker/main/scripts/install.ps1 | iex }
      finally { [Environment]::SetEnvironmentVariable('Path', $savedPath, 'User') }
    `], { stdio: "inherit" });
  } else {
    run("bash", ["-o", "pipefail", "-c", "curl -fsSL --connect-timeout 10 --max-time 60 https://raw.githubusercontent.com/dunzic/multica-worker/main/scripts/install.sh | bash"], { stdio: "inherit" });
  }
};

console.log(`Isolated public-release acceptance: ${root}`);
install();
const fresh = inspect(installed);
// A repeated one-line install is an idempotent repair/upgrade operation.
install();
inspect(installed);
const upgradeDir = join(root, "upgrade");
mkdirSync(upgradeDir);
const upgradeBinary = join(upgradeDir, binName);
copyFileSync(resolve(previousCli), upgradeBinary);
const previousVersion = JSON.parse(run(upgradeBinary, ["version", "--output", "json"])).version;
assert.notEqual(previousVersion.replace(/^v/, ""), tag.slice(1), "Upgrade must start from an older owned build");
run(upgradeBinary, ["update", "--download-timeout", "5m"], { stdio: "inherit" });
inspect(upgradeBinary);
run(upgradeBinary, ["update"], { stdio: "inherit" });
assert.equal(readFileSync(profile, "utf8"), originalConfig, "Private profile was modified");
const evidence = { tag, commit: fresh.build_commit, platform: process.platform, arch: process.arch, previousVersion, freshInstall: true, repeatInstall: true, oldBuildUpgrade: true, currentVersionUpdate: true, profilePreserved: true, root };
writeFileSync(join(root, "acceptance.json"), JSON.stringify(evidence, null, 2));
console.log(JSON.stringify(evidence, null, 2));
