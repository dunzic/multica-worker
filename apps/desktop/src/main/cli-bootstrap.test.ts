// @vitest-environment node
import { createHash } from "node:crypto";
import { mkdtemp, readFile, readdir, rm, writeFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import baseline from "../../../../server/internal/buildinfo/distribution.json";

const ctx = vi.hoisted(() => ({
  userData: "",
  fetch: vi.fn(),
  execFile: vi.fn(),
  failRename: false,
  report: {} as unknown,
}));

vi.mock("electron", () => ({ app: { getPath: () => ctx.userData } }));
vi.mock("child_process", () => ({ execFile: ctx.execFile }));
vi.mock("fs/promises", async (importOriginal) => {
  const fs = await importOriginal<typeof import("node:fs/promises")>();
  return {
    ...fs,
    rename: async (source: string, destination: string) => {
      if (ctx.failRename) throw new Error("target is locked");
      return fs.rename(source, destination);
    },
  };
});

import { ensureManagedCli, managedCliPath } from "./cli-bootstrap";

const repository = "https://github.com/dunzic/multica-worker/releases";
const api = "https://api.github.com/repos/dunzic/multica-worker/releases/latest";
const tag = "v1.2.3";
const os = process.platform === "win32" ? "windows" : process.platform;
const arch = process.arch === "x64" ? "amd64" : process.arch;
const extension = process.platform === "win32" ? "zip" : "tar.gz";
const asset = `multica-cli-1.2.3-${os}-${arch}.${extension}`;
const archive = "synthetic archive bytes";
const checksum = createHash("sha256").update(archive).digest("hex");
const metadata = { tag_name: tag, draft: false, prerelease: false };

function respond(options: { metadata?: unknown; status?: number; corrupt?: boolean } = {}) {
  ctx.fetch.mockImplementation(async (input: string) => {
    if (input === api) {
      return new Response(JSON.stringify(options.metadata ?? metadata), {
        status: options.status ?? 200,
      });
    }
    if (input === `${repository}/download/${tag}/checksums.txt`) {
      return new Response(`${checksum}  ${asset}\n`);
    }
    if (input === `${repository}/download/${tag}/${asset}`) {
      return new Response(options.corrupt ? "corrupted" : archive);
    }
    throw new Error(`Unexpected download URL: ${input}`);
  });
}

describe("owned CLI bootstrap", () => {
  beforeEach(async () => {
    ctx.userData = await mkdtemp(join(tmpdir(), "multica-bootstrap-test-"));
    ctx.failRename = false;
    ctx.fetch.mockReset();
    ctx.execFile.mockReset();
    ctx.report = {
      version: tag,
      build_commit: "a".repeat(40),
      distribution: baseline,
      capabilities: baseline.required_cli_capabilities,
    };
    vi.stubGlobal("fetch", ctx.fetch);
    ctx.execFile.mockImplementation((command: string, args: string[], _options: unknown, callback: (error: Error | null, stdout?: string) => void) => {
      if (command === "tar") {
        // Extraction is simulated; the download/checksum and replacement use
        // real files. Native tar and code-signing acceptance is separate.
        const binary = process.platform === "win32" ? "multica.exe" : "multica";
        void writeFile(join(args[3], binary), "new CLI").then(
          () => callback(null), callback,
        );
      } else if (command === "codesign") {
        callback(null);
      } else if (args.join(" ") === "version --output json" && command.startsWith(ctx.userData)) {
        callback(null, JSON.stringify(ctx.report));
      } else {
        callback(new Error(`unexpected executable: ${command}`));
      }
    });
    respond();
  });

  afterEach(async () => {
    vi.unstubAllGlobals();
    await rm(ctx.userData, { recursive: true, force: true });
  });

  it("pins the manifest and archive to one owned release and installs after verification", async () => {
    const installed = await ensureManagedCli();
    expect(installed).toBe(managedCliPath());
    expect(await readFile(installed, "utf8")).toBe("new CLI");
    expect(ctx.fetch.mock.calls.map(([url]) => url)).toEqual([
      api,
      `${repository}/download/${tag}/checksums.txt`,
      `${repository}/download/${tag}/${asset}`,
    ]);
    expect(await readdir(dirname(installed))).toEqual([process.platform === "win32" ? "multica.exe" : "multica"]);
  });

  it("keeps the existing managed CLI without contacting any release server", async () => {
    await mkdir(dirname(managedCliPath()), { recursive: true });
    await writeFile(managedCliPath(), "old CLI");
    await expect(ensureManagedCli()).resolves.toBe(managedCliPath());
    expect(ctx.fetch).not.toHaveBeenCalled();
  });

  it("reports a missing owned release without trying community binaries", async () => {
    respond({ status: 404 });
    await expect(ensureManagedCli()).rejects.toThrow("No published CLI release");
    expect(ctx.fetch).toHaveBeenCalledTimes(1);
    expect(ctx.execFile).not.toHaveBeenCalled();
  });

  it.each([
    { ...metadata, tag_name: "location: https://example.com" },
    { ...metadata, tag_name: "v1.2.3/../../other" },
    { ...metadata, draft: true },
    { ...metadata, prerelease: true },
    { tag_name: "v1.2.3" },
  ])("rejects malformed or unpublished release metadata: %j", async (badMetadata) => {
    respond({ metadata: badMetadata });
    await expect(ensureManagedCli()).rejects.toThrow("Invalid stable CLI release metadata");
    expect(ctx.fetch).toHaveBeenCalledTimes(1);
  });

  it("rejects corrupt bytes before extraction and preserves the old CLI", async () => {
    await mkdir(dirname(managedCliPath()), { recursive: true });
    await writeFile(managedCliPath(), "old CLI");
    respond({ corrupt: true });
    await expect(ensureManagedCli({ forceInstall: true })).rejects.toThrow("checksum mismatch");
    expect(ctx.execFile).not.toHaveBeenCalled();
    expect(await readFile(managedCliPath(), "utf8")).toBe("old CLI");
    expect(await readdir(dirname(managedCliPath()))).toHaveLength(1);
  });

  it("preserves the old CLI if atomic replacement fails", async () => {
    await mkdir(dirname(managedCliPath()), { recursive: true });
    await writeFile(managedCliPath(), "old CLI");
    ctx.failRename = true;
    await expect(ensureManagedCli({ forceInstall: true })).rejects.toThrow("target is locked");
    expect(await readFile(managedCliPath(), "utf8")).toBe("old CLI");
    expect(await readdir(dirname(managedCliPath()))).toHaveLength(1);
  });

  it.each([
    { version: tag },
    { version: "v1.2.4", build_commit: "a".repeat(40), distribution: baseline, capabilities: baseline.required_cli_capabilities },
    { version: tag, build_commit: "a".repeat(40), distribution: baseline, capabilities: [] },
  ])("preserves the old CLI when the verified archive has incompatible metadata: %j", async (report) => {
    await mkdir(dirname(managedCliPath()), { recursive: true });
    await writeFile(managedCliPath(), "old CLI");
    ctx.report = report;
    await expect(ensureManagedCli({ forceInstall: true })).rejects.toThrow();
    expect(await readFile(managedCliPath(), "utf8")).toBe("old CLI");
    expect(await readdir(dirname(managedCliPath()))).toHaveLength(1);
  });

  it("rejects a release below the offline baseline before downloading", async () => {
    respond({ metadata: { ...metadata, tag_name: "v0.1.0" } });
    await expect(ensureManagedCli()).rejects.toThrow("below the recorded minimum");
    expect(ctx.fetch).toHaveBeenCalledTimes(1);
  });
});
