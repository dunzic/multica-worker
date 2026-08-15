// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const deliveries = vi.hoisted(() => ({ current: [] as Array<Record<string, unknown>> }));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: () => ({ data: deliveries.current, isLoading: false, isError: false }),
  };
});

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

import { ChannelDeliveryAudit } from "./channel-delivery-audit";

afterEach(cleanup);

beforeEach(() => {
  deliveries.current = [
    {
      id: "delivery-1",
      workspace_id: "workspace-1",
      task_id: "00000000-0000-4000-8000-000000000001",
      chat_session_id: "chat-secret-route",
      channel_type: "slack",
      channel_chat_id: "channel-secret-route",
      operation_kind: "chat_reply",
      correlation_id: "00000000-0000-4000-8000-000000000002",
      payload_digest: `sha256:${"a".repeat(64)}`,
      status: "readback",
      attempt_count: 1,
      external_message_id: "provider-message-secret",
      evidence_digest: `sha256:${"b".repeat(64)}`,
      evidence: {
        delivered_at: "2026-08-13T06:00:00Z",
        readback_at: "2026-08-13T06:01:00Z",
      },
      created_at: "2026-08-13T06:00:00Z",
      updated_at: "2026-08-13T06:01:00Z",
    },
    {
      id: "delivery-2",
      workspace_id: "workspace-1",
      task_id: "task-2",
      chat_session_id: "chat-2",
      channel_type: "dingtalk",
      channel_chat_id: "channel-2",
      operation_kind: "failure_notice",
      correlation_id: "correlation-2",
      payload_digest: `sha256:${"c".repeat(64)}`,
      status: "failed",
      attempt_count: 2,
      last_error_code: "rate_limited",
      created_at: "2026-08-13T05:00:00Z",
      updated_at: "2026-08-13T05:01:00Z",
    },
    {
      id: "delivery-3",
      workspace_id: "workspace-1",
      task_id: "task-3",
      chat_session_id: "chat-ambiguous-secret",
      channel_type: "slack",
      channel_chat_id: "channel-ambiguous-secret",
      operation_kind: "chat_reply",
      correlation_id: "correlation-3",
      payload_digest: `sha256:${"d".repeat(64)}`,
      status: "ambiguous",
      attempt_count: 1,
      external_message_id: "provider-ambiguous-secret",
      evidence_digest: `sha256:${"e".repeat(64)}`,
      evidence: {
        external_message_id: "provider-ambiguous-secret",
        delivered_at: "",
        ambiguity_reason: "partial_delivery",
        ambiguous_at: "2026-08-13T05:02:00Z",
      },
      last_error_code: "partial_delivery",
      ambiguous_at: "2026-08-13T05:02:00Z",
      created_at: "2026-08-13T05:00:00Z",
      updated_at: "2026-08-13T05:02:00Z",
    },
  ];
});

function renderAudit() {
  return render(
    <I18nProvider locale="en" resources={{ en: { common: enCommon, settings: enSettings } }}>
      <ChannelDeliveryAudit />
    </I18nProvider>,
  );
}

describe("ChannelDeliveryAudit", () => {
  it("shows content-free evidence and describes readback as an explicit reply", () => {
    renderAudit();

    expect(screen.getByText("Explicit reply received")).toBeInTheDocument();
    expect(screen.getByText(/not a passive read receipt/)).toBeInTheDocument();
    expect(screen.getByText(/rate_limited/)).toBeInTheDocument();
    expect(screen.getByText("Acceptance unknown — resend blocked")).toBeInTheDocument();
    expect(screen.getByText(/Automatic resend is blocked/)).toBeInTheDocument();
    expect(screen.queryByText(/chat-secret-route|channel-secret-route|provider-message-secret|ambiguous-secret/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /retry|replay|resend/i })).not.toBeInTheDocument();
  });

  it("filters the bounded evidence list by status", async () => {
    const user = userEvent.setup();
    renderAudit();

    const filter = screen.getByRole("combobox", { name: "Status" });
    await user.click(filter);
    await user.click(await screen.findByRole("option", { name: "Delivery failed" }));
    expect(screen.getByText(/rate_limited/)).toBeInTheDocument();
    expect(screen.queryByText(/not a passive read receipt/)).not.toBeInTheDocument();
  });
});
