import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithI18n } from "../../test/i18n";

const queryFixtures = vi.hoisted(() => ({
  sources: [] as Array<Record<string, unknown>>,
  adapters: [] as Array<Record<string, unknown>>,
  runtimes: [] as Array<Record<string, unknown>>,
  plans: [] as Array<Record<string, unknown>>,
  impact: undefined as Record<string, unknown> | undefined,
  configurationReview: undefined as Record<string, unknown> | undefined,
  failures: [] as Array<Record<string, unknown>>,
  attestations: [] as Array<Record<string, unknown>>,
  legalHolds: [] as Array<Record<string, unknown>>,
  retention: undefined as Record<string, unknown> | undefined,
  latestScan: undefined as Record<string, unknown> | undefined,
  scans: [] as Array<Record<string, unknown>>,
  lifecycleEvents: [] as Array<Record<string, unknown>>,
  snapshotSummaries: [] as Array<Record<string, unknown>>,
  snapshotComparison: undefined as Record<string, unknown> | undefined,
  approvals: [] as Array<Record<string, unknown>>,
  applies: [] as Array<Record<string, unknown>>,
  secretTransfers: [] as Array<Record<string, unknown>>,
}));

const memberFixture = vi.hoisted(() => ({ role: "owner" }));
const featureFlags = vi.hoisted(() => ({ roleSourceApply: false }));
const toastMocks = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }));
const queryClientMocks = vi.hoisted(() => ({ invalidateQueries: vi.fn().mockResolvedValue(undefined) }));
const apiMocks = vi.hoisted(() => ({
  requestRoleSourceScan: vi.fn(),
  createRoleSource: vi.fn(),
  updateRoleSourceLifecycle: vi.fn(),
  createRoleSourceLegalHold: vi.fn(),
  releaseRoleSourceLegalHold: vi.fn(),
  updateRoleSourceRetentionPolicy: vi.fn(),
  createRoleSourcePlan: vi.fn(),
  createRoleSourceRollbackPlan: vi.fn(),
  listRoleSourcePlanApprovals: vi.fn(),
  createRoleSourcePlanApproval: vi.fn(),
  applyRoleSourcePlan: vi.fn(),
  listRoleSourceApplyHistory: vi.fn(),
  requestRoleSourceSecretTransfer: vi.fn(),
  listRoleSourceSecretTransfers: vi.fn(),
}));

vi.mock("sonner", () => ({ toast: toastMocks }));

vi.mock("@multica/core/api", () => ({
  api: apiMocks,
  errorCode: (error: unknown) => {
    if (!error || typeof error !== "object" || !("body" in error)) return undefined;
    const body = (error as { body?: unknown }).body;
    if (!body || typeof body !== "object" || !("code" in body)) return undefined;
    const code = (body as { code?: unknown }).code;
    return typeof code === "string" ? code : undefined;
  },
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "workspace-1", name: "Acme" }),
}));

vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ role: memberFixture.role, userId: "user-1", member: null, isLoading: false }),
}));

vi.mock("@multica/core/feature-flags", () => ({
  useFlag: (name: string, fallback: boolean) => name === "role_source_apply"
    ? featureFlags.roleSourceApply
    : fallback,
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQueryClient: () => queryClientMocks,
    useMutation: (options: { mutationFn: (requestKey: string) => Promise<unknown> }) => ({
      isPending: false,
      mutateAsync: options.mutationFn,
    }),
    useQuery: (options: { queryKey: readonly unknown[] }) => ({
      data: options.queryKey.includes("runtimes")
        ? queryFixtures.runtimes
        : options.queryKey.includes("adapters")
          ? queryFixtures.adapters
        : options.queryKey.includes("snapshot-comparison")
          ? queryFixtures.snapshotComparison
        : options.queryKey.includes("snapshot-summaries")
          ? queryFixtures.snapshotSummaries
        : options.queryKey.includes("configuration-review")
          ? queryFixtures.configurationReview
        : options.queryKey.includes("latest-scan")
        ? queryFixtures.latestScan
        : options.queryKey.includes("lifecycle-events")
          ? queryFixtures.lifecycleEvents
        : options.queryKey.includes("scans")
          ? queryFixtures.scans
        : options.queryKey.includes("impact")
        ? queryFixtures.impact
        : options.queryKey.includes("approvals")
          ? queryFixtures.approvals
        : options.queryKey.includes("applies")
            ? queryFixtures.applies
          : options.queryKey.includes("secret-transfers")
            ? queryFixtures.secretTransfers
        : options.queryKey.includes("apply-failures")
          ? queryFixtures.failures
          : options.queryKey.includes("legal-holds")
          ? queryFixtures.legalHolds
          : options.queryKey.includes("retention")
            ? queryFixtures.retention
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
  vi.clearAllMocks();
  queryClientMocks.invalidateQueries.mockResolvedValue(undefined);
  memberFixture.role = "owner";
  featureFlags.roleSourceApply = false;
  queryFixtures.approvals = [];
  queryFixtures.applies = [];
  queryFixtures.secretTransfers = [];
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
        attestation_status: "loaded",
        runtime_status: "online",
        attestation_id: "sha256:attestation1234567890",
        revision: "sha256:revision1234567890",
        observed_at: "2026-08-13T00:05:00Z",
        changed_at: "2026-08-13T00:05:00Z",
      },
    },
  ];
  queryFixtures.runtimes = [
    {
      id: "00000000-0000-4000-8000-000000000011",
      workspace_id: "workspace-1",
      daemon_id: "daemon-1",
      name: "Codex (build-host)",
      custom_name: "Build runtime",
      runtime_mode: "local",
      provider: "codex",
      launch_header: "codex",
      status: "online",
      device_info: "macOS",
      metadata: {},
      owner_id: "user-1",
      visibility: "private",
      last_seen_at: "2026-08-13T05:00:00Z",
      created_at: "2026-08-13T00:00:00Z",
      updated_at: "2026-08-13T05:00:00Z",
    },
  ];
  queryFixtures.adapters = [
    {
      kind: "agentwaker",
      display_name: "AgentWaker",
      adapter_version: "1.0.0",
      contract_version: "1.0",
      capabilities: {
        change_hints: true,
        secret_transfer: true,
        binary_artifacts: false,
        provenance: true,
      },
    },
  ];
  queryFixtures.latestScan = {
    id: "scan-1",
    source_id: "source-1",
    workspace_id: "workspace-1",
    status: "succeeded",
    expected_adapter_version: "1.0.0",
    snapshot_digest: `sha256:${"d".repeat(64)}`,
    error_code: null,
    requested_at: "2026-08-13T00:10:00Z",
    claimed_at: "2026-08-13T00:10:01Z",
    completed_at: "2026-08-13T00:10:02Z",
  };
  queryFixtures.scans = [
    queryFixtures.latestScan,
    {
      ...queryFixtures.latestScan,
      id: "scan-old",
      status: "failed",
      snapshot_digest: null,
      error_code: "remote_trust_invalid",
      requested_at: "2026-08-12T00:10:00Z",
      claimed_at: "2026-08-12T00:10:01Z",
      completed_at: "2026-08-12T00:10:02Z",
    },
  ];
  queryFixtures.lifecycleEvents = [
    {
      sequence: 7,
      event_type: "source_rebound",
      actor_type: "user",
      actor_id: "00000000-0000-4000-8000-000000000099",
      previous_state: "detached",
      state: "paused",
      previous_runtime_id: "00000000-0000-4000-8000-000000000010",
      runtime_id: "00000000-0000-4000-8000-000000000011",
      cancelled_scan_count: 2,
      cancelled_transfer_count: 1,
      event_digest: `sha256:${"e".repeat(64)}`,
      occurred_at: "2026-08-13T05:00:00Z",
    },
  ];
  queryFixtures.snapshotSummaries = [
    {
      snapshot_digest: `sha256:${"b".repeat(64)}`,
      manifest_digest: `sha256:${"c".repeat(64)}`,
      kind: "agentwaker",
      adapter_version: "1.0.0",
      revision: "commit-new",
      tree_digest: `sha256:${"d".repeat(64)}`,
      role_count: 2,
      capability_count: 1,
      diagnostic_count: 0,
      created_at: "2026-08-13T06:00:00Z",
    },
    {
      snapshot_digest: `sha256:${"a".repeat(64)}`,
      manifest_digest: `sha256:${"e".repeat(64)}`,
      kind: "agentwaker",
      adapter_version: "1.0.0",
      revision: "commit-old",
      tree_digest: `sha256:${"f".repeat(64)}`,
      role_count: 1,
      capability_count: 1,
      diagnostic_count: 0,
      created_at: "2026-08-12T06:00:00Z",
    },
  ];
  queryFixtures.snapshotComparison = {
    from_snapshot_digest: `sha256:${"a".repeat(64)}`,
    to_snapshot_digest: `sha256:${"b".repeat(64)}`,
    total_changes: 2,
    offset: 0,
    limit: 100,
    changes: [
      { object_kind: "role", object_id: "reviewer", display_name: "Reviewer", operation: "added" },
      { object_kind: "skill", object_id: "draft", parent_id: "writer", display_name: "Draft", operation: "changed" },
    ],
  };
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
  queryFixtures.configurationReview = {
    plan_digest: "sha256:plan1234567890abcdef",
    total_changes: 2,
    environment_count: 1,
    mcp_count: 1,
    offset: 0,
    limit: 100,
    changes: [
      { object_kind: "environment", role_id: "writer", object_id: "OPENAI_API_KEY", operation: "update" },
      { object_kind: "mcp", role_id: "writer", object_id: "browser", operation: "create" },
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
  queryFixtures.legalHolds = [
    {
      id: "hold-1",
      workspace_id: "workspace-1",
      source_id: "source-1",
      scope: "source",
      reason_code: "regulatory",
      reference_digest: `sha256:${"a".repeat(64)}`,
      created_by: "user-1",
      created_at: "2026-08-13T03:00:00Z",
      status: "active",
    },
  ];
  queryFixtures.retention = {
    policy: {
      workspace_id: "workspace-1",
      source_id: "source-1",
      version: 1,
      enabled: false,
      minimum_age_days: 90,
      keep_successful_snapshots: 10,
    },
    eligible_count: 1,
    estimated_bytes: 4096,
    uniquely_reclaimable_bytes: 1024,
    truncated: false,
    candidates: [
      {
        snapshot_digest: `sha256:${"c".repeat(64)}`,
        created_at: "2026-01-01T00:00:00Z",
        estimated_bytes: 4096,
      },
    ],
  };
});

describe("RoleSourcesTab", () => {
  it("renders the immutable plan and blockers as a read-only audit surface", async () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(await screen.findAllByText("AgentWaker production")).toHaveLength(2);
    expect(screen.getAllByText("Runtime config loaded")).toHaveLength(2);
    expect(screen.getByText(/Runtime revision:/)).toBeInTheDocument();
    expect(screen.getByText("Runtime loaded-config evidence")).toBeInTheDocument();
    expect(screen.getByText(/observations: 1/)).toBeInTheDocument();
    expect(await screen.findByText("Blocked — no changes can be applied")).toBeInTheDocument();
    expect(screen.getByText("capability.external_write_not_supported")).toBeInTheDocument();
    expect(screen.getByText("Account actions")).toBeInTheDocument();
    expect(screen.getByText("Environment and MCP change review")).toBeInTheDocument();
    expect(screen.getByText("OPENAI_API_KEY")).toBeInTheDocument();
    expect(screen.getByText("browser")).toBeInTheDocument();
    expect(screen.queryByText(/value_digest|definition_hash|https:\/\/|Authorization/)).not.toBeInTheDocument();
    expect(screen.getByText("Affected workers and tasks")).toBeInTheDocument();
    expect(screen.getByText("2 cancel on apply")).toBeInTheDocument();
    expect(screen.getByText("1 continue current version")).toBeInTheDocument();
    expect(screen.getByText("Researcher")).toBeInTheDocument();
    expect(screen.getByText("task-1")).toBeInTheDocument();
    expect(screen.getByText("Apply attempts that returned errors")).toBeInTheDocument();
    expect(screen.getByText("state_conflict")).toBeInTheDocument();
    expect(screen.getByText(/apply · materialization/)).toBeInTheDocument();
    expect(screen.getByText(/automatic receipt check did not confirm/)).toBeInTheDocument();
    expect(screen.getByText(/Plan approval and apply remain read-only/)).toBeInTheDocument();
    expect(screen.getByText("Read-only source scan")).toBeInTheDocument();
    expect(screen.getAllByText("Succeeded")).toHaveLength(2);
    expect(screen.getByText("Scan history")).toBeInTheDocument();
    expect(screen.getByText("remote_trust_invalid")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run read-only scan" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve|apply|retry|recover/i })).not.toBeInTheDocument();
  });

  it("guides failed apply recovery through evidence reconciliation without a direct retry action", async () => {
    featureFlags.roleSourceApply = true;
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText(/run a new read-only scan/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /retry apply/i })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review current plan" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh evidence" }));

    expect(queryClientMocks.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["role-sources", "workspace-1"] });
    expect(queryClientMocks.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["role-sources", "workspace-1", "applies", "source-1"] });
    expect(toastMocks.success).toHaveBeenCalledWith("Current source, scan, plan, receipt, and failure evidence refreshed.");
  });

  it("queues a read-only scan and does not expose approval or apply controls", async () => {
    apiMocks.requestRoleSourceScan.mockResolvedValue({
      ...queryFixtures.latestScan,
      id: "scan-2",
      status: "queued",
      snapshot_digest: null,
      completed_at: null,
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Run read-only scan" }));

    expect(apiMocks.requestRoleSourceScan).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      expect.stringMatching(/^role-source-scan-/),
    );
    expect(screen.queryByRole("button", { name: /approve|apply|retry|recover/i })).not.toBeInTheDocument();
  });

  it("generates a plan from the latest successful snapshot only when controlled apply is enabled", async () => {
    featureFlags.roleSourceApply = true;
    apiMocks.createRoleSourcePlan.mockResolvedValue(queryFixtures.plans[0]);
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Generate plan from latest scan" }));

    expect(apiMocks.createRoleSourcePlan).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      { target_snapshot_digest: `sha256:${"d".repeat(64)}` },
    );
  });

  it("creates a forward rollback plan only after confirming a historical successful receipt", async () => {
    featureFlags.roleSourceApply = true;
    const historicalDigest = `sha256:${"a".repeat(64)}`;
    queryFixtures.applies = [{
      id: "apply-old",
      status: "succeeded",
      completed_at: "2026-08-12T04:00:00Z",
      receipt: {
        receipt_digest: `sha256:${"e".repeat(64)}`,
        snapshot_digest: historicalDigest,
        counts: { created: 1, updated: 0, unchanged: 0, archived: 0, retained: 0 },
      },
    }];
    apiMocks.createRoleSourceRollbackPlan.mockResolvedValue({
      ...queryFixtures.plans[0],
      plan: {
        ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
        mode: "rollback",
        to_snapshot_digest: historicalDigest,
      },
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Create rollback plan" }));
    expect(screen.getByRole("heading", { name: "Create a forward rollback plan?" })).toBeInTheDocument();
    expect(apiMocks.createRoleSourceRollbackPlan).not.toHaveBeenCalled();
    await user.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Create rollback plan" }));

    expect(apiMocks.createRoleSourceRollbackPlan).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      { target_snapshot_digest: historicalDigest },
    );
  });

  it("does not offer rollback to the currently active snapshot", () => {
    featureFlags.roleSourceApply = true;
    queryFixtures.applies = [{
      id: "apply-current",
      status: "succeeded",
      completed_at: "2026-08-13T04:00:00Z",
      receipt: {
        receipt_digest: `sha256:${"e".repeat(64)}`,
        snapshot_digest: queryFixtures.sources[0]!.current_snapshot_digest,
        counts: { created: 0, updated: 0, unchanged: 1, archived: 0, retained: 0 },
      },
    }];
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.queryByRole("button", { name: "Create rollback plan" })).not.toBeInTheDocument();
  });

  it("requires an explicit decision for every archive candidate and submits them in canonical order", async () => {
    featureFlags.roleSourceApply = true;
    queryFixtures.plans[0]!.plan = {
      ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
      applyable: true,
      summary: { create: 0, update: 0, unchanged: 0, archive_candidate: 2, blocked: 0 },
      blockers: [],
      actions: [
        {
          ref: { kind: "role", id: "zeta" },
          display_name: "Zeta",
          operation: "archive_candidate",
          risk: "high",
          reason: "Removed from source.",
        },
        {
          ref: { kind: "role", id: "alpha" },
          display_name: "Alpha",
          operation: "archive_candidate",
          risk: "high",
          reason: "Removed from source.",
        },
      ],
    };
    apiMocks.createRoleSourcePlanApproval.mockResolvedValue({
      id: "approval-new",
      source_id: "source-1",
      workspace_id: "workspace-1",
      plan_digest: "sha256:plan1234567890abcdef",
      decision: "approved",
      actor_user_id: "user-1",
      created_at: "2026-08-13T04:00:00Z",
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    const approve = screen.getByRole("button", { name: "Approve exact plan" });
    expect(approve).toBeDisabled();
    const alphaChoice = screen.getByRole("combobox", { name: "Alpha" });
    await user.click(alphaChoice);
    await user.click(await screen.findByRole("option", { name: "Retain existing object" }));
    expect(approve).toBeDisabled();
    const zetaChoice = screen.getByRole("combobox", { name: "Zeta" });
    await user.click(zetaChoice);
    const archiveOption = await screen.findByRole("option", { name: "Archive existing object" });
    await user.click(archiveOption);
    expect(approve).toBeEnabled();
    await user.click(approve);

    expect(apiMocks.createRoleSourcePlanApproval).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      "sha256:plan1234567890abcdef",
      expect.objectContaining({
        decision: "approved",
        decisions: {
          contract_version: "role-source-plan/v1",
          archives: [
            { ref: { kind: "role", id: "alpha" }, decision: "retain" },
            { ref: { kind: "role", id: "zeta" }, decision: "archive" },
          ],
          adoptions: [],
        },
      }),
    );
  });

  it("requires explicit adoption of the immutable target and allows undo before approval", async () => {
    featureFlags.roleSourceApply = true;
    queryFixtures.plans[0]!.plan = {
      ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
      applyable: true,
      summary: { create: 1, update: 0, unchanged: 0, archive_candidate: 0, blocked: 0 },
      blockers: [],
      actions: [{
        ref: { kind: "skill", parent_id: "writer", id: "draft" },
        display_name: "Draft",
        operation: "create",
        risk: "high",
        reason: "One unmanaged same-name target exists.",
        adoption_candidate: {
          target_kind: "skill",
          target_id: "00000000-0000-4000-8000-000000000051",
          version_commitment: `sha256:${"c".repeat(64)}`,
        },
      }],
    };
    apiMocks.createRoleSourcePlanApproval.mockResolvedValue({
      id: "approval-adoption",
      decision: "approved",
      created_at: "2026-08-13T04:00:00Z",
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    const approve = screen.getByRole("button", { name: "Approve exact plan" });
    const confirm = screen.getByRole("button", { name: "Confirm adoption" });
    expect(approve).toBeDisabled();
    await user.click(confirm);
    expect(approve).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Undo confirmation" }));
    expect(approve).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Confirm adoption" }));
    await user.click(approve);

    expect(apiMocks.createRoleSourcePlanApproval).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      "sha256:plan1234567890abcdef",
      expect.objectContaining({
        decisions: {
          contract_version: "role-source-plan/v1",
          archives: [],
          adoptions: [{
            ref: { kind: "skill", parent_id: "writer", id: "draft" },
            target_kind: "skill",
            target_id: "00000000-0000-4000-8000-000000000051",
            version_commitment: `sha256:${"c".repeat(64)}`,
          }],
        },
      }),
    );
  });

  it("pages large archive reviews and still requires a decision for every candidate", async () => {
    featureFlags.roleSourceApply = true;
    const actions = Array.from({ length: 51 }, (_, index) => ({
      ref: { kind: "skill", parent_id: "writer", id: `skill-${String(index).padStart(2, "0")}` },
      display_name: `Skill ${String(index).padStart(2, "0")}`,
      operation: "archive_candidate",
      risk: "high",
      reason: "Removed from source.",
    }));
    queryFixtures.plans[0]!.plan = {
      ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
      applyable: true,
      summary: { create: 0, update: 0, unchanged: 0, archive_candidate: actions.length, blocked: 0 },
      blockers: [],
      actions,
    };
    apiMocks.createRoleSourcePlanApproval.mockResolvedValue({
      id: "approval-large",
      decision: "approved",
      created_at: "2026-08-13T04:00:00Z",
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("0 of 51 decisions complete · page 1 of 2")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Skill 00" })).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Skill 50" })).not.toBeInTheDocument();
    const approve = screen.getByRole("button", { name: "Approve exact plan" });
    await user.click(screen.getByRole("button", { name: "Retain this page" }));
    expect(screen.getByText("50 of 51 decisions complete · page 1 of 2")).toBeInTheDocument();
    expect(approve).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next candidates" }));
    expect(screen.getByRole("combobox", { name: "Skill 50" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Archive this page" }));
    expect(screen.getByText("51 of 51 decisions complete · page 2 of 2")).toBeInTheDocument();
    expect(approve).toBeEnabled();
    await user.click(approve);

    expect(apiMocks.createRoleSourcePlanApproval).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      "sha256:plan1234567890abcdef",
      expect.objectContaining({
        decisions: expect.objectContaining({
          archives: [
            expect.objectContaining({ ref: expect.objectContaining({ id: "skill-00" }), decision: "retain" }),
            ...Array.from({ length: 49 }, () => expect.anything()),
            expect.objectContaining({ ref: expect.objectContaining({ id: "skill-50" }), decision: "archive" }),
          ],
        }),
      }),
    );
  });

  it("requires a second confirmation and reuses the apply idempotency key after an ambiguous failure", async () => {
    featureFlags.roleSourceApply = true;
    queryFixtures.plans[0]!.plan = {
      ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
      applyable: true,
      summary: { create: 1, update: 0, unchanged: 0, archive_candidate: 0, blocked: 0 },
      blockers: [],
      actions: [],
    };
    queryFixtures.approvals = [{
      id: "approval-1",
      decision: "approved",
      created_at: "2026-08-13T04:00:00Z",
    }];
    apiMocks.applyRoleSourcePlan
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({ id: "apply-1", status: "succeeded" });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Apply approved plan" }));
    expect(screen.getByRole("heading", { name: "Apply this approved role-source plan?" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm and apply" }));
    await user.click(screen.getByRole("button", { name: "Confirm and apply" }));

    const firstRequest = apiMocks.applyRoleSourcePlan.mock.calls[0]?.[3];
    const secondRequest = apiMocks.applyRoleSourcePlan.mock.calls[1]?.[3];
    expect(firstRequest.approval_id).toBe("approval-1");
    expect(firstRequest.request_key).toBe(secondRequest.request_key);
  });

  it("requests daemon-managed secret transfers and keeps apply disabled until every role is submitted", async () => {
    featureFlags.roleSourceApply = true;
    queryFixtures.plans[0]!.plan = {
      ...(queryFixtures.plans[0]!.plan as Record<string, unknown>),
      applyable: true,
      summary: { create: 1, update: 0, unchanged: 0, archive_candidate: 0, blocked: 0 },
      blockers: [],
      actions: [{
        ref: { kind: "role", id: "writer" },
        display_name: "Writer",
        needs_secret_transfer: true,
        operation: "create",
        risk: "low",
        after_digest: `sha256:${"a".repeat(64)}`,
        reason: "New role.",
      }],
    };
    queryFixtures.approvals = [{ id: "approval-1", decision: "approved", created_at: "2026-08-13T04:00:00Z" }];
    apiMocks.requestRoleSourceSecretTransfer.mockResolvedValue({
      id: "transfer-1",
      role_id: "writer",
      status: "pending",
      expires_at: "2099-08-13T04:15:00Z",
      created_at: "2026-08-13T04:00:00Z",
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByRole("button", { name: "Apply approved plan" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Request secure transfer" }));

    expect(apiMocks.requestRoleSourceSecretTransfer).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      "sha256:plan1234567890abcdef",
      expect.objectContaining({ approval_id: "approval-1", role_id: "writer" }),
    );
    expect(screen.queryByText(/do-not-expose|BEGIN PRIVATE KEY/i)).not.toBeInTheDocument();
  });

  it("reuses the scan idempotency key after an ambiguous response failure", async () => {
    apiMocks.requestRoleSourceScan
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({ ...queryFixtures.latestScan, status: "queued" });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Run read-only scan" }));
    await user.click(screen.getByRole("button", { name: "Run read-only scan" }));

    const firstKey = apiMocks.requestRoleSourceScan.mock.calls[0]?.[2];
    const secondKey = apiMocks.requestRoleSourceScan.mock.calls[1]?.[2];
    expect(firstKey).toBe(secondKey);
  });

  it("explains when a scan request key belongs to another operator", async () => {
    apiMocks.requestRoleSourceScan.mockRejectedValue({
      body: { code: "role_source_scan_request_conflict" },
    });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Run read-only scan" }));

    expect(toastMocks.error).toHaveBeenCalledWith(
      "This scan request belongs to another operator. Refresh the status before retrying.",
    );
  });

  it("shows scan status to members but not the request control", () => {
    memberFixture.role = "member";
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getAllByText("Succeeded")).toHaveLength(2);
    expect(screen.getByText("Only workspace owners and admins can request a scan.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Run read-only scan" })).not.toBeInTheDocument();
  });

  it("filters scan history and maps stable failure families to safe recovery guidance", async () => {
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText(/Verify the publisher identity, active signing key and revision policy/)).toBeInTheDocument();
    const statusFilter = screen.getByRole("combobox", { name: "Status" });
    await user.click(statusFilter);
    await user.click(await screen.findByRole("option", { name: "Succeeded" }));
    expect(screen.queryByText("remote_trust_invalid")).not.toBeInTheDocument();
    expect(screen.getAllByText(/sha256:dddddd/).length).toBeGreaterThan(0);

    await user.click(statusFilter);
    await user.click(await screen.findByRole("option", { name: "All statuses" }));
    await user.type(screen.getByRole("textbox", { name: "Error code" }), "trust");
    expect(screen.getByText("remote_trust_invalid")).toBeInTheDocument();
    expect(screen.getAllByText(/sha256:dddddd/)).toHaveLength(1);
    expect(screen.getByText(/Verify the publisher identity, active signing key and revision policy/)).toBeInTheDocument();
  });

  it("compares immutable snapshot versions without rendering manifest content", async () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(await screen.findByText("Snapshot version comparison")).toBeInTheDocument();
    expect(screen.getByText("2 object changes")).toBeInTheDocument();
    expect(screen.getByText("Reviewer")).toBeInTheDocument();
    expect(screen.getByText("Added")).toBeInTheDocument();
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(screen.getByText(/Manifest bodies, artifact paths, environment keys, and MCP configuration stay on the server/)).toBeInTheDocument();
    expect(screen.queryByText(/instructions|SECRET_NAME|private\/|mcp_servers/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("disables scan requests while a scan is active", () => {
    queryFixtures.latestScan = { ...queryFixtures.latestScan, status: "claimed", completed_at: null };
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByRole("button", { name: "Run read-only scan" })).toBeDisabled();
    expect(screen.getByText("Running")).toBeInTheDocument();
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

  it("does not present historical loaded evidence as current health when the runtime is unavailable", async () => {
    queryFixtures.sources[0]!.runtime_config = {
      status: "runtime_unavailable",
      runtime_status: "offline",
      attestation_status: "loaded",
      attestation_id: "sha256:attestation1234567890",
      revision: "sha256:revision1234567890",
      observed_at: "2026-08-13T00:05:00Z",
      changed_at: "2026-08-13T00:05:00Z",
    };

    renderWithI18n(<RoleSourcesTab />);

    expect(await screen.findByText("Runtime offline or stale")).toBeInTheDocument();
    expect(screen.getAllByText("Runtime config loaded")).toHaveLength(2);
  });

  it("offers pause for an active source and never skips directly to detach", () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByRole("button", { name: "Pause" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Detach" })).not.toBeInTheDocument();
  });

  it("shows a bounded safe lifecycle audit projection", () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("Source rebound")).toBeInTheDocument();
    expect(screen.getByText(/detached → paused · user: 00000000-000…00000099/)).toBeInTheDocument();
    expect(screen.getByText(/Runtime: 00000000-000…00000010 → 00000000-000…00000011/)).toBeInTheDocument();
    expect(screen.getByText("Cancelled scans: 2 · transfers: 1")).toBeInTheDocument();
    expect(screen.queryByText(/adapter_kind|plan_digest|receipt_digest|payload/)).not.toBeInTheDocument();
  });

  it("requires paused state before detach and exposes resume separately", () => {
    queryFixtures.sources[0] = { ...queryFixtures.sources[0], state: "paused" };
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Detach" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
  });

  it("only offers rebind after detach", () => {
    queryFixtures.sources[0] = { ...queryFixtures.sources[0], state: "detached" };
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByRole("button", { name: "Rebind" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Resume" })).not.toBeInTheDocument();
  });

  it("guides rebind through registered workspace runtimes instead of a raw UUID field", async () => {
    queryFixtures.sources[0] = { ...queryFixtures.sources[0], state: "detached" };
    apiMocks.updateRoleSourceLifecycle.mockResolvedValue({ ...queryFixtures.sources[0], state: "paused" });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Rebind" }));
    expect(screen.queryByRole("textbox", { name: "Destination runtime ID" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Destination runtime ID" }));
    await user.click(await screen.findByRole("option", { name: "Build runtime (Codex) · online" }));
    await user.type(screen.getByRole("textbox", { name: "Daemon config handle" }), "agentwaker-production");
    await user.click(screen.getByRole("button", { name: "Rebind" }));

    expect(apiMocks.updateRoleSourceLifecycle).toHaveBeenCalledWith(
      "workspace-1",
      "source-1",
      {
        action: "rebind",
        expected_version: 3,
        runtime_id: "00000000-0000-4000-8000-000000000011",
        daemon_config_id: "agentwaker-production",
      },
    );
  });

  it("registers a source from trusted metadata without collecting private configuration", async () => {
    const created = {
      ...queryFixtures.sources[0],
      id: "source-created",
      state: "registered",
      runtime_config: {
        status: "unattested",
        attestation_status: "unattested",
        runtime_status: "online",
        attestation_id: null,
        revision: null,
        observed_at: null,
        changed_at: null,
      },
    };
    apiMocks.createRoleSource.mockResolvedValue(created);
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Register source" }));
    expect(screen.getByText(/Never paste a filesystem path/)).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /path|credential|token|signing/i })).not.toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Runtime" }));
    await user.click(await screen.findByRole("option", { name: "Build runtime (Codex) · online" }));
    await user.click(screen.getByRole("combobox", { name: "Trusted adapter" }));
    await user.click(await screen.findByRole("option", { name: "AgentWaker · 1.0.0" }));
    await user.type(screen.getByRole("textbox", { name: "Source display name" }), "Production roles");
    await user.type(screen.getByRole("textbox", { name: "Daemon config handle" }), "agentwaker-production");
    await user.click(screen.getByRole("button", { name: "Register and verify" }));

    expect(apiMocks.createRoleSource).toHaveBeenCalledWith("workspace-1", {
      runtime_id: "00000000-0000-4000-8000-000000000011",
      name: "Production roles",
      kind: "agentwaker",
      adapter_version: "1.0.0",
      daemon_config_id: "agentwaker-production",
      config_summary: { configured: true, attributes: [] },
      policy: {},
    });
  });

  it("keeps scanning disabled until the selected Runtime config is attested as loaded", () => {
    queryFixtures.sources[0] = {
      ...queryFixtures.sources[0],
      runtime_config: {
        ...(queryFixtures.sources[0]?.runtime_config as Record<string, unknown>),
        status: "unattested",
      },
    };
    renderWithI18n(<RoleSourcesTab />);
    expect(screen.getByRole("button", { name: "Run read-only scan" })).toBeDisabled();
  });

  it("shows owner-only legal holds as retention controls, not lifecycle controls", () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("Legal holds")).toBeInTheDocument();
    expect(screen.getByText("Regulatory requirement")).toBeInTheDocument();
    expect(screen.getByText(/does not pause scanning or applying changes/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create legal hold" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Release hold" })).toBeInTheDocument();
  });

  it("shows an owner-only retention preview without an immediate delete action", () => {
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.getByText("Historical retention")).toBeInTheDocument();
    expect(screen.getByText("Pruning disabled")).toBeInTheDocument();
    expect(screen.getByText(/1 snapshots currently eligible/)).toBeInTheDocument();
    expect(screen.getByText(/Projected uniquely reclaimable artifacts.*1\.0 KiB/)).toBeInTheDocument();
    expect(screen.getByText(/not realized storage savings/)).toBeInTheDocument();
    expect(screen.getByText(/rechecks legal holds, task pins/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit policy" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /delete snapshot|prune now/i })).not.toBeInTheDocument();
  });

  it("reuses the same retention idempotency key after an ambiguous request failure", async () => {
    apiMocks.updateRoleSourceRetentionPolicy
      .mockRejectedValueOnce(new Error("response lost"))
      .mockResolvedValueOnce({ version: 2 });
    const user = userEvent.setup();
    renderWithI18n(<RoleSourcesTab />);

    await user.click(screen.getByRole("button", { name: "Edit policy" }));
    await user.click(screen.getByRole("button", { name: "Save policy revision" }));
    await screen.findByText("Update historical retention policy");
    await user.click(screen.getByRole("button", { name: "Save policy revision" }));

    expect(apiMocks.updateRoleSourceRetentionPolicy).toHaveBeenCalledTimes(2);
    const first = apiMocks.updateRoleSourceRetentionPolicy.mock.calls[0]?.[2];
    const second = apiMocks.updateRoleSourceRetentionPolicy.mock.calls[1]?.[2];
    expect(first.request_key).toBe(second.request_key);
  });

  it("does not expose legal-hold records or controls to workspace admins", () => {
    memberFixture.role = "admin";
    renderWithI18n(<RoleSourcesTab />);

    expect(screen.queryByText("Legal holds")).not.toBeInTheDocument();
    expect(screen.queryByText("Historical retention")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create legal hold" })).not.toBeInTheDocument();
  });
});
