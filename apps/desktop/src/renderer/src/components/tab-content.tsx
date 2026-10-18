import { useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { RouterProvider, type DataRouter } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { ScrollRestorationProvider } from "@multica/views/platform";
import { useActiveGroup, useTabStore } from "@/stores/tab-store";
import {
  createScrollRestorationAdapter,
  getAppRouter,
  initTabCoordinator,
  registerActiveHostElement,
  registerCoordinatorQueryClient,
} from "@/platform/tab-coordinator";

/**
 * Renders the active tab session through THE single app router
 * (MUL-4741 single-router session architecture).
 *
 * Exactly one tab is mounted at a time. Switching tabs remounts this host
 * (the key includes the tab id), and reload() remounts it without switching
 * (the key includes mountGeneration). Inactive tabs are pure state — their
 * restorable view state lives in the session memento, captured by the
 * Coordinator on deactivation and restored here on mount:
 *
 *   - restore is PULL-based: views ask for their saved offset while they
 *     mount (ScrollRestorationProvider) — virtualized lists feed it into
 *     their initial render, plain containers assign it at ref-attach. The
 *     first painted frame is already at the restored position; cold
 *     restores show the correct shell + skeletons and settle when data
 *     lands, never a flash of another workspace's data.
 */
export function TabContent() {
  const group = useActiveGroup();
  const generation = useTabStore((s) => s.mountGeneration);
  const qc = useQueryClient();

  // Fast Refresh can replace the Coordinator while preserving this tree's
  // hook state. Recheck its idempotent initialization before reading the
  // router, rather than relying on a once-only useState initializer.
  initTabCoordinator();
  const router = getAppRouter();
  // RouterProvider initializes its rendered state from its router only on
  // mount. A replacement router needs a new host, but normal URL/title
  // updates must not remount the page or discard local editing state.
  const routerHost = useMemo(() => ({ router, key: crypto.randomUUID() }), [router]);

  useEffect(() => {
    registerCoordinatorQueryClient(qc);
  }, [qc, router]);

  // Sync document.title when switching tabs within the active workspace.
  const activeTab = group?.tabs.find((tab) => tab.id === group.activeTabId);
  useEffect(() => {
    if (activeTab) document.title = activeTab.title;
  }, [activeTab]);

  if (!group) return null;

  return (
    <ActiveTabHost
      key={`${routerHost.key}:${group.activeTabId}:${generation}`}
      tabId={group.activeTabId}
      router={routerHost.router}
    />
  );
}

function ActiveTabHost({ tabId, router }: { tabId: string; router: DataRouter }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const scrollAdapter = useMemo(
    () => createScrollRestorationAdapter(tabId),
    [tabId],
  );

  // The Coordinator captures the outgoing memento from this element while
  // the store notification is still synchronous (pre-unmount).
  useLayoutEffect(() => {
    registerActiveHostElement(hostRef.current);
    return () => registerActiveHostElement(null);
  }, []);

  // `display: contents` keeps the wrapper transparent to the surrounding
  // flex layout.
  return (
    <div ref={hostRef} style={{ display: "contents" }}>
      <ScrollRestorationProvider adapter={scrollAdapter}>
        <RouterProvider router={router} />
      </ScrollRestorationProvider>
    </div>
  );
}
