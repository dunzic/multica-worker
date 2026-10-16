export interface DistributionManifest {
  schema_version: number;
  distribution: string;
  upstream: { repository: string; version: string; release_commit: string; commit: string };
  minimum_cli_version: string;
  required_cli_capabilities: string[];
  api_requirements: Record<string, string>;
}
export interface CliBuildReport {
  version: string;
  build_commit: string;
  distribution: DistributionManifest;
  capabilities: string[];
}
export function compareVersions(left: string, right: string): number;
export function validateManifest(value: unknown): DistributionManifest;
export function requireReleaseVersion(version: string, manifest: DistributionManifest): void;
export function validateCliReport(report: unknown, baseline: DistributionManifest, expectedVersion?: string): CliBuildReport;
export function isDevelopmentVersion(version: string): boolean;
export function validateBundledCliReport(report: unknown, baseline: DistributionManifest, desktopVersion: string): CliBuildReport;
