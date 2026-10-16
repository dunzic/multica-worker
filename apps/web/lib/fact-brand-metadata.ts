import type { Metadata } from "next";

/** Scope the company identity to entry pages without changing app/service URLs. */
export function createFactBrandMetadata(title: string, description: string): Metadata {
  return {
    title: { absolute: title },
    description,
    icons: {
      icon: [{ url: "/fact-mark.svg", type: "image/svg+xml" }],
      shortcut: ["/fact-mark.svg"],
    },
    openGraph: { title, description, siteName: "FACT", type: "website" },
    twitter: { card: "summary", title, description },
    // Do not inherit the community site's canonical URL or social accounts.
    alternates: { canonical: null },
  };
}

export const FACT_HOME_METADATA = createFactBrandMetadata(
  "FACT · 智臻纪元 | 数字员工协作平台",
  "面向专业服务的可信 AI。让数字员工成为专业伙伴，以智能赋能专业，以事实支撑判断。",
);
