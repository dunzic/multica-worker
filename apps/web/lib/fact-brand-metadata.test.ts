// @vitest-environment node
import { describe, expect, it } from "vitest";
import { createFactBrandMetadata, FACT_HOME_METADATA } from "./fact-brand-metadata";

describe("FACT entry metadata", () => {
  it("uses the company identity without inheriting community canonical or social metadata", () => {
    expect(FACT_HOME_METADATA.title).toEqual({ absolute: "FACT · 智臻纪元 | 数字员工协作平台" });
    expect(FACT_HOME_METADATA.openGraph).toMatchObject({ siteName: "FACT" });
    expect(FACT_HOME_METADATA.twitter).toEqual({
      card: "summary",
      title: "FACT · 智臻纪元 | 数字员工协作平台",
      description: FACT_HOME_METADATA.description,
    });
    expect(FACT_HOME_METADATA.alternates).toEqual({ canonical: null });
    expect(JSON.stringify(FACT_HOME_METADATA)).not.toMatch(/multica\.ai|multica_hq|fact\.tech/i);
  });

  it("uses a local entry-page icon and preserves a caller's authentication title", () => {
    const metadata = createFactBrandMetadata("登录 FACT · 智臻纪元", "登录工作台");
    expect(metadata.title).toEqual({ absolute: "登录 FACT · 智臻纪元" });
    expect(metadata.icons).toEqual({
      icon: [{ url: "/fact-mark.svg", type: "image/svg+xml" }],
      shortcut: ["/fact-mark.svg"],
    });
  });
});
