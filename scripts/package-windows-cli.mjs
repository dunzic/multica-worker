#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { deriveVersion } from "../apps/desktop/scripts/package.mjs";
import { checkDistribution } from "./check-distribution.mjs";

export function packageWindowsCli(root, version = deriveVersion(root)) {
  if (!version || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error("A valid Git-derived package version is required.");
  }
  // Validate both inputs before producing any archive.
  const binaries = ["amd64", "arm64"].map((arch) => {
    const binary = join(root, "server", "bin", "windows-" + arch, "multica.exe");
    const bytes = readFileSync(binary);
    if (bytes.length < 64 || bytes.subarray(0, 2).toString() !== "MZ") {
      throw new Error("Not a Windows executable: " + binary);
    }
    const offset = bytes.readUInt32LE(0x3c);
    if (offset > bytes.length - 6 ||
        bytes.toString("ascii", offset, offset + 4) !== "PE\0\0" ||
        bytes.readUInt16LE(offset + 4) !== (arch === "amd64" ? 0x8664 : 0xaa64)) {
      throw new Error("Windows executable architecture does not match " + arch);
    }
    return { arch, binary, bytes };
  });
  const output = join(root, "dist", "cli");
  mkdirSync(output, { recursive: true });
  const checksums = [];
  const archives = [];
  for (const { arch, binary, bytes } of binaries) {
    const staging = mkdtempSync(join(tmpdir(), "multica-windows-package-"));
    try {
      const files = ["multica.exe", "install.ps1", "install.cmd", "README.txt", "LICENSE", "NOTICE", "checksums.txt"];
      copyFileSync(binary, join(staging, "multica.exe"));
      for (const file of ["install.ps1", "install.cmd", "README.txt"]) {
        const source = readFileSync(join(root, "scripts", "windows-cli", file), "utf8");
        writeFileSync(join(staging, file), source.replace(/\r?\n/g, "\r\n"));
      }
      for (const file of ["LICENSE", "NOTICE"]) copyFileSync(join(root, file), join(staging, file));
      writeFileSync(join(staging, "checksums.txt"), createHash("sha256").update(bytes).digest("hex") + "  multica.exe\r\n");
      const name = "multica-cli-" + version + "-windows-" + arch + "-installer.zip";
      const archive = join(output, name);
      // Repack from an empty archive; never retain files from a previous build.
      rmSync(archive, { force: true });
      execFileSync("zip", ["-q", archive, ...files], { cwd: staging });
      checksums.push(createHash("sha256").update(readFileSync(archive)).digest("hex") + "  " + name);
      archives.push(archive);
    } finally {
      rmSync(staging, { recursive: true, force: true });
    }
  }
  writeFileSync(join(output, "checksums.txt"), checksums.join("\n") + "\n");
  return archives;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
  checkDistribution({ cwd: root });
  for (const archive of packageWindowsCli(root)) console.log(archive);
}
