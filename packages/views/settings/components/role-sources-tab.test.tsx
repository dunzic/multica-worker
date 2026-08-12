import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithI18n } from "../../test/i18n";

const queryFixtures = vi.hoisted(() => ({
  sources: [] as Array<Record<string, unknown>>,
  plans: [] as Array<Record<string, unknown>>,
  impact: undefined as Record<string, unknown> | undefined,
  failures: [] as Array<Record<string, unknown>>,
  attestations: [] as Array<Record<string, unknown>>,
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
      data: options.queryKey.includes("impact")
        ? queryFixtures.impact
        : options.queryKey.includes("apply-failures")
          ? queryFixtures.failures
          : options.queryKey.includes("runtime-attestations")
            ? queryFixtures.attestations
            : options.queryKey.includes("plans")
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
      runtime_config: {
        status: "loaded",
        attestation_id: "sha256:attestation1234567890",
        revision: "sha256:revision1234567890",
        observed_at: "2026-08-13T00:05:00Z",
        changed_at: "2026-08-13T00:05:00Z",
      },
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
  queryFixtures.impact = {
    contract_version: "1.0",
    source_id: "source-1",
    plan_digest: "sha256:plan1234567890abcdef",
    target_snapshot_digest: "sha256:abcdef",
    applyable: false,
    generated_at: "2026-08-13T00:00:00Z",
    summary: {
      new_roles: 0,
      mandatory_refresh_roles: 1,
      conditional_archive_roles: 1,
      unmapped_existing_roles: 0,
      cancel_on_apply: 2,
      conditional_cancel_on_archive: 1,
      continue_current_version: 1,
      worker_details_truncated: false,
      task_details_truncated: false,
    },
    workers: [
      {
        source_role_id: "researcher",
        agent_id: "agent-1",
        agent_name: "Researcher",
        effect: "provenance_refresh",
        pre_start_tasks: 2,
        running_tasks: 1,
        current_snapshot_digest: "sha256:old",
      },
    ],
    tasks: [
      {
        task_id: "task-1",
        source_role_id: "researcher",
        agent_id: "agent-1",
        status: "queued",
        effect: "cancel_on_apply",
        created_at: "2026-08-13T00:00:00Z",
      },
    ],
  };
  queryFixtures.failures = [
    {
      id: "failure-1",
      source_id: "source-1",
      workspace_id: "workspace-1",
      plan_digest: "sha256:plan1234567890abcdef",
      approval_id: "approval-1",
      actor_user_id: "user-1",
      mode: "apply",
      failure_stage: "materialization",
      failure_code: "state_conflict",
      occurred_at: "2026-08-13T02:00:00Z",
    },
  ];
  queryFixtures.attestations = [
    {
      status: "loaded",
      contract_version: "role-source-config-attestation-v1",
      loaded: true,
      attestation_id: "sha256:attestation1234567890",
      revision: "sha256:revision1234567890",
      first_observed_at: "2026-08-13T00:05:00Z",
      last_observed_at: "2026-08-13T00:05:00Z",
      observation_count: 1,
    },
  ];
});

describe("RoleSourcesTab", () => {
  it("renders the immutable plan and blockers as a read-only audit surface", async () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(await screen.findByText("AgentWaker production")).toBeInTheDocument();
    expect(screen.getAllByText("Runtime config loaded")).toHaveLength(2);
    expect(screen.getByText(/Runtime revision:/)).toBeInTheDocument();
    expect(screen.getByText("Runtime loaded-config evidence")).toBeInTheDocument();
    expect(screen.getByText(/observations: 1/)).toBeInTheDocument();
    expect(await screen.findByText("Blocked — no changes can be applied")).toBeInTheDocument();
    expect(screen.getByText("capability.external_write_not_supported")).toBeInTheDocument();
    expect(screen.getByText("Account actions")).toBeInTheDocument();
    expect(screen.getByText("Affected workers and tasks")).toBeInTheDocument();
    expect(screen.getByText("2 cancel on apply")).toBeInTheDocument();
    expect(screen.getByText("1 continue current version")).toBeInTheDocument();
    expect(screen.getByText("Researcher")).toBeInTheDocument();
    expect(screen.getByText("task-1")).toBeInTheDocument();
    expect(screen.getByText("Apply attempts that returned errors")).toBeInTheDocument();
    expect(screen.getByText("state_conflict")).toBeInTheDocument();
    expect(screen.getByText(/apply · materialization/)).toBeInTheDocument();
    expect(screen.getByText(/automatic receipt check did not confirm/)).toBeInTheDocument();
    expect(screen.getByText(/This preview is read-only/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve|apply|retry|recover/i })).not.toBeInTheDocument();
  });

  it("shows an explicit empty state without inventing configuration actions", () => {
    queryFixtures.sources = [];
    queryFixtures.plans = [];
    queryFixtures.impact = undefined;
    queryFixtures.failures = [];
    queryFixtures.attestations = [];

    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("No role sources configured")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /create|configure/i })).not.toBeInTheDocument();
  });
});
