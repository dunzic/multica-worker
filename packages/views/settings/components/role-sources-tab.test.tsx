import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithI18n } from "../../test/i18n";

const queryFixtures = vi.hoisted(() => ({
  sources: [] as Array<Record<string, unknown>>,
  plans: [] as Array<Record<string, unknown>>,
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: (options: { queryKey: readonly unknown[] }) => ({
      data: options.queryKey.includes("plans")
        ? queryFixtures.plans
        : queryFixtures.sources,
      isLoading: false,
      isError: false,
    }),
  };
});

import { RoleSourcesTab } from "./role-sources-tab";

beforeEach(() => {
  queryFixtures.sources = [
    {
      id: "source-1",
      workspace_id: "workspace-1",
      runtime_id: "runtime-1",
      name: "AgentWaker production",
      kind: "agentwaker",
      adapter_version: "1.0.0",
      config_summary: { configured: true, attributes: [] },
      policy: {},
      state: "active",
      current_snapshot_digest: "sha256:1234567890abcdef1234567890abcdef",
      version: 3,
      created_at: "2026-08-13T00:00:00Z",
      updated_at: "2026-08-13T00:00:00Z",
    },
  ];
  queryFixtures.plans = [
    {
      source_id: "source-1",
      workspace_id: "workspace-1",
      created_by: "scanner",
      created_at: "2026-08-13T00:00:00Z",
      plan: {
        contract_version: "role-source-plan/v1",
        source_id: "source-1",
        to_snapshot_digest: "sha256:abcdef",
        plan_digest: "sha256:plan1234567890abcdef",
        applyable: false,
        summary: {
          create: 1,
          update: 0,
          unchanged: 0,
          archive_candidate: 0,
          blocked: 1,
        },
        blockers: [
          {
            code: "capability.external_write_not_supported",
            message: "External-write capabilities remain disabled.",
            global: false,
          },
        ],
        actions: [
          {
            ref: { kind: "capability_binding", id: "account-actions" },
            display_name: "Account actions",
            operation: "blocked",
            risk: "high",
            reason: "External-write authority is not enabled.",
          },
        ],
      },
    },
  ];
});

describe("RoleSourcesTab", () => {
  it("renders the immutable plan and blockers as a read-only audit surface", async () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(await screen.findByText("AgentWaker production")).toBeInTheDocument();
    expect(await screen.findByText("Blocked — no changes can be applied")).toBeInTheDocument();
    expect(screen.getByText("capability.external_write_not_supported")).toBeInTheDocument();
    expect(screen.getByText("Account actions")).toBeInTheDocument();
    expect(screen.getByText(/This preview is read-only/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve|apply/i })).not.toBeInTheDocument();
  });

  it("shows an explicit empty state without inventing configuration actions", () => {
    queryFixtures.sources = [];
    queryFixtures.plans = [];

    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("No role sources configured")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /create|configure/i })).not.toBeInTheDocument();
  });
});
