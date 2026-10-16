import { execFile } from "child_process";
import baseline from "../../../../server/internal/buildinfo/distribution.json";
import {
  validateBundledCliReport,
  validateCliReport,
} from "../../../../scripts/release-compatibility.mjs";

/** Probe only version metadata; never start a daemon or access an account. */
export async function probeCompatibleCli(
  bin: string,
  options: { bundledDesktopVersion?: string; expectedReleaseVersion?: string } = {},
): Promise<string> {
  const stdout = await new Promise<string>((resolve, reject) => {
    execFile(bin, ["version", "--output", "json"], { timeout: 5_000 }, (error, output) => {
      if (error) reject(error);
      else resolve(output);
    });
  });
  const report: unknown = JSON.parse(stdout);
  return (options.bundledDesktopVersion
    ? validateBundledCliReport(report, baseline, options.bundledDesktopVersion)
    : validateCliReport(report, baseline, options.expectedReleaseVersion)).version;
}
