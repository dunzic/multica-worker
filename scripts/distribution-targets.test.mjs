import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

const read = (file) => readFileSync(fileURLToPath(new URL("../" + file, import.meta.url)), "utf8");
const service = "https://workers.turingomni.com";
const repo = "dunzic/multica-worker";

test("production service defaults are TuringOmni across CLI, desktop, and mobile", () => {
  for (const file of [
    "server/cmd/multica/cmd_agent.go",
    "apps/desktop/src/shared/runtime-config.ts",
    "packages/views/runtimes/components/connect-remote-dialog.tsx",
    "apps/mobile/.env.production",
    "apps/mobile/.env.production.example",
  ]) {
    const source = read(file);
    assert.ok(source.includes(service), file);
    assert.ok(!source.includes('"https://api.multica.ai"'), file);
    assert.ok(!source.includes('"https://multica.ai"'), file);
  }
  assert.ok(read("apps/desktop/src/shared/runtime-config.ts").includes("wss://workers.turingomni.com/ws"));
  for (const file of ["apps/mobile/.env.production", "apps/mobile/.env.production.example"]) {
    assert.match(read(file), /^EXPO_PUBLIC_API_URL=https:\/\/workers\.turingomni\.com$/m);
    assert.match(read(file), /^EXPO_PUBLIC_WEB_URL=https:\/\/workers\.turingomni\.com$/m);
  }
});

test("installation and updates never select community release assets", () => {
  for (const file of [
    "scripts/install.sh", "scripts/install.ps1",
    "server/internal/cli/update.go",
    "apps/desktop/src/main/cli-bootstrap.ts",
    "packages/views/common/cli-install-command.tsx",
    "packages/views/layout/help-launcher.tsx",
    "apps/web/features/landing/utils/github-release.ts",
    "apps/web/app/(landing)/download/download-client.tsx",
    "apps/desktop/src/renderer/src/components/update-notification.tsx",
    "packages/views/runtimes/components/update-section.tsx",
    "README.md",
    "scripts/windows-cli/README.txt",
  ]) {
    const source = read(file);
    assert.ok(source.includes(repo), file);
    assert.doesNotMatch(source, /(?:github\.com|raw\.githubusercontent\.com)\/(?:repos\/)?multica-ai\/multica\/(?:releases|main\/scripts)/, file);
    assert.ok(!source.includes("multica-ai/tap"), file);
  }
  const builder = read("apps/desktop/electron-builder.yml");
  assert.match(builder, /owner: dunzic\s+repo: multica-worker/);
  assert.match(read(".github/workflows/release.yml"), /github\.repository == 'dunzic\/multica-worker'/);
  assert.doesNotMatch(read(".goreleaser.yml"), /brews:|owner: multica-ai/);
  assert.ok(!read("server/cmd/multica/cmd_update.go").includes("multica-ai/tap"));
});

test("Windows offline installer isolates credentials and validates the binary", () => {
  const source = read("scripts/windows-cli/install.ps1");
  assert.ok(source.includes(service));
  assert.ok(source.includes("Get-FileHash"));
  assert.ok(source.includes("profiles\\turingomni"));
  assert.ok(source.includes("config.server_url -ne $service"));
  assert.ok(source.includes("multica --profile turingomni login"));
});

test("CLI program name is unchanged and installers do not add an alternate launcher", () => {
  for (const file of [
    "scripts/install.ps1", "scripts/windows-cli/install.ps1",
    "scripts/windows-cli/README.txt", "CLI_INSTALL.md",
  ]) {
    assert.ok(!read(file).includes("multica-turingomni"), file);
  }
  assert.ok(!existsSync(fileURLToPath(new URL("windows-cli/multica-turingomni.cmd", import.meta.url))));
  assert.ok(!read("scripts/package-windows-cli.mjs").includes("multica-turingomni.cmd"));
});
