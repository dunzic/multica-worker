// Pure compatibility rules shared by offline packaging and the desktop main
// process. Do not fetch GitHub here: the reviewed repository manifest is truth.
const NUMBER = /^(0|[1-9][0-9]*)$/;

function parseVersion(raw) {
  if (typeof raw !== "string") throw new Error("Missing semantic version");
  const match = /^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$/.exec(raw);
  if (!match || match[0] !== raw || match.slice(1, 4).some((part) => !NUMBER.test(part))) {
    throw new Error(`Invalid semantic version: ${raw}`);
  }
  const prerelease = match[4]?.split(".") ?? [];
  if (prerelease.some((part) => !part || (/^[0-9]+$/.test(part) && !NUMBER.test(part))) ||
      match[5]?.split(".").some((part) => !part)) {
    throw new Error(`Invalid semantic version: ${raw}`);
  }
  return { core: match.slice(1, 4).map(BigInt), prerelease };
}

export function compareVersions(left, right) {
  const a = parseVersion(left);
  const b = parseVersion(right);
  for (let i = 0; i < 3; i++) {
    if (a.core[i] !== b.core[i]) return a.core[i] > b.core[i] ? 1 : -1;
  }
  if (!a.prerelease.length || !b.prerelease.length) {
    return a.prerelease.length === b.prerelease.length ? 0 : a.prerelease.length ? -1 : 1;
  }
  for (let i = 0; i < Math.max(a.prerelease.length, b.prerelease.length); i++) {
    const x = a.prerelease[i];
    const y = b.prerelease[i];
    if (x === y) continue;
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    const nx = NUMBER.test(x);
    const ny = NUMBER.test(y);
    if (nx && ny) return BigInt(x) > BigInt(y) ? 1 : -1;
    if (nx !== ny) return nx ? -1 : 1;
    return x > y ? 1 : -1;
  }
  return 0;
}

export function validateManifest(value) {
  if (!value || value.schema_version !== 1 || value.distribution !== "dunzic/multica-worker" ||
      value.upstream?.repository !== "multica-ai/multica" ||
      !/^[0-9a-f]{40}$/.test(value.upstream?.commit ?? "") ||
      !/^[0-9a-f]{40}$/.test(value.upstream?.release_commit ?? "") ||
      !Array.isArray(value.required_cli_capabilities) || !value.required_cli_capabilities.length ||
      value.required_cli_capabilities.some((item) => typeof item !== "string" || !item.trim()) ||
      !value.api_requirements || typeof value.api_requirements !== "object" ||
      Array.isArray(value.api_requirements) || Object.values(value.api_requirements).some((item) => typeof item !== "string" || !item)) {
    throw new Error("Missing or invalid distribution compatibility manifest");
  }
  if (compareVersions(value.minimum_cli_version, value.upstream.version) < 0) {
    throw new Error("Minimum CLI version is below the absorbed upstream baseline");
  }
  return value;
}

export function requireReleaseVersion(version, manifest) {
  validateManifest(manifest);
  if (isDevelopmentVersion(version)) {
    throw new Error("A local development build is not a publishable CLI release");
  }
  if (compareVersions(version, manifest.minimum_cli_version) < 0) {
    throw new Error(`CLI version ${version} is below the recorded minimum ${manifest.minimum_cli_version} (upstream ${manifest.upstream.version})`);
  }
}

export function validateCliReport(report, baseline, expectedVersion) {
  validateManifest(baseline);
  if (!report || typeof report.version !== "string" || !/^[0-9a-f]{40}$/.test(report.build_commit ?? "")) {
    throw new Error("CLI build metadata is missing; install a versioned package from this fork");
  }
  const candidate = validateManifest(report.distribution);
  requireReleaseVersion(report.version, baseline);
  requireReleaseVersion(report.version, candidate);
  if (expectedVersion && report.version.replace(/^v/, "") !== expectedVersion.replace(/^v/, "")) {
    throw new Error(`CLI binary version ${report.version} does not match release ${expectedVersion}`);
  }
  if (compareVersions(candidate.upstream.version, baseline.upstream.version) < 0) {
    throw new Error("CLI absorbed upstream baseline is older than this client's recorded baseline");
  }
  const capabilities = Array.isArray(report.capabilities) ? report.capabilities : [];
  for (const capability of new Set([...baseline.required_cli_capabilities, ...candidate.required_cli_capabilities])) {
    if (!capabilities.includes(capability)) throw new Error(`CLI is missing required capability: ${capability}`);
  }
  return report;
}

export function isDevelopmentVersion(version) {
  return /-\d+-g[0-9a-f]+/.test(version) ||
    /^v?0\.0\.0-g[0-9a-f]+(?:-dirty)?(?:\+.*)?$/.test(version) || /-dirty(?:\+.*)?$/.test(version);
}

export function validateBundledCliReport(report, baseline, desktopVersion) {
  // A same-checkout development bundle is not a public release. Keep its
  // real git-describe label, but require exact desktop/CLI version and baseline.
  if (isDevelopmentVersion(desktopVersion)) {
    if (!report || typeof report.version !== "string" ||
        report.version.replace(/^v/, "") !== desktopVersion.replace(/^v/, "")) {
      throw new Error("Bundled development CLI does not match the desktop build");
    }
    const candidate = validateManifest(report.distribution);
    if (candidate.upstream.commit !== baseline.upstream.commit || candidate.upstream.version !== baseline.upstream.version) {
      throw new Error("Bundled development CLI has a different upstream baseline");
    }
    const baseVersion = report.version.replace(/^(v?\d+\.\d+\.\d+).*$/, "$1");
    validateCliReport({ ...report, version: baseVersion }, baseline, baseVersion);
    return report;
  }
  return validateCliReport(report, baseline, desktopVersion);
}
