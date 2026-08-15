/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import type { StoreApi, UseBoundStore } from "zustand";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthState } from "../auth/store";
import type { WSClient } from "../api/ws-client";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", async () => {
  const actual = await vi.importActual<typeof import("../platform/workspace-storage")>("../platform/workspace-storage");
  return { ...actual, getCurrentWsId: () => "workspace-1", getCurrentSlug: () => "workspace-1" };
});

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

type AnyHandler = (msg: { type: string; payload: unknown }) => void;

function fakeWS() {
  let anyHandler: AnyHandler | undefined;
  return {
    ws: {
      onAny: (handler: AnyHandler) => {
        anyHandler = handler;
        return () => undefined;
      },
      on: () => () => undefined,
      onReconnect: () => () => undefined,
    } as unknown as WSClient,
    emit: (type: string) => anyHandler?.({ type, payload: {} }),
  };
}

describe("useRealtimeSync role-source durable invalidation", () => {
  beforeEach(() => vi.useRealTimers());

  it("invalidates role-source and all materialized projections once", async () => {
    const client = new QueryClient();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const rec = fakeWS();
    const stores = {
      authStore: { getState: () => ({ user: null }) } as unknown as UseBoundStore<StoreApi<AuthState>>,
    } satisfies RealtimeSyncStores;
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    renderHook(() => useRealtimeSync(rec.ws, stores), { wrapper });

    rec.emit("role_source:applied");
    await waitFor(() => {
      expect(invalidate).toHaveBeenCalledWith({ queryKey: ["role-sources", "workspace-1"] });
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspaces", "workspace-1", "agents"] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["workspaces", "workspace-1", "skills"] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["autopilots", "workspace-1"] });
  });
});
