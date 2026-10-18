import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useHtmlPreviewLocation } from "./use-html-preview-location";
import {
  HTML_PREVIEW_LOCATION_KEY as K,
  withLocationBridge,
} from "../utils/iframe-location-bridge";

function mountFrame() {
  const iframe = document.createElement("iframe");
  document.body.appendChild(iframe);
  const postMessage = vi.spyOn(iframe.contentWindow!, "postMessage");
  return { iframe, postMessage };
}

function post(source: Window | null, data: unknown) {
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { data, source }));
  });
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("useHtmlPreviewLocation", () => {
  it("accepts one foreground attachment action only from the current frame and scope", async () => {
    const activation = { isActive: false };
    Object.defineProperty(navigator, "userActivation", { configurable: true, value: activation });
    const { iframe } = mountFrame();
    const other = mountFrame().iframe;
    let complete!: () => void;
    const onAction = vi.fn(() => new Promise<void>((resolve) => { complete = resolve; }));
    const config = { baseUrl: "https://app.example", origins: ["https://app.example"], onAction };
    const { result, rerender } = renderHook(({ scope }) => useHtmlPreviewLocation("", scope, config), { initialProps: { scope: "report" } });
    act(() => result.current.frameRef(iframe));
    const data = { __multicaAttachment: 1, scope: "report:0", href: "/api/attachments/01a14afb-f2bb-7464-82b4-e5088732ed18/download" };
    const port = { onmessage: null as ((event: MessageEvent) => void) | null, start: vi.fn(), close: vi.fn() };
    const connect = (source: Window | null, scope: string) => {
      const event = new MessageEvent("message", { data: { __multicaAttachment: 1, type: "connect", scope }, source });
      Object.defineProperty(event, "ports", { value: [port] });
      act(() => window.dispatchEvent(event));
    };
    connect(other.contentWindow, "report:0");
    connect(iframe.contentWindow, "old:0");
    expect(port.start).not.toHaveBeenCalled();
    connect(iframe.contentWindow, "report:0");
    expect(port.start).toHaveBeenCalledOnce();
    const throughPort = (value = data) => act(() => port.onmessage!(new MessageEvent("message", { data: value })));
    throughPort();
    post(iframe.contentWindow, data);
    expect(onAction).not.toHaveBeenCalled();
    activation.isActive = true;
    post(other.contentWindow, data);
    // Even an activated report cannot issue public-channel attachment commands.
    post(iframe.contentWindow, data);
    post(iframe.contentWindow, { ...data, scope: "old:0" });
    throughPort({ ...data, scope: "old:0" });
    expect(onAction).not.toHaveBeenCalled();
    throughPort();
    rerender({ scope: "report" });
    throughPort();
    expect(onAction).toHaveBeenCalledOnce();
    await act(async () => { complete(); });
    rerender({ scope: "next-report" });
    throughPort();
    expect(onAction).toHaveBeenCalledOnce();
    expect(port.close).toHaveBeenCalledOnce();
  });
  it("loads the document at the initial address", () => {
    const { result } = renderHook(() => useHtmlPreviewLocation("?s=ia"));
    expect(result.current.address).toBe("?s=ia");
    expect(result.current.withAddress("<p>x</p>")).toBe(withLocationBridge("<p>x</p>", "?s=ia"));
  });

  it("follows the address its own frame reports, and no other window's", () => {
    const { iframe } = mountFrame();
    const other = mountFrame().iframe;
    const { result } = renderHook(() => useHtmlPreviewLocation());
    act(() => result.current.frameRef(iframe));

    post(other.contentWindow, { [K]: 1, type: "location", value: "?s=spoof" });
    expect(result.current.address).toBe("");

    post(iframe.contentWindow, { [K]: 1, type: "location", value: "?s=b#top" });
    expect(result.current.address).toBe("?s=b#top");
    // In-document navigation does not reload the frame.
    expect(result.current.frameKey).toBe(0);
  });

  it("reloads the frame for a new query", () => {
    const { iframe, postMessage } = mountFrame();
    const { result } = renderHook(() => useHtmlPreviewLocation("?s=a"));
    act(() => result.current.frameRef(iframe));

    act(() => result.current.navigate("?s=b#top"));
    expect(result.current.frameKey).toBe(1);
    expect(result.current.address).toBe("?s=b#top");
    expect(result.current.withAddress("")).toBe(withLocationBridge("", "?s=b#top"));
    expect(postMessage).not.toHaveBeenCalled();
  });

  it("sends a fragment-only change to the running document instead of reloading", () => {
    const { iframe, postMessage } = mountFrame();
    const { result } = renderHook(() => useHtmlPreviewLocation("?s=a"));
    act(() => result.current.frameRef(iframe));

    act(() => result.current.navigate("?s=a#anatomy"));
    expect(result.current.frameKey).toBe(0);
    expect(postMessage).toHaveBeenCalledWith({ [K]: 1, type: "hash", value: "#anatomy" }, "*");
  });

  it("reloads at the address the document navigated to", () => {
    const { iframe } = mountFrame();
    const { result } = renderHook(() => useHtmlPreviewLocation("?s=a"));
    act(() => result.current.frameRef(iframe));
    post(iframe.contentWindow, { [K]: 1, type: "location", value: "?s=c" });

    act(() => result.current.reload());
    expect(result.current.frameKey).toBe(1);
    expect(result.current.withAddress("")).toBe(withLocationBridge("", "?s=c"));
  });

  it("starts another document over at the initial address, in a fresh frame", () => {
    const { result, rerender } = renderHook(
      ({ scope }) => useHtmlPreviewLocation("?s=start", scope),
      { initialProps: { scope: "file-a" } },
    );
    act(() => result.current.navigate("?s=elsewhere"));
    expect(result.current.address).toBe("?s=elsewhere");

    rerender({ scope: "file-b" });
    expect(result.current.address).toBe("?s=start");
    expect(result.current.withAddress("")).toBe(withLocationBridge("", "?s=start"));
    expect(result.current.frameKey).toBe(2);
  });

  it("follows a link's navigate request only right after a click", () => {
    const { iframe } = mountFrame();
    // jsdom has no user activation; model the browser's.
    const activation = { isActive: false };
    Object.defineProperty(navigator, "userActivation", {
      configurable: true,
      value: activation,
    });
    try {
      const { result } = renderHook(() => useHtmlPreviewLocation());
      act(() => result.current.frameRef(iframe));

      post(iframe.contentWindow, { [K]: 1, type: "navigate", value: "?s=loop" });
      expect(result.current.frameKey).toBe(0);

      activation.isActive = true;
      post(iframe.contentWindow, { [K]: 1, type: "navigate", value: "?s=next" });
      expect(result.current.frameKey).toBe(1);
      expect(result.current.address).toBe("?s=next");
    } finally {
      delete (navigator as { userActivation?: unknown }).userActivation;
    }
  });
});
