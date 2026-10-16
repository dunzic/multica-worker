// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";
import baseline from "../../../../server/internal/buildinfo/distribution.json";

const ctx = vi.hoisted(() => ({ execFile: vi.fn() }));
vi.mock("child_process", () => ({ execFile: ctx.execFile }));
import { probeCompatibleCli } from "./cli-compatibility";

function respond(version = "v0.6.3") {
  ctx.execFile.mockImplementation((_file, _args, _options, callback) => callback(null, JSON.stringify({
    version, build_commit: "a".repeat(40), distribution: baseline,
    capabilities: baseline.required_cli_capabilities,
  })));
}

describe("CLI compatibility probe wiring", () => {
  beforeEach(() => { ctx.execFile.mockReset(); });

  it("probes version metadata only and returns the actual version", async () => {
    respond();
    await expect(probeCompatibleCli("/test/multica")).resolves.toBe("v0.6.3");
    expect(ctx.execFile).toHaveBeenCalledWith("/test/multica", ["version", "--output", "json"],
      { timeout: 5_000 }, expect.any(Function));
  });

  it("allows only the matching bundled development build", async () => {
    respond("v0.6.2-135-g73be2734f-dirty");
    await expect(probeCompatibleCli("/test/bundled", {
      bundledDesktopVersion: "0.6.2-135-g73be2734f-dirty",
    })).resolves.toBe("v0.6.2-135-g73be2734f-dirty");
    await expect(probeCompatibleCli("/test/managed")).rejects.toThrow();
    await expect(probeCompatibleCli("/test/bundled", {
      bundledDesktopVersion: "0.6.2-136-g111111111-dirty",
    })).rejects.toThrow("does not match");
  });

  it("does not accept an old/community executable reporting only a high version", async () => {
    ctx.execFile.mockImplementation((_file, _args, _options, callback) => callback(null, '{"version":"99.0.0"}'));
    await expect(probeCompatibleCli("/test/multica")).rejects.toThrow("metadata is missing");
  });

  it("propagates a timed-out probe instead of treating the executable as compatible", async () => {
    ctx.execFile.mockImplementation((_file, _args, _options, callback) => callback(new Error("timed out")));
    await expect(probeCompatibleCli("/test/multica")).rejects.toThrow("timed out");
  });
});
