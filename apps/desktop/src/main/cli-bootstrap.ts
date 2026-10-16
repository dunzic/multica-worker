import { app } from "electron";
import { execFile } from "child_process";
import { createHash } from "crypto";
import { createReadStream, createWriteStream, existsSync } from "fs";
import { chmod, mkdir, mkdtemp, rename, rm } from "fs/promises";
import { join, dirname } from "path";
import { pipeline } from "stream/promises";
import { Readable } from "stream";

import { selectPlatformReleaseAssetName } from "./cli-release-asset";
import { probeCompatibleCli } from "./cli-compatibility";
import baseline from "../../../../server/internal/buildinfo/distribution.json";
import { requireReleaseVersion } from "../../../../scripts/release-compatibility.mjs";

// Desktop prefers the bundled `multica` CLI shipped inside the app for
// same-repo builds, but it can also repair or bootstrap a managed copy in
// userData on first launch when the bundled binary is missing or unusable.

const GITHUB_RELEASES = "https://github.com/dunzic/multica-worker/releases";

async function latestReleaseTag(): Promise<string> {
  const res = await fetch("https://api.github.com/repos/dunzic/multica-worker/releases/latest", {
    headers: { Accept: "application/vnd.github+json" },
    signal: AbortSignal.timeout(30_000),
  });
  if (res.status === 404) {
    throw new Error(`No published CLI release is available at ${GITHUB_RELEASES}`);
  }
  if (!res.ok) throw new Error(`release lookup failed: ${res.status} ${res.statusText}`);
  const release: unknown = await res.json();
  if (
    typeof release !== "object" || release === null ||
    !("tag_name" in release) || typeof release.tag_name !== "string" ||
    !/^v\d+\.\d+\.\d+$/.test(release.tag_name) ||
    !("draft" in release) || release.draft !== false ||
    !("prerelease" in release) || release.prerelease !== false
  ) {
    throw new Error("Invalid stable CLI release metadata; refusing to install");
  }
  return release.tag_name;
}

function binaryName(): string {
  return process.platform === "win32" ? "multica.exe" : "multica";
}

export function managedCliPath(): string {
  return join(app.getPath("userData"), "bin", binaryName());
}

function run(cmd: string, args: string[], cwd?: string): Promise<void> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { cwd }, (err) => (err ? reject(err) : resolve()));
  });
}

async function downloadToFile(url: string, dest: string): Promise<void> {
  const res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(300_000) });
  if (!res.ok || !res.body) {
    throw new Error(`download failed: ${res.status} ${res.statusText}`);
  }
  await mkdir(dirname(dest), { recursive: true });
  // Node's fetch returns a web ReadableStream; adapt to a Node stream for pipeline.
  const nodeStream = Readable.fromWeb(res.body as Parameters<typeof Readable.fromWeb>[0]);
  await pipeline(nodeStream, createWriteStream(dest));
}

// Fetch goreleaser's published checksums.txt and parse it into a
// filename → sha256 lookup. Format is `<hex>  <filename>` per line.
async function fetchChecksums(releaseBase: string): Promise<Map<string, string>> {
  const url = `${releaseBase}/checksums.txt`;
  const res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(30_000) });
  if (!res.ok) {
    throw new Error(
      `checksums.txt fetch failed: ${res.status} ${res.statusText}`,
    );
  }
  const text = await res.text();
  const map = new Map<string, string>();
  for (const rawLine of text.split("\n")) {
    const line = rawLine.trim();
    if (!line) continue;
    const match = line.match(/^([a-f0-9]{64})\s+\*?(\S+)$/i);
    if (match) map.set(match[2], match[1].toLowerCase());
  }
  return map;
}

async function sha256OfFile(path: string): Promise<string> {
  const hash = createHash("sha256");
  await pipeline(createReadStream(path), hash);
  return hash.digest("hex");
}

async function verifyChecksum(
  archivePath: string,
  assetName: string,
  expected: string,
): Promise<void> {
  const actual = await sha256OfFile(archivePath);
  if (actual.toLowerCase() !== expected) {
    throw new Error(
      `checksum mismatch for ${assetName}: expected ${expected}, got ${actual}`,
    );
  }
}

async function extractArchive(archive: string, dest: string): Promise<void> {
  await mkdir(dest, { recursive: true });
  // Modern OSes all ship a `tar` that auto-detects tar.gz and zip:
  // - macOS/Linux: GNU tar or bsdtar
  // - Windows 10+: bsdtar is bundled as `tar.exe` since build 17063
  await run("tar", ["-xf", archive, "-C", dest]);
}

async function installFresh(): Promise<string> {
  const target = managedCliPath();
  // Resolve latest once: a release published during the download must not
  // switch the archive out from under its checksum manifest.
  const tag = await latestReleaseTag();
  requireReleaseVersion(tag, baseline);
  const releaseBase = `${GITHUB_RELEASES}/download/${tag}`;
  const checksums = await fetchChecksums(releaseBase);
  const assetName = selectPlatformReleaseAssetName(checksums.keys(), tag.slice(1));
  const expectedChecksum = checksums.get(assetName);
  if (!expectedChecksum) {
    throw new Error(
      `no checksum for ${assetName} in checksums.txt — refusing to install unverified binary`,
    );
  }
  const url = `${releaseBase}/${assetName}`;

  await mkdir(dirname(target), { recursive: true });
  // Keep staging on the target filesystem so rename is atomic on Unix and
  // a locked Windows target fails without first deleting the working CLI.
  const workDir = await mkdtemp(join(dirname(target), ".multica-cli-"));

  try {
    const archivePath = join(workDir, assetName);
    console.log(`[cli-bootstrap] downloading ${url}`);
    await downloadToFile(url, archivePath);

    console.log(`[cli-bootstrap] verifying ${assetName} against checksums.txt`);
    await verifyChecksum(archivePath, assetName, expectedChecksum);

    console.log(`[cli-bootstrap] extracting ${assetName}`);
    await extractArchive(archivePath, workDir);

    const extractedBin = join(workDir, binaryName());
    if (!existsSync(extractedBin)) {
      throw new Error(
        `archive ${assetName} did not contain ${binaryName()} at its root`,
      );
    }

    await chmod(extractedBin, 0o755);

    // macOS: ad-hoc sign so spawning the child never hits a gatekeeper quirk.
    // Non-fatal: unsigned binaries still execute when the parent app is trusted.
    if (process.platform === "darwin") {
      await run("codesign", ["-s", "-", "--force", extractedBin]).catch((err) => {
        console.warn("[cli-bootstrap] ad-hoc codesign failed:", err);
      });
    }

    await probeCompatibleCli(extractedBin, { expectedReleaseVersion: tag });
    await rename(extractedBin, target);

    console.log(`[cli-bootstrap] installed CLI at ${target}`);
    return target;
  } finally {
    await rm(workDir, { recursive: true, force: true }).catch(() => {});
  }
}

/**
 * Returns the path to a usable `multica` binary. If one is already present at
 * the managed userData location, returns it immediately. Otherwise downloads
 * the latest release asset for the current platform and installs it.
 */
export async function ensureManagedCli(
  options: { forceInstall?: boolean } = {},
): Promise<string> {
  const target = managedCliPath();
  if (existsSync(target) && !options.forceInstall) return target;
  return installFresh();
}
