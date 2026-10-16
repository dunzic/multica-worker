import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";

// Scope belongs to each immutable annotated tag, not a mutable repository
// variable. Missing or ambiguous instructions must never publish everything.
export function parseReleaseScope(message) {
  const entries = message.split(/\r?\n/).filter((line) => /^Release-Scope:/i.test(line));
  if (entries.length !== 1 || !/^Release-Scope: (cli|full)$/.test(entries[0])) {
    throw new Error("Annotated release tag requires exactly one Release-Scope: cli or Release-Scope: full line");
  }
  return entries[0].slice("Release-Scope: ".length);
}

export function readReleaseScope(tag, cwd = process.cwd()) {
  if (!/^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$/.test(tag ?? "")) {
    throw new Error("Invalid release tag");
  }
  const git = (...args) => execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
  const ref = `refs/tags/${tag}`;
  if (git("cat-file", "-t", ref) !== "tag") throw new Error("Release scope requires an annotated tag");
  if (git("rev-parse", `${ref}^{commit}`) !== git("rev-parse", "HEAD")) {
    throw new Error("Release tag must identify HEAD");
  }
  const object = git("cat-file", "-p", ref);
  return parseReleaseScope(object.slice(object.indexOf("\n\n") + 2));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { console.log(readReleaseScope(process.argv[2])); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
