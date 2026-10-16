import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import zhAuth from "@multica/views/locales/zh-Hans/auth.json";
import { MulticaLanding } from "./multica-landing";

const state = vi.hoisted(() => ({ user: null as null | { id: string }, setLocale: vi.fn(), href: "/login" }));
vi.mock("next/link", () => ({ default: ({ children, ...props }: { children: ReactNode; href: string }) => <a {...props}>{children}</a> }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: Object.assign((selector: (value: typeof state) => unknown) => selector(state), { getState: () => state }) }));
vi.mock("../i18n", () => ({ useLocale: () => ({ locale: "zh-Hans", setLocale: state.setLocale }), localeLabels: { "zh-Hans": "中文", en: "EN", ja: "日本語", ko: "한국어", fr: "FR" } }));
vi.mock("../utils/use-dashboard-cta", () => ({ useDashboardCtaHref: () => state.href }));

function renderLanding() {
  return render(<I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { auth: zhAuth } }}><MulticaLanding /></I18nProvider>);
}

describe("FACT landing", () => {
  beforeEach(() => { state.user = null; state.href = "/login"; vi.clearAllMocks(); });

  it("introduces the company and distinguishes principles from delivered capabilities", () => {
    renderLanding();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("让数字员工，成为专业伙伴。");
    expect(screen.getAllByText("智臻纪元智能科技").length).toBeGreaterThan(0);
    expect(screen.getByText(/不代表所有能力均已实现或获得认证/)).toBeInTheDocument();
    expect(screen.getByText("协作流程示意")).toBeInTheDocument();
    expect(screen.getAllByRole("article")).toHaveLength(4);
    expect(screen.queryByText(/53k|free trial|won’t be human/i)).not.toBeInTheDocument();
  });

  it("keeps login and download local, with an explicit fork attribution", () => {
    renderLanding();
    for (const link of screen.getAllByRole("link", { name: "登录工作台" })) expect(link).toHaveAttribute("href", "/login");
    expect(screen.getByRole("link", { name: "下载客户端" })).toHaveAttribute("href", "/download");
    expect(screen.getByRole("link", { name: "基于 Multica 构建" })).toHaveAttribute("href", "https://github.com/dunzic/multica-worker");
  });

  it("uses the existing workspace destination for signed-in users", () => {
    state.user = { id: "test-user" }; state.href = "/test-workspace/issues";
    renderLanding();
    for (const link of screen.getAllByRole("link", { name: "进入工作台" })) expect(link).toHaveAttribute("href", state.href);
  });

  it("preserves the locale selector and keyboard-accessible section links", async () => {
    renderLanding();
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "语言" }), "en");
    expect(state.setLocale).toHaveBeenCalledWith("en");
    expect(screen.getByRole("link", { name: "FACT 专业原则" })).toHaveAttribute("href", "#principles");
    expect(screen.getByRole("link", { name: "跳到主要内容" })).toHaveAttribute("href", "#fact-main");
  });
});
