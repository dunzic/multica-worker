import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Keep the real router, coordinator, store and host. Only replace business
// pages: this regression concerns their lifecycle, not their API requests.
vi.mock("@/routes", async () => {
  const { createMemoryRouter, useLocation } = await import("react-router-dom");
  function RouteBody() {
    const location = useLocation();
    return (
      <>
        <h1>{location.pathname + location.search + location.hash}</h1>
        <input aria-label="Local editing state" defaultValue="" />
      </>
    );
  }
  return {
    createAppRouter: () => createMemoryRouter([
      { path: "*", element: <RouteBody /> },
    ]),
  };
});

import { TabContent } from "./tab-content";
import { getActiveTab, useTabStore } from "@/stores/tab-store";
import {
  __resetTabCoordinatorForTests,
  getAppRouter,
  initTabCoordinator,
} from "@/platform/tab-coordinator";

let qc: QueryClient;
const reportPath = "/acme/attachments/report/preview";

function tree() {
  return <QueryClientProvider client={qc}><TabContent /></QueryClientProvider>;
}

beforeEach(() => {
  __resetTabCoordinatorForTests();
  useTabStore.getState().reset();
  useTabStore.getState().switchWorkspace("acme", reportPath);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});

afterEach(() => {
  cleanup();
  __resetTabCoordinatorForTests();
  qc.clear();
  vi.restoreAllMocks();
});

describe("active tab router lifecycle", () => {
  it("keeps the title and body together after replacing the coordinator module", () => {
    const view = render(tree());
    expect(screen.getByRole("heading")).toHaveTextContent(reportPath);
    const oldRouter = getAppRouter();

    // Fast Refresh preserves the component's hook state while imported
    // modules are re-evaluated. Reset precisely that module-owned state.
    act(() => __resetTabCoordinatorForTests());
    view.rerender(tree());
    expect(getAppRouter()).not.toBe(oldRouter);
    expect(getAppRouter().state.location.pathname).toBe(reportPath);
    act(() => useTabStore.getState().navigateActiveSession("/acme/chat"));
    act(() => useTabStore.getState().updateTab(
      getActiveTab(useTabStore.getState())!.id, { title: "Chat" },
    ));
    expect(document.title).toBe("Chat");
    expect(screen.getByRole("heading")).toHaveTextContent("/acme/chat");
    expect(screen.queryByText(reportPath)).not.toBeInTheDocument();
  });

  it("releases the retired router and its store subscription before rebinding", () => {
    const view = render(tree());
    const oldRouter = getAppRouter();
    const dispose = vi.spyOn(oldRouter, "dispose");
    const oldNavigate = vi.spyOn(oldRouter, "navigate");
    act(() => __resetTabCoordinatorForTests());
    view.rerender(tree());
    expect(dispose).toHaveBeenCalledTimes(1);
    oldNavigate.mockClear();
    act(() => useTabStore.getState().switchWorkspace("other", "/other/chat"));
    expect(screen.getByRole("heading")).toHaveTextContent("/other/chat");
    expect(oldNavigate).not.toHaveBeenCalled();
    expect(useTabStore.getState().byWorkspace.acme.tabs[0].url).toBe(reportPath);
  });

  it("does not remount the body for normal rerenders or title changes", () => {
    const view = render(tree());
    const router = getAppRouter();
    fireEvent.change(screen.getByLabelText("Local editing state"), {
      target: { value: "Keep this unsaved input" },
    });
    view.rerender(tree());
    act(() => useTabStore.getState().updateTab(
      getActiveTab(useTabStore.getState())!.id, { title: "Report title resolved" },
    ));
    expect(getAppRouter()).toBe(router);
    expect(screen.getByLabelText("Local editing state")).toHaveValue("Keep this unsaved input");
  });

  it("initializes idempotently and retains one effective subscription", () => {
    render(tree());
    const router = getAppRouter();
    const subscribe = vi.spyOn(router, "subscribe");
    const navigate = vi.spyOn(router, "navigate");
    act(() => { initTabCoordinator(); initTabCoordinator(); });
    expect(subscribe).not.toHaveBeenCalled();
    act(() => useTabStore.getState().navigateActiveSession("/acme/chat?conversation=group"));
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("heading")).toHaveTextContent("/acme/chat?conversation=group");
  });

  it("unsubscribes the old store listener and rebinds reload invalidation", () => {
    const subscribe = useTabStore.subscribe;
    const disposals: ReturnType<typeof vi.fn>[] = [];
    vi.spyOn(useTabStore, "subscribe").mockImplementation((listener) => {
      const dispose = vi.fn(subscribe(listener));
      disposals.push(dispose);
      return dispose;
    });
    const view = render(tree());
    // The coordinator subscribes in render, before the React store hooks.
    const coordinatorDisposal = disposals[0];
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    act(() => __resetTabCoordinatorForTests());
    expect(coordinatorDisposal).toHaveBeenCalledTimes(1);
    view.rerender(tree());
    act(() => useTabStore.getState().reloadActiveTab());
    expect(invalidate).toHaveBeenCalledExactlyOnceWith({ type: "active", refetchType: "none" });
    expect(screen.getByRole("heading")).toHaveTextContent(reportPath);
  });

  it("keeps history, tab selection and workspace navigation working after repeated replacement", () => {
    const view = render(tree());
    for (let cycle = 0; cycle < 3; cycle++) {
      act(() => __resetTabCoordinatorForTests());
      view.rerender(tree());
      act(() => useTabStore.getState().navigateActiveSession("/acme/chat?conversation=group&topic=one"));
      expect(screen.getByRole("heading")).toHaveTextContent("topic=one");
      act(() => useTabStore.getState().goBack());
      expect(screen.getByRole("heading")).toHaveTextContent(reportPath);
      act(() => useTabStore.getState().goForward());
      expect(screen.getByRole("heading")).toHaveTextContent("topic=one");
      act(() => useTabStore.getState().navigateActiveSession(reportPath));
    }
    const reportTab = getActiveTab(useTabStore.getState())!.id;
    act(() => useTabStore.getState().openTab("/acme/chat?session=direct", "Direct", { activate: true }));
    expect(screen.getByRole("heading")).toHaveTextContent("session=direct");
    act(() => useTabStore.getState().setActiveTab(reportTab));
    expect(screen.getByRole("heading")).toHaveTextContent(reportPath);
    const history = getActiveTab(useTabStore.getState())!.history;
    act(() => useTabStore.getState().reloadActiveTab());
    expect(screen.getByRole("heading")).toHaveTextContent(reportPath);
    expect(getActiveTab(useTabStore.getState())!.history).toEqual(history);
  });
});
