#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { requireReleaseVersion, validateManifest } from "./release-compatibility.mjs";

export const manifestPath = "server/internal/buildinfo/distribution.json";
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function checkDistribution({ cwd = root, release = false, version } = {}) {
  const manifest = validateManifest(JSON.parse(readFileSync(resolve(cwd, manifestPath), "utf8")));
  const git = (args) => execFileSync("git", args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
  try {
    git(["merge-base", "--is-ancestor", manifest.upstream.commit, "HEAD"]);
    git(["merge-base", "--is-ancestor", manifest.upstream.release_commit, manifest.upstream.commit]);
  } catch {
    throw new Error(`Recorded upstream commit ${manifest.upstream.commit} is missing or is not contained in HEAD. Restore local history or correct the reviewed baseline; do not raise only the version.`);
  }
  // If a tag is available locally, it must agree with the reviewed record.
  // A source export with commit history but no tag refs still works offline.
  let upstreamTag;
  try { upstreamTag = git(["rev-parse", `v${manifest.upstream.version}^{commit}`]); } catch { /* No local tag. */ }
  if (upstreamTag && upstreamTag !== manifest.upstream.release_commit) {
    throw new Error("Recorded upstream version does not match the local release tag");
  }
  if (release) {
    requireReleaseVersion(version, manifest);
    const tag = `v${version.replace(/^v/, "")}`;
    let tagged;
    try { tagged = git(["rev-parse", `${tag}^{commit}`]); } catch {
      throw new Error(`Release tag ${tag} is missing locally`);
    }
    if (tagged !== git(["rev-parse", "HEAD"])) throw new Error(`Release tag ${tag} does not identify HEAD`);
    if (git(["status", "--porcelain", "--untracked-files=normal"])) throw new Error("A release requires a clean reviewed checkout");
  }
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const args = process.argv.slice(2);
    const versionIndex = args.indexOf("--version");
    const manifest = checkDistribution({
      release: args.includes("--release"),
      version: versionIndex >= 0 ? args[versionIndex + 1] : undefined,
    });
    console.log(`Offline baseline verified: ${manifest.distribution}; upstream ${manifest.upstream.version}@${manifest.upstream.commit}; minimum CLI ${manifest.minimum_cli_version}`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
