// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseHtmlAttachmentLink, readAttachmentAction, withAttachmentBridge } from "./iframe-attachment-bridge";

const id = "01a14afb-f2bb-7464-82b4-e5088732ed18";
const baseUrl = "https://app.example";
const origins = [baseUrl, "https://api.example"];

describe("report attachment actions", () => {
  it("distinguishes preview and download without trusting URL workspace or query credentials", () => {
    expect(parseHtmlAttachmentLink(`${baseUrl}/acme/attachments/${id}/preview`, baseUrl, origins)).toEqual({ id, action: "preview" });
    expect(parseHtmlAttachmentLink(`/api/attachments/${id}/download?workspace_slug=other`, baseUrl, origins)).toEqual({ id, action: "download" });
    expect(parseHtmlAttachmentLink(`https://api.example/api/attachments/${id}/download`, baseUrl, origins)).toEqual({ id, action: "download" });
  });
  it.each([
    `https://evil.example/api/attachments/${id}/download`,
    `https://user:password@app.example/api/attachments/${id}/download`,
    `javascript:alert(1)`, `data:text/html,test`, `file:///api/attachments/${id}/download`,
    `/api/attachments/not-an-id/download`, `/api/attachments/${id}/signed-download`,
    `/api/attachments/${id}/download/extra`, `/acme%2fother/attachments/${id}/preview`,
    `/api/attachments/${id}/download${"x".repeat(2048)}`,
  ])("rejects unsupported or hostile link %s", (href) => {
    expect(parseHtmlAttachmentLink(href, baseUrl, origins)).toBeNull();
  });
  it("accepts only the current bounded bridge message", () => {
    const valid = { __multicaAttachment: 1, scope: "current", href: `/api/attachments/${id}/download` };
    expect(readAttachmentAction(valid, "current", baseUrl, origins)).toEqual({ id, action: "download" });
    for (const data of [null, {}, { ...valid, scope: "stale" }, { ...valid, href: 1 }, { ...valid, __multicaAttachment: 2 }]) {
      expect(readAttachmentAction(data, "current", baseUrl, origins)).toBeNull();
    }
  });
  it("preserves standards mode and escapes injected values", () => {
    const html = withAttachmentBridge("<!doctype html><p>report</p>", "</script>", baseUrl, origins);
    expect(html.startsWith("<!doctype html><script>")).toBe(true);
    expect(html).toContain("\\u003c/script>");
    expect(html).toContain("e.isTrusted");
    expect(html).toContain("new MessageChannel()");
    expect(html).toContain("postMessage.bind(c.port1)");
    expect(html).not.toContain("scope:S,href:h},'*'");
    expect(html).toContain("closest('a[href]')");
  });
});
