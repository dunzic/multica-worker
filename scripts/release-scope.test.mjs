import assert from "node:assert/strict";
import { readFileSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { parseReleaseScope, readReleaseScope } from "./release-scope.mjs";

test("release scope is explicit and fails closed", () => {
  for (const scope of ["cli", "full"]) assert.equal(parseReleaseScope(`Release\n\nRelease-Scope: ${scope}\n`), scope);
  for (const message of ["", "Release-Scope: all", "Release-Scope: CLI", "Release-Scope: cli\nRelease-Scope: full", "release-scope: full"]) {
    assert.throws(() => parseReleaseScope(message), /requires exactly one/);
  }
});

test("scope comes from an annotated tag at HEAD, never a commit message or lightweight tag", () => {
  const cwd = mkdtempSync(join(tmpdir(), "multica-release-scope-"));
  const git = (...args) => execFileSync("git", ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", ...args], { cwd, stdio: "pipe" });
  try {
    git("init");
    writeFileSync(join(cwd, "source"), "test");
    git("add", "."); git("commit", "-m", "Release-Scope: full");
    git("tag", "v1.0.0");
    assert.throws(() => readReleaseScope("v1.0.0", cwd), /annotated/);
    git("tag", "-a", "v1.0.1", "-m", "Release-Scope: cli");
    assert.equal(readReleaseScope("v1.0.1", cwd), "cli");
    git("commit", "--allow-empty", "-m", "next");
    assert.throws(() => readReleaseScope("v1.0.1", cwd), /identify HEAD/);
  } finally { rmSync(cwd, { recursive: true, force: true }); }
});

test("every non-CLI publisher requires full scope before it can start", () => {
  const workflow = readFileSync(new URL("../.github/workflows/release.yml", import.meta.url), "utf8");
  for (const name of ["docker-backend-build", "docker-backend-merge", "docker-web-build", "docker-web-merge", "desktop", "helm-chart"]) {
    const job = workflow.split(`\n  ${name}:\n`)[1]?.split(/\n  [a-z][a-z-]*:\n/)[0];
    assert.ok(job, `missing job ${name}`);
    assert.match(job, /if:.*needs\.verify\.outputs\.release_scope == 'full'/, name);
  }
  assert.match(workflow, /release_scope: \$\{\{ steps\.scope\.outputs\.release_scope \}\}/);
  assert.match(workflow, /node scripts\/release-scope\.mjs/);
  const cliPublisher = workflow.split("\n  release:\n")[1].split("\n  cli-acceptance:\n")[0];
  assert.match(cliPublisher, /needs: \[verify, installer-verify\]/);
  assert.match(workflow, /node scripts\/verify-cli-release\.mjs/);
});
