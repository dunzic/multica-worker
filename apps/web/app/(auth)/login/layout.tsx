import type { Metadata } from "next";
import { createFactBrandMetadata } from "@/lib/fact-brand-metadata";

export const metadata: Metadata = {
  ...createFactBrandMetadata("登录 FACT · 智臻纪元", "登录数字员工协作平台，与专业伙伴一起工作。"),
  robots: { index: false, follow: false },
};

export default function LoginLayout({ children }: { children: React.ReactNode }) {
  return children;
}
