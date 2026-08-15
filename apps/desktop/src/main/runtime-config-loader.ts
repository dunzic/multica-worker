import { app } from "electron";
import { mkdir, readFile, rename, writeFile } from "fs/promises";
import { dirname, join } from "path";
import {
  DEFAULT_RUNTIME_CONFIG,
  parseRuntimeConfig,
  runtimeConfigFromDevEnv,
  runtimeConfigFromTarget,
  type RuntimeConfig,
  type RuntimeConfigEnv,
  type RuntimeConfigResult,
} from "../shared/runtime-config";

export async function loadRuntimeConfig(options: {
  isDev: boolean;
  env: RuntimeConfigEnv;
  configPath?: string;
}): Promise<RuntimeConfigResult> {
  if (options.isDev) {
    try {
      return { ok: true, config: runtimeConfigFromDevEnv(options.env) };
    } catch (err) {
      return { ok: false, error: { message: errorMessage(err) } };
    }
  }

  const configPath = options.configPath ?? desktopConfigPath();
  try {
    const raw = await readFile(configPath, "utf-8");
    return { ok: true, config: parseRuntimeConfig(raw) };
  } catch (err) {
    if (isMissingFileError(err)) {
      return { ok: true, config: { ...DEFAULT_RUNTIME_CONFIG } };
    }
    return {
      ok: false,
      error: {
        message: `Invalid ${configPath}: ${errorMessage(err)}`,
      },
    };
  }
}

export function desktopConfigPath(): string {
  return join(app.getPath("home"), ".multica", "desktop.json");
}

/** Persist a validated deployment target without ever exposing a partial file. */
export async function saveRuntimeConfig(
  input: unknown,
  configPath = desktopConfigPath(),
): Promise<RuntimeConfig> {
  const config = runtimeConfigFromTarget(input);
  await mkdir(dirname(configPath), { recursive: true });
  const temporaryPath = `${configPath}.tmp-${process.pid}-${Date.now()}`;
  await writeFile(temporaryPath, `${JSON.stringify(config, null, 2)}\n`, {
    encoding: "utf-8",
    mode: 0o600,
  });
  await rename(temporaryPath, configPath);
  return config;
}

function isMissingFileError(err: unknown): boolean {
  return Boolean(
    err &&
      typeof err === "object" &&
      "code" in err &&
      (err as NodeJS.ErrnoException).code === "ENOENT",
  );
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export type { RuntimeConfig, RuntimeConfigResult };
