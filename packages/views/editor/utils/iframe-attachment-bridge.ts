export interface HtmlAttachmentAction {
  id: string;
  action: "preview" | "download";
}

/** Neither the URL's workspace nor its query credentials grant access. */
export function parseHtmlAttachmentLink(
  href: string,
  baseUrl: string,
  origins: readonly string[],
): HtmlAttachmentAction | null {
  if (href.length > 2048 || /[\\\s]/.test(href)) return null;
  try {
    const url = new URL(href, baseUrl);
    if (!/^https?:$/.test(url.protocol) || url.username || url.password || !origins.includes(url.origin)) return null;
    const uuid = "([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})";
    const preview = new RegExp(`^/[^/%]+/attachments/${uuid}/preview$`, "i").exec(url.pathname);
    if (preview?.[1]) return { id: preview[1], action: "preview" };
    const download = new RegExp(`^/api/attachments/${uuid}/download$`, "i").exec(url.pathname);
    return download?.[1] ? { id: download[1], action: "download" } : null;
  } catch {
    return null;
  }
}

export function readAttachmentAction(data: unknown, scope: string, baseUrl: string, origins: readonly string[]): HtmlAttachmentAction | null {
  if (!data || typeof data !== "object") return null;
  const record = data as Record<string, unknown>;
  if (record.__multicaAttachment !== 1 || record.scope !== scope || typeof record.href !== "string") return null;
  return parseHtmlAttachmentLink(record.href, baseUrl, origins);
}

/** No credentials, file data, popup permission, or same-origin capability enters the frame. */
export function withAttachmentBridge(html: string, scope: string, baseUrl: string, origins: readonly string[]): string {
  const literal = (value: unknown) => JSON.stringify(value).replace(/</g, "\\u003c");
  const script = [
    "(function(){",
    `var S=${literal(scope)},B=${literal(baseUrl)},O=${literal(origins)};`,
    // This runs before the report's scripts. The first transferred port is
    // retained by the parent; the report cannot obtain the private send port
    // or replace it with an ordinary postMessage, even during user activation.
    "var c=new MessageChannel(),send=c.port1.postMessage.bind(c.port1);",
    "parent.postMessage({__multicaAttachment:1,type:'connect',scope:S},'*',[c.port2]);",
    "document.addEventListener('click',function(e){",
    "if(!e.isTrusted||e.defaultPrevented||e.button!==0)return;",
    "var t=e.target,a=t&&t.closest&&t.closest('a[href]');if(!a)return;",
    "var h=a.getAttribute('href');if(!h||h.length>2048)return;",
    "try{var u=new URL(h,B);if(O.indexOf(u.origin)<0||u.username||u.password)return;",
    "if(!/^\\/(?:[^/%]+\\/attachments\\/[a-f0-9-]{36}\\/preview|api\\/attachments\\/[a-f0-9-]{36}\\/download)$/i.test(u.pathname))return;",
    "e.preventDefault();e.stopImmediatePropagation();send({__multicaAttachment:1,scope:S,href:h})",
    "}catch(_){}} ,true);})();",
  ].join("");
  const prefix = `<script>${script}</script>`;
  const doctype = /^\s*(?:<!--[\s\S]*?-->\s*)*<!doctype[^>]*>/i.exec(html)?.[0];
  return doctype ? doctype + prefix + html.slice(doctype.length) : prefix + html;
}
