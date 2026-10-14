import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { packageWindowsCli } from "./package-windows-cli.mjs";

const version = "0.6.1-test";
const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "multica-package-test-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const binaries = {};
  for (const [arch, machine] of [["amd64", 0x8664], ["arm64", 0xaa64]]) {
    // Synthetic PE headers test packaging, not executable behavior.
    const bytes = Buffer.alloc(128);
    bytes.write("MZ");
    bytes.writeUInt32LE(64, 0x3c);
    bytes.write("PE\0\0", 64);
    bytes.writeUInt16LE(machine, 68);
    const dir = join(root, "server", "bin", "windows-" + arch);
    mkdirSync(dir, { recursive: true });
    binaries[arch] = join(dir, "multica.exe");
    writeFileSync(binaries[arch], bytes);
  }
  const installer = join(root, "scripts", "windows-cli");
  mkdirSync(installer, { recursive: true });
  for (const name of ["install.ps1", "install.cmd", "README.txt"]) {
    copyFileSync(fileURLToPath(new URL("windows-cli/" + name, import.meta.url)), join(installer, name));
  }
  for (const name of ["LICENSE", "NOTICE"]) {
    copyFileSync(fileURLToPath(new URL("../" + name, import.meta.url)), join(root, name));
  }
  return { root, binaries };
}

test("Windows archives retain multica.exe, installer/profile instructions, attribution and matching hashes", { skip: process.platform === "win32" }, (t) => {
  // The local ZIP producer uses POSIX zip/unzip; Windows CI tests installers separately.
  const { root, binaries } = fixture(t);
  const archives = packageWindowsCli(root, version);
  assert.equal(archives.length, 2);
  const manifest = readFileSync(join(root, "dist", "cli", "checksums.txt"), "utf8");
  for (const [index, arch] of ["amd64", "arm64"].entries()) {
    const name = "multica-cli-" + version + "-windows-" + arch + "-installer.zip";
    const archive = archives[index];
    assert.equal(archive, join(root, "dist", "cli", name));
    const names = execFileSync("unzip", ["-Z1", archive], { encoding: "utf8" }).trim().split("\n");
    assert.deepEqual(names.sort(), ["multica.exe", "install.ps1", "install.cmd", "README.txt", "LICENSE", "NOTICE", "checksums.txt"].sort());
    const extract = (file) => execFileSync("unzip", ["-p", archive, file]);
    assert.deepEqual(extract("multica.exe"), readFileSync(binaries[arch]));
    assert.equal(extract("checksums.txt").toString(), sha256(extract("multica.exe")) + "  multica.exe\r\n");
    assert.ok(manifest.includes(sha256(readFileSync(archive)) + "  " + name + "\n"));
    for (const file of ["install.ps1", "install.cmd", "README.txt"]) {
      assert.doesNotMatch(extract(file).toString(), /(?<!\r)\n/);
    }
    assert.ok(extract("README.txt").toString().includes("multica --profile turingomni login"));
    assert.ok(extract("install.ps1").toString().includes("https://workers.turingomni.com"));
  }
});

for (const [label, mutate] of [
  ["truncated binary", () => Buffer.from("MZ")],
  ["missing MZ signature", (bytes) => { bytes.write("XX"); return bytes; }],
  ["invalid PE signature", (bytes) => { bytes.write("XX", 64); return bytes; }],
  ["out-of-range PE offset", (bytes) => { bytes.writeUInt32LE(0xffffffff, 0x3c); return bytes; }],
  ["wrong architecture", (bytes) => { bytes.writeUInt16LE(0x8664, 68); return bytes; }],
]) {
  test("rejects " + label + " before writing any archive", (t) => {
    const { root, binaries } = fixture(t);
    writeFileSync(binaries.arm64, mutate(readFileSync(binaries.arm64)));
    assert.throws(() => packageWindowsCli(root, version), /Windows executable/);
    assert.equal(existsSync(join(root, "dist", "cli")), false);
  });
}

test("rejects a missing second architecture without a partial package", (t) => {
  const { root, binaries } = fixture(t);
  rmSync(binaries.arm64);
  assert.throws(() => packageWindowsCli(root, version), /ENOENT/);
  assert.equal(existsSync(join(root, "dist", "cli")), false);
});

test("rejects invalid versions before accessing files", () => {
  for (const invalid of [null, "", "dev", "../escape", "v0.6.1"]) {
    assert.throws(() => packageWindowsCli("unused-fixture", invalid), /valid Git-derived package version/);
  }
});
