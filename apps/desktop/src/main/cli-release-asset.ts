function platformArchiveDescriptor(
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): { os: string; arch: string; ext: string } {
  const osMap: Record<string, string> = {
    darwin: "darwin",
    linux: "linux",
    win32: "windows",
  };
  const archMap: Record<string, string> = {
    x64: "amd64",
    arm64: "arm64",
  };
  const os = osMap[platform];
  const mappedArch = archMap[arch];
  if (!os || !mappedArch) {
    throw new Error(
      `unsupported platform for CLI auto-install: ${platform}/${arch}`,
    );
  }
  const ext = platform === "win32" ? "zip" : "tar.gz";
  return { os, arch: mappedArch, ext };
}

export function selectPlatformReleaseAssetName(
  assetNames: Iterable<string>,
  version: string,
  platform: NodeJS.Platform = process.platform,
  arch: string = process.arch,
): string {
  const { os, arch: mappedArch, ext } = platformArchiveDescriptor(
    platform,
    arch,
  );
  const names = [...assetNames];

  // Prefer the versioned `multica-cli-<v>-<os>-<arch>.<ext>` name; fall
  // back to the legacy `multica_<os>_<arch>.<ext>` so older releases that
  // only ship the legacy archive keep working.
  const suffix = `-${os}-${mappedArch}.${ext}`;
  const versionedName = `multica-cli-${version}${suffix}`;
  if (names.includes(versionedName)) return versionedName;

  const legacyName = `multica_${os}_${mappedArch}.${ext}`;
  if (names.includes(legacyName)) {
    return legacyName;
  }

  throw new Error(`no release asset found for version ${version} and current platform: ${suffix}`);
}
