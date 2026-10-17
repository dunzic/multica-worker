import { expect, test } from "@playwright/test";
import { buildSurfaceFrameDocument } from "../packages/views/plugins/surface-document";

/**
 * Real Chromium coverage for behavior jsdom does not implement: executing a
 * hosted, opaque plugin document inside the trusted srcdoc wrapper, reporting
 * errors, and firing lifecycle events when the host replaces the launch.
 */

test.describe("plugin surface document (real Chromium, hosted v2 surface)", () => {
  test("a host-authored srcdoc reload is not reported as hostile navigation", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", async (route) => {
      const challenge = new URL(route.request().url()).pathname.endsWith("replacement")
        ? "replacement-proof" : "first-proof";
      await route.fulfill({
        contentType: "text/html",
        headers: { "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'" },
        body: `<!doctype html><script>
          const channel = new MessageChannel();
          addEventListener("pagehide", () => parent.postMessage({ type: "multica:plugin-surface-navigated" }, "*"));
          parent.postMessage({ type: "multica:plugin-bridge-connect", version: 2,
            challenge: ${JSON.stringify(challenge)} }, "*", [channel.port1]);
        </script>`,
      });
    });
    const first = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/plugin-surfaces/first",
      bridgeToken: "first-proof",
    });
    const replacement = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/plugin-surfaces/replacement",
      bridgeToken: "replacement-proof",
    });

    await page.setContent("<!doctype html><body></body>");
    await page.evaluate((srcdoc) => {
      const state = { ran: 0, navigated: 0 };
      (window as unknown as { __surfaceState: typeof state }).__surfaceState = state;
      window.addEventListener("message", (event) => {
        const frame = document.querySelector<HTMLIFrameElement>("#surface");
        if (!frame || event.source !== frame.contentWindow) return;
        const type = (event.data as { type?: string } | null)?.type;
        if (type === "multica:plugin-bridge-connect" && event.ports[0]) {
          state.ran++;
          event.ports[0].close();
        }
        if (type === "multica:plugin-surface-navigated") state.navigated++;
      });
      const frame = document.createElement("iframe");
      frame.id = "surface";
      frame.sandbox.add("allow-scripts", "allow-same-origin");
      frame.srcdoc = srcdoc;
      document.body.appendChild(frame);
    }, first);

    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { __surfaceState: { ran: number } }).__surfaceState.ran,
    )).toBe(1);

    await page.evaluate((srcdoc) => {
      document.querySelector<HTMLIFrameElement>("#surface")!.srcdoc = srcdoc;
    }, replacement);
    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { __surfaceState: { ran: number } }).__surfaceState.ran,
    )).toBe(2);

    expect(await page.evaluate(() =>
      (window as unknown as { __surfaceState: { navigated: number } }).__surfaceState.navigated,
    )).toBe(0);
  });

  test("relays a first-line hosted plugin error exactly once", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", async (route) => {
      await route.fulfill({
        contentType: "text/html",
        headers: { "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'" },
        body: `<!doctype html><script>
          addEventListener("error", () => {
            parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
            parent.postMessage({ type: "multica:plugin-surface-error" }, "*");
          });
          throw new Error("plugin failed during bootstrap");
        </script>`,
      });
    });
    const documentWithError = buildSurfaceFrameDocument({
      url: "https://plugin-content.example.test/plugin-surfaces/failing",
      bridgeToken: "failure-proof",
    });

    await page.setContent("<!doctype html><body></body>");
    await page.evaluate((srcdoc) => {
      // v2 arms the host listener before assigning srcdoc; its one-shot
      // bridge and terminal events must not rely on replayable challenges.
      (window as unknown as { __surfaceErrors: number }).__surfaceErrors = 0;
      window.addEventListener("message", (event) => {
        if ((event.data as { type?: string } | null)?.type !== "multica:plugin-surface-error") return;
        const frame = document.querySelector<HTMLIFrameElement>("#surface")!;
        if (event.source !== frame.contentWindow) return;
        (window as unknown as { __surfaceErrors: number }).__surfaceErrors++;
      });
      const frame = document.createElement("iframe");
      frame.id = "surface";
      frame.sandbox.add("allow-scripts", "allow-same-origin");
      frame.srcdoc = srcdoc;
      document.body.appendChild(frame);
    }, documentWithError);

    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { __surfaceErrors: number }).__surfaceErrors,
    )).toBe(1);
    await page.waitForTimeout(100);
    expect(await page.evaluate(() =>
      (window as unknown as { __surfaceErrors: number }).__surfaceErrors,
    )).toBe(1);
  });
});
