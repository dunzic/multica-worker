// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BrowserWindow, WebContents } from "electron";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

type Handler = (...args: unknown[]) => void;
type IpcHandler = (...args: unknown[]) => unknown;

const ctx = vi.hoisted(() => ({
  handlers: new Map<string, Handler[]>(),
  ipcHandlers: new Map<string, IpcHandler>(),
  ipcHandle: vi.fn(),
  checkForUpdates: vi.fn(async () => ({
    updateInfo: { version: "0.3.18" },
    isUpdateAvailable: false,
  })),
  downloadUpdate: vi.fn(),
  quitAndInstall: vi.fn(),
  getVersion: vi.fn(() => "0.3.17"),
  userDataPath: "",
  isPackaged: true,
  platformSupportsUpdate: vi.fn(() => true),
}));

vi.mock("electron-updater", () => {
  const autoUpdater = {
    autoDownload: false,
    autoInstallOnAppQuit: false,
    channel: undefined as string | undefined,
    allowDowngrade: false,
    allowPrerelease: false,
    setFeedURL: vi.fn(),
    isUpdateSupported: ctx.platformSupportsUpdate,
    on: vi.fn((event: string, handler: Handler) => {
      const handlers = ctx.handlers.get(event) ?? [];
      handlers.push(handler);
      ctx.handlers.set(event, handlers);
      return autoUpdater;
    }),
    checkForUpdates: ctx.checkForUpdates,
    downloadUpdate: ctx.downloadUpdate,
    quitAndInstall: ctx.quitAndInstall,
  };
  return { autoUpdater };
});

vi.mock("electron", () => ({
  app: {
    get isPackaged() { return ctx.isPackaged; },
    getVersion: ctx.getVersion,
    getPath: vi.fn(() => ctx.userDataPath),
  },
  BrowserWindow: class BrowserWindow {},
  ipcMain: {
    handle: ctx.ipcHandle,
  },
}));

import {
  configureUpdateSource,
  setupAutoUpdater,
} from "./updater";
import { updaterPreferencesPath } from "./updater-preferences";
import { autoUpdater } from "electron-updater";

describe("owned desktop update source", () => {
  it("rejects a version below the recorded baseline without weakening the platform gate", async () => {
    const info = { version: "0.6.3", files: [], path: "test", sha512: "test", releaseDate: "2026-10-16" };
    expect(() => autoUpdater.isUpdateSupported({ ...info, version: "0.6.2-rc.1" })).toThrow("below");
    ctx.platformSupportsUpdate.mockReturnValueOnce(false);
    await expect(Promise.resolve(autoUpdater.isUpdateSupported(info))).resolves.toBe(false);
    expect(ctx.platformSupportsUpdate).toHaveBeenCalledWith(info);
    await expect(Promise.resolve(autoUpdater.isUpdateSupported(info))).resolves.toBe(true);
  });
  it.each([
    ["darwin", "arm64", null],
    ["darwin", "x64", "latest-x64"],
    ["win32", "x64", null],
    ["win32", "arm64", "latest-arm64"],
    ["linux", "x64", null],
    ["linux", "arm64", null],
  ] as const)("pins %s/%s to the owned stable feed without downgrades", (platform, arch, expectedChannel) => {
    let channel: string | null = null;
    const updater = {
      setFeedURL: vi.fn(),
      allowDowngrade: true,
      allowPrerelease: true,
      get channel() { return channel; },
      set channel(value: string | null) {
        channel = value;
        this.allowDowngrade = true;
      },
    };
    configureUpdateSource(updater, platform, arch);
    expect(updater.setFeedURL).toHaveBeenCalledWith({
      provider: "github", owner: "dunzic", repo: "multica-worker",
    });
    expect(updater.channel).toBe(expectedChannel);
    expect(updater.allowDowngrade).toBe(false);
    expect(updater.allowPrerelease).toBe(false);
  });
});

function emitUpdater(event: string, ...args: unknown[]) {
  for (const handler of ctx.handlers.get(event) ?? []) {
    handler(...args);
  }
}

async function invokeIpc(channel: string, ...args: unknown[]) {
  const handler = ctx.ipcHandlers.get(channel);
  if (!handler) throw new Error(`Missing IPC handler: ${channel}`);
  return handler({}, ...args);
}

function makeWindow() {
  const send = vi.fn();
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeDestroyedWindow() {
  return {
    isDestroyed: () => true,
    get webContents(): WebContents {
      throw new TypeError("Object has been destroyed");
    },
  } as unknown as BrowserWindow;
}

function makeWindowWithDestroyedWebContents() {
  const send = vi.fn(() => {
    throw new TypeError("Object has been destroyed");
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => true,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

function makeWindowWithThrowingSend(error: Error) {
  const send = vi.fn(() => {
    throw error;
  });
  return {
    win: {
      isDestroyed: () => false,
      webContents: {
        isDestroyed: () => false,
        send,
      },
    } as unknown as BrowserWindow,
    send,
  };
}

describe("setupAutoUpdater", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    ctx.userDataPath = mkdtempSync(join(tmpdir(), "multica-updater-test-"));
    ctx.handlers.clear();
    ctx.ipcHandlers.clear();
    ctx.ipcHandle.mockClear();
    ctx.ipcHandle.mockImplementation((channel: string, handler: IpcHandler) => {
      ctx.ipcHandlers.set(channel, handler);
    });
    ctx.checkForUpdates.mockClear();
    ctx.downloadUpdate.mockClear();
    ctx.quitAndInstall.mockClear();
    ctx.getVersion.mockClear();
    ctx.getVersion.mockReturnValue("0.6.2");
    ctx.isPackaged = true;
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    rmSync(ctx.userDataPath, { recursive: true, force: true });
  });

  it("enables automatic background updates by default", async () => {
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:get-preferences")).resolves.toEqual({
      automaticUpdates: true,
    });

    await vi.advanceTimersByTimeAsync(5_000);
    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("skips startup and periodic checks when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    // Let the async preference load settle before advancing timers; otherwise
    // the in-flight readFile can resolve after afterEach() removes the temp
    // dir, default back to enabled=true, and fire a background check into the
    // next test's freshly-cleared mock (flake on slow CI).
    await invokeIpc("updater:get-preferences");

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);

    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("persists the automatic update preference and stops future background checks", async () => {
    setupAutoUpdater(() => null);

    await expect(
      invokeIpc("updater:set-automatic-updates", false),
    ).resolves.toEqual({ automaticUpdates: false });
    expect(
      JSON.parse(
        readFileSync(updaterPreferencesPath(ctx.userDataPath), "utf-8"),
      ),
    ).toEqual({ automaticUpdates: false });

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000 + 5_000);
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("still allows an explicit manual check when automatic updates are disabled", async () => {
    writeFileSync(
      updaterPreferencesPath(ctx.userDataPath),
      JSON.stringify({ automaticUpdates: false }),
    );
    setupAutoUpdater(() => null);

    await expect(invokeIpc("updater:check")).resolves.toMatchObject({
      ok: true,
    });

    expect(ctx.checkForUpdates).toHaveBeenCalledTimes(1);
  });

  it("does not replace a local development package with a public release", async () => {
    ctx.getVersion.mockReturnValue("0.6.2-135-g73be2734f-dirty");
    setupAutoUpdater(() => null);
    await invokeIpc("updater:get-preferences");
    await vi.advanceTimersByTimeAsync(5_000);
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
    await expect(invokeIpc("updater:check")).resolves.toMatchObject({ ok: false, error: expect.stringContaining("development") });
    expect(ctx.checkForUpdates).not.toHaveBeenCalled();
  });

  it("reports a release lookup failure instead of claiming the installation is up to date", async () => {
    ctx.checkForUpdates.mockRejectedValueOnce(new Error("No published release"));
    setupAutoUpdater(() => null);
    await expect(invokeIpc("updater:check")).resolves.toEqual({ ok: false, error: "No published release" });
  });

  it("forwards update progress to a live renderer", () => {
    const { win, send } = makeWindow();
    setupAutoUpdater(() => win);

    emitUpdater("download-progress", { percent: 42 });

    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("skips update progress when the BrowserWindow has already been destroyed", () => {
    setupAutoUpdater(() => makeDestroyedWindow());

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
  });

  it("skips update progress when the BrowserWindow webContents has already been destroyed", () => {
    const { win, send } = makeWindowWithDestroyedWebContents();
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).not.toHaveBeenCalled();
  });

  it("skips update progress when webContents.send loses a destroy race", () => {
    const { win, send } = makeWindowWithThrowingSend(
      new TypeError("Object has been destroyed"),
    );
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).not.toThrow();
    expect(send).toHaveBeenCalledWith("updater:download-progress", {
      percent: 42,
    });
  });

  it("rethrows non-destroy errors from webContents.send", () => {
    const { win } = makeWindowWithThrowingSend(new Error("boom"));
    setupAutoUpdater(() => win);

    expect(() => emitUpdater("download-progress", { percent: 42 })).toThrow(
      "boom",
    );
  });
});
