"use client";

import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { AlertTriangle, CheckCircle2, CircleSlash2, Loader2, PauseCircle, PlayCircle, RefreshCw, Repeat2, ShieldAlert, Unplug } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import {
  roleSourceApplyFailureListOptions,
  roleSourceApplyHistoryListOptions,
  roleSourceLegalHoldListOptions,
  roleSourceLifecycleEventListOptions,
  roleSourceLatestScanOptions,
  roleSourceListOptions,
  roleSourcePlanImpactOptions,
  roleSourcePlanApprovalListOptions,
  roleSourcePlanListOptions,
  roleSourceRuntimeAttestationListOptions,
  roleSourceScanListOptions,
  roleSourceSnapshotComparisonOptions,
  roleSourceSnapshotSummaryListOptions,
  roleSourceSecretTransferListOptions,
  roleSourceRetentionPreviewOptions,
  roleSourceKeys,
  useApplyRoleSourcePlan,
  useCreateRoleSourcePlan,
  useCreateRoleSourceRollbackPlan,
  useCreateRoleSourcePlanApproval,
  useRequestRoleSourceScan,
  useRequestRoleSourceSecretTransfer,
  type RoleSourceArchiveDecision,
  type RoleSourceLifecycleAction,
  type RoleSourceLegalHold,
  type RoleSourceLegalHoldReason,
  type RoleSourceLegalHoldReleaseReason,
  type RoleSourceLegalHoldScope,
  type RoleSourcePlanAction,
} from "@multica/core/role-sources";
import { api, errorCode } from "@multica/core/api";
import { useFlag } from "@multica/core/feature-flags";
import { useCurrentMember } from "@multica/core/permissions";
import { useCurrentWorkspace } from "@multica/core/paths";
import { runtimeDisplayLabel, runtimeListOptions } from "@multica/core/runtimes";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

function shortDigest(value: string | null | undefined) {
  if (!value) return "—";
  return `${value.slice(0, 14)}…${value.slice(-8)}`;
}

function shortIdentifier(value: string | null | undefined) {
  if (!value) return "—";
  if (value.length <= 24) return value;
  return `${value.slice(0, 12)}…${value.slice(-8)}`;
}

function operationVariant(operation: RoleSourcePlanAction["operation"]) {
  if (operation === "blocked" || operation === "archive_candidate") return "destructive" as const;
  if (operation === "unchanged") return "outline" as const;
  return "secondary" as const;
}

function objectRefKey(ref: RoleSourcePlanAction["ref"]) {
  return `${ref.kind}\u0000${ref.parent_id ?? ""}\u0000${ref.id}`;
}

function runtimeConfigTranslationKey(status: string) {
  switch (status) {
    case "loaded":
      return "runtime_loaded" as const;
    case "not_loaded":
      return "runtime_not_loaded" as const;
    case "config_missing":
      return "runtime_config_missing" as const;
    case "kind_mismatch":
      return "runtime_kind_mismatch" as const;
    case "adapter_version_mismatch":
      return "runtime_version_mismatch" as const;
    case "invalid_attestation":
      return "runtime_invalid_attestation" as const;
    case "runtime_unavailable":
      return "runtime_unavailable" as const;
    default:
      return "runtime_unattested" as const;
  }
}

function scanStatusTranslationKey(status: string) {
  switch (status) {
    case "queued":
      return "scan_status_queued" as const;
    case "claimed":
      return "scan_status_claimed" as const;
    case "succeeded":
      return "scan_status_succeeded" as const;
    case "failed":
      return "scan_status_failed" as const;
    case "cancelled":
      return "scan_status_cancelled" as const;
    default:
      return "scan_status_unknown" as const;
  }
}

function lifecycleEventTranslationKey(eventType: string) {
  switch (eventType) {
    case "source_paused":
      return "lifecycle_event_source_paused" as const;
    case "source_resumed":
      return "lifecycle_event_source_resumed" as const;
    case "source_detached":
      return "lifecycle_event_source_detached" as const;
    case "source_rebound":
      return "lifecycle_event_source_rebound" as const;
    default:
      return "lifecycle_event_unknown" as const;
  }
}

function snapshotOperationVariant(operation: string) {
  if (operation === "removed") return "destructive" as const;
  if (operation === "added") return "secondary" as const;
  return "outline" as const;
}

function snapshotOperationTranslationKey(operation: string) {
  switch (operation) {
    case "added": return "snapshot_operation_added" as const;
    case "removed": return "snapshot_operation_removed" as const;
    default: return "snapshot_operation_changed" as const;
  }
}

function scanRecoveryTranslationKey(code: string) {
  switch (code) {
    case "remote_unavailable":
    case "artifact_preflight_failed":
    case "artifact_upload_failed":
    case "scan_timeout":
      return "scan_recovery_retry" as const;
    case "remote_trust_invalid":
      return "scan_recovery_trust" as const;
    case "config_not_found":
    case "adapter_not_supported":
    case "adapter_version_mismatch":
    case "scanner_unavailable":
      return "scan_recovery_runtime" as const;
    case "remote_content_invalid":
    case "source_invalid":
    case "artifact_manifest_invalid":
    case "artifact_preflight_invalid":
    case "source_changed":
      return "scan_recovery_source" as const;
    default:
      return "scan_recovery_unknown" as const;
  }
}

function scanRequestErrorTranslationKey(error: unknown) {
  switch (errorCode(error)) {
    case "role_source_scan_already_active":
      return "scan_already_active" as const;
    case "role_source_scan_source_state":
      return "scan_source_state" as const;
    case "role_source_scan_request_conflict":
      return "scan_request_conflict" as const;
    default:
      return "scan_request_failed" as const;
  }
}

function applyFailureRecoveryTranslationKey(code: string, stage: string) {
  if (stage === "commit" || code === "request_cancelled") return "failed_apply_recovery_reconcile" as const;
  switch (code) {
    case "invalid_secret_transfer":
      return "failed_apply_recovery_secret" as const;
    case "capacity_exhausted":
    case "deadline_exceeded":
    case "dependency_unavailable":
    case "internal_failure":
      return "failed_apply_recovery_retry" as const;
    case "state_conflict":
    case "resource_not_found":
    case "materialization_blocked":
    case "invalid_request":
    default:
      return "failed_apply_recovery_rebuild" as const;
  }
}

const sha256DigestPattern = /^sha256:[0-9a-f]{64}$/;
const archiveDecisionPageSize = 50;

function legalHoldRequestKey(prefix: "create" | "release") {
  return `role-source-legal-hold-${prefix}-${globalThis.crypto.randomUUID()}`;
}

function formatRetentionBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  if (value < 1024 * 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
  return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GiB`;
}

export function RoleSourcesTab() {
  const { t } = useT("settings");
  const workspaceId = useCurrentWorkspace()?.id ?? "";
  const queryClient = useQueryClient();
  const { role } = useCurrentMember(workspaceId);
  const isOwner = role === "owner";
  const canManage = role === "owner" || role === "admin";
  const roleSourceApplyEnabled = useFlag("role_source_apply", false);
  const sources = useQuery({
    ...roleSourceListOptions(workspaceId),
    enabled: Boolean(workspaceId),
  });
  const runtimes = useQuery({
    ...runtimeListOptions(workspaceId),
    enabled: Boolean(workspaceId && canManage),
  });
  const [selectedId, setSelectedId] = React.useState("");
  const [pendingAction, setPendingAction] = React.useState<Exclude<RoleSourceLifecycleAction, "rebind"> | null>(null);
  const [rebindOpen, setRebindOpen] = React.useState(false);
  const [rebindRuntimeId, setRebindRuntimeId] = React.useState("");
  const [rebindConfigId, setRebindConfigId] = React.useState("");
  const [savingLifecycle, setSavingLifecycle] = React.useState(false);
  const [scanStatusFilter, setScanStatusFilter] = React.useState("all");
  const [scanCodeFilter, setScanCodeFilter] = React.useState("");
  const [snapshotFrom, setSnapshotFrom] = React.useState("");
  const [snapshotTo, setSnapshotTo] = React.useState("");
  const [snapshotOffset, setSnapshotOffset] = React.useState(0);
  const [createHoldOpen, setCreateHoldOpen] = React.useState(false);
  const [createHoldRequestKey, setCreateHoldRequestKey] = React.useState("");
  const [holdScope, setHoldScope] = React.useState<RoleSourceLegalHoldScope>("source");
  const [holdReason, setHoldReason] = React.useState<RoleSourceLegalHoldReason>("regulatory");
  const [holdSnapshotDigest, setHoldSnapshotDigest] = React.useState("");
  const [holdReferenceDigest, setHoldReferenceDigest] = React.useState("");
  const [holdToRelease, setHoldToRelease] = React.useState<RoleSourceLegalHold | null>(null);
  const [releaseHoldRequestKey, setReleaseHoldRequestKey] = React.useState("");
  const [releaseReason, setReleaseReason] = React.useState<RoleSourceLegalHoldReleaseReason>("resolved");
  const [releaseReferenceDigest, setReleaseReferenceDigest] = React.useState("");
  const [savingHold, setSavingHold] = React.useState(false);
  const [retentionDialogOpen, setRetentionDialogOpen] = React.useState(false);
  const [retentionRequestKey, setRetentionRequestKey] = React.useState("");
  const [retentionEnabled, setRetentionEnabled] = React.useState(false);
  const [retentionMinimumDays, setRetentionMinimumDays] = React.useState("90");
  const [retentionKeepSuccessful, setRetentionKeepSuccessful] = React.useState("10");
  const [savingRetention, setSavingRetention] = React.useState(false);
  const [archiveDecisions, setArchiveDecisions] = React.useState<Record<string, RoleSourceArchiveDecision>>({});
  const [archivePage, setArchivePage] = React.useState(0);
  const [applyDialogOpen, setApplyDialogOpen] = React.useState(false);
  const [rollbackSnapshotDigest, setRollbackSnapshotDigest] = React.useState("");
  const scanRequestKeyRef = React.useRef("");
  const approvalRequestKeyRef = React.useRef("");
  const applyRequestKeyRef = React.useRef("");
  const secretTransferRequestKeysRef = React.useRef<Record<string, string>>({});
  const planSectionRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    if (!sources.data?.length) {
      setSelectedId("");
      return;
    }
    if (!sources.data.some((source) => source.id === selectedId)) {
      setSelectedId(sources.data[0]?.id ?? "");
    }
  }, [selectedId, sources.data]);

  const plans = useQuery({
    ...roleSourcePlanListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const selected = sources.data?.find((source) => source.id === selectedId);
  const rebindRuntimeOptions = React.useMemo(
    () => [...(runtimes.data ?? [])]
      .sort((left, right) => {
        if (left.status !== right.status) return left.status === "online" ? -1 : 1;
        return runtimeDisplayLabel(left).localeCompare(runtimeDisplayLabel(right));
      })
      .map((runtime) => ({
        value: runtime.id,
        label: `${runtimeDisplayLabel(runtime)} · ${runtime.status}`,
      })),
    [runtimes.data],
  );
  const latestScan = useQuery({
    ...roleSourceLatestScanOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const scanHistory = useQuery({
    ...roleSourceScanListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const lifecycleHistory = useQuery({
    ...roleSourceLifecycleEventListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const snapshotSummaries = useQuery({
    ...roleSourceSnapshotSummaryListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const snapshotComparison = useQuery({
    ...roleSourceSnapshotComparisonOptions(workspaceId, selectedId, snapshotFrom, snapshotTo, snapshotOffset),
    enabled: Boolean(workspaceId && selectedId && snapshotFrom && snapshotTo && snapshotFrom !== snapshotTo),
  });
  const filteredScanHistory = React.useMemo(() => {
    const code = scanCodeFilter.trim().toLowerCase();
    return (scanHistory.data ?? []).filter((scan) =>
      (scanStatusFilter === "all" || scan.status === scanStatusFilter) &&
      (!code || scan.error_code?.toLowerCase().includes(code)),
    );
  }, [scanCodeFilter, scanHistory.data, scanStatusFilter]);
  const latestScanData = latestScan.data;
  const requestScan = useRequestRoleSourceScan(workspaceId, selectedId);
  const latest = plans.data?.[0];
  const createPlan = useCreateRoleSourcePlan(workspaceId, selectedId);
  const createRollbackPlan = useCreateRoleSourceRollbackPlan(workspaceId, selectedId);
  const createApproval = useCreateRoleSourcePlanApproval(
    workspaceId,
    selectedId,
    latest?.plan.plan_digest ?? "",
  );
  const applyPlan = useApplyRoleSourcePlan(
    workspaceId,
    selectedId,
    latest?.plan.plan_digest ?? "",
  );
  const approvals = useQuery({
    ...roleSourcePlanApprovalListOptions(
      workspaceId,
      selectedId,
      latest?.plan.plan_digest ?? "",
    ),
    enabled: Boolean(workspaceId && selectedId && latest?.plan.plan_digest),
  });
  const applyHistory = useQuery({
    ...roleSourceApplyHistoryListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const impact = useQuery({
    ...roleSourcePlanImpactOptions(
      workspaceId,
      selectedId,
      latest?.plan.plan_digest ?? "",
    ),
    enabled: Boolean(workspaceId && selectedId && latest?.plan.plan_digest),
  });
  const applyFailures = useQuery({
    ...roleSourceApplyFailureListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const runtimeAttestations = useQuery({
    ...roleSourceRuntimeAttestationListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId),
  });
  const legalHolds = useQuery({
    ...roleSourceLegalHoldListOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId && isOwner),
  });
  const retention = useQuery({
    ...roleSourceRetentionPreviewOptions(workspaceId, selectedId),
    enabled: Boolean(workspaceId && selectedId && isOwner),
  });

  React.useEffect(() => {
    if (latestScan.data?.status !== "succeeded") return;
    void queryClient.invalidateQueries({ queryKey: roleSourceKeys.list(workspaceId) });
    void queryClient.invalidateQueries({ queryKey: roleSourceKeys.plans(workspaceId, selectedId) });
    void queryClient.invalidateQueries({ queryKey: roleSourceKeys.scans(workspaceId, selectedId) });
  }, [latestScan.data?.id, latestScan.data?.status, queryClient, selectedId, workspaceId]);

  React.useEffect(() => {
    setArchiveDecisions({});
    setArchivePage(0);
    setApplyDialogOpen(false);
    setRollbackSnapshotDigest("");
    approvalRequestKeyRef.current = "";
    applyRequestKeyRef.current = "";
    secretTransferRequestKeysRef.current = {};
  }, [latest?.plan.plan_digest, selectedId]);

  React.useEffect(() => {
    const snapshots = snapshotSummaries.data ?? [];
    const available = new Set(snapshots.map((snapshot) => snapshot.snapshot_digest));
    const nextTo = available.has(snapshotTo) ? snapshotTo : snapshots[0]?.snapshot_digest ?? "";
    const nextFrom = available.has(snapshotFrom) && snapshotFrom !== nextTo
      ? snapshotFrom
      : snapshots.find((snapshot) => snapshot.snapshot_digest !== nextTo)?.snapshot_digest ?? "";
    if (nextTo !== snapshotTo) setSnapshotTo(nextTo);
    if (nextFrom !== snapshotFrom) setSnapshotFrom(nextFrom);
    setSnapshotOffset(0);
  }, [selectedId, snapshotSummaries.data, snapshotFrom, snapshotTo]);

  React.useEffect(() => {
    if (!retention.data?.policy) return;
    setRetentionEnabled(retention.data.policy.enabled);
    setRetentionMinimumDays(String(retention.data.policy.minimum_age_days));
    setRetentionKeepSuccessful(String(retention.data.policy.keep_successful_snapshots));
  }, [retention.data?.policy]);

  const createHoldValid = holdScope === "source" || sha256DigestPattern.test(holdSnapshotDigest.trim());
  const createReferenceValid = !holdReferenceDigest.trim() || sha256DigestPattern.test(holdReferenceDigest.trim());
  const releaseReferenceValid = !releaseReferenceDigest.trim() || sha256DigestPattern.test(releaseReferenceDigest.trim());

  async function createLegalHold() {
    if (!selected || savingHold || !createHoldRequestKey || !createHoldValid || !createReferenceValid) return;
    setSavingHold(true);
    try {
      await api.createRoleSourceLegalHold(workspaceId, selected.id, {
        request_key: createHoldRequestKey,
        scope: holdScope,
        snapshot_digest: holdScope === "snapshot" ? holdSnapshotDigest.trim() : undefined,
        reason_code: holdReason,
        reference_digest: holdReferenceDigest.trim() || undefined,
      });
      await queryClient.invalidateQueries({ queryKey: roleSourceKeys.legalHolds(workspaceId, selected.id) });
      toast.success(t(($) => $.role_sources.legal_hold_created));
      setCreateHoldOpen(false);
      setCreateHoldRequestKey("");
      setHoldScope("source");
      setHoldReason("regulatory");
      setHoldSnapshotDigest("");
      setHoldReferenceDigest("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.legal_hold_failed));
    } finally {
      setSavingHold(false);
    }
  }

  async function releaseLegalHold() {
    if (!selected || !holdToRelease || savingHold || !releaseHoldRequestKey || !releaseReferenceValid) return;
    setSavingHold(true);
    try {
      await api.releaseRoleSourceLegalHold(workspaceId, selected.id, holdToRelease.id, {
        request_key: releaseHoldRequestKey,
        reason_code: releaseReason,
        reference_digest: releaseReferenceDigest.trim() || undefined,
      });
      await queryClient.invalidateQueries({ queryKey: roleSourceKeys.legalHolds(workspaceId, selected.id) });
      toast.success(t(($) => $.role_sources.legal_hold_released));
      setHoldToRelease(null);
      setReleaseHoldRequestKey("");
      setReleaseReason("resolved");
      setReleaseReferenceDigest("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.legal_hold_failed));
    } finally {
      setSavingHold(false);
    }
  }

  const retentionDays = Number(retentionMinimumDays);
  const retentionKeep = Number(retentionKeepSuccessful);
  const retentionInputValid = Number.isInteger(retentionDays) && retentionDays >= 30 && retentionDays <= 3650 &&
    Number.isInteger(retentionKeep) && retentionKeep >= 2 && retentionKeep <= 100;

  async function updateRetentionPolicy() {
    if (!selected || !retention.data || savingRetention || !retentionRequestKey || !retentionInputValid) return;
    setSavingRetention(true);
    try {
      await api.updateRoleSourceRetentionPolicy(workspaceId, selected.id, {
        request_key: retentionRequestKey,
        expected_version: retention.data.policy.version,
        enabled: retentionEnabled,
        minimum_age_days: retentionDays,
        keep_successful_snapshots: retentionKeep,
      });
      await queryClient.invalidateQueries({ queryKey: roleSourceKeys.retention(workspaceId, selected.id) });
      toast.success(t(($) => $.role_sources.retention_updated));
      setRetentionDialogOpen(false);
      setRetentionRequestKey("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.retention_update_failed));
    } finally {
      setSavingRetention(false);
    }
  }

  async function updateLifecycle(
    action: RoleSourceLifecycleAction,
    extra?: { runtime_id: string; daemon_config_id: string },
  ) {
    if (!selected || savingLifecycle) return;
    setSavingLifecycle(true);
    try {
      await api.updateRoleSourceLifecycle(
        workspaceId,
        selected.id,
        { action, expected_version: selected.version, ...extra },
      );
      await queryClient.invalidateQueries({ queryKey: roleSourceKeys.all(workspaceId) });
      await queryClient.invalidateQueries({ queryKey: roleSourceKeys.lifecycleEvents(workspaceId, selected.id) });
      toast.success(t(($) => $.role_sources.lifecycle_success));
      setPendingAction(null);
      setRebindOpen(false);
      setRebindRuntimeId("");
      setRebindConfigId("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.lifecycle_failed));
    } finally {
      setSavingLifecycle(false);
    }
  }

  async function runReadOnlyScan() {
    if (!selected || requestScan.isPending) return;
    if (!scanRequestKeyRef.current) {
      scanRequestKeyRef.current = `role-source-scan-${globalThis.crypto.randomUUID()}`;
    }
    try {
      const scan = await requestScan.mutateAsync(scanRequestKeyRef.current);
      if (!scan) {
        toast.error(t(($) => $.role_sources.scan_invalid_response));
        return;
      }
      scanRequestKeyRef.current = "";
      toast.success(t(($) => $.role_sources.scan_requested));
    } catch (error) {
      if (errorCode(error)) scanRequestKeyRef.current = "";
      toast.error(t(($) => $.role_sources[scanRequestErrorTranslationKey(error)]));
    }
  }

  const archiveCandidates = React.useMemo(
    () => (latest?.plan.actions ?? [])
      .filter((action) => action.operation === "archive_candidate")
      .sort((left, right) => {
        const leftKey = objectRefKey(left.ref);
        const rightKey = objectRefKey(right.ref);
        return leftKey < rightKey ? -1 : leftKey > rightKey ? 1 : 0;
      }),
    [latest?.plan.actions],
  );
  const allArchiveCandidatesDecided = archiveCandidates.every(
    (action) => Boolean(archiveDecisions[objectRefKey(action.ref)]),
  );
  const decidedArchiveCandidateCount = archiveCandidates.reduce(
    (count, action) => count + (archiveDecisions[objectRefKey(action.ref)] ? 1 : 0),
    0,
  );
  const archivePageCount = Math.max(1, Math.ceil(archiveCandidates.length / archiveDecisionPageSize));
  const visibleArchiveCandidates = archiveCandidates.slice(
    archivePage * archiveDecisionPageSize,
    (archivePage + 1) * archiveDecisionPageSize,
  );

  React.useEffect(() => {
    setArchivePage((current) => Math.min(current, archivePageCount - 1));
  }, [archivePageCount]);

  function decideVisibleArchiveCandidates(decision: RoleSourceArchiveDecision) {
    setArchiveDecisions((current) => {
      const next = { ...current };
      for (const action of visibleArchiveCandidates) {
        next[objectRefKey(action.ref)] = decision;
      }
      return next;
    });
  }
  const approvedApproval = createApproval.data?.decision === "approved"
    ? createApproval.data
    : approvals.data?.find((approval) => approval.decision === "approved");
  const secretTransferRoles = (latest?.plan.actions ?? [])
    .filter((action) => action.ref.kind === "role" && action.needs_secret_transfer === true && action.operation !== "archive_candidate")
    .map((action) => ({ id: action.ref.id, name: action.display_name || action.ref.id }));
  const secretTransfers = useQuery({
    ...roleSourceSecretTransferListOptions(
      workspaceId,
      selectedId,
      latest?.plan.plan_digest ?? "",
      approvedApproval?.id ?? "",
    ),
    enabled: Boolean(workspaceId && selectedId && latest?.plan.plan_digest && approvedApproval),
  });
  const requestSecretTransfer = useRequestRoleSourceSecretTransfer(
    workspaceId,
    selectedId,
    latest?.plan.plan_digest ?? "",
    approvedApproval?.id ?? "",
  );
  const secretTransferByRole = new Map((secretTransfers.data ?? []).map((transfer) => [transfer.role_id, transfer]));
  const allSecretTransfersSubmitted = secretTransferRoles.every(
    (role) => {
      const transfer = secretTransferByRole.get(role.id);
      return transfer?.status === "submitted" && new Date(transfer.expires_at).getTime() > Date.now();
    },
  );

  async function requestTransfer(roleId: string) {
    if (!approvedApproval || requestSecretTransfer.isPending) return;
    if (!secretTransferRequestKeysRef.current[roleId]) {
      secretTransferRequestKeysRef.current[roleId] = `role-source-secret-transfer-${globalThis.crypto.randomUUID()}`;
    }
    try {
      const transfer = await requestSecretTransfer.mutateAsync({
        request_key: secretTransferRequestKeysRef.current[roleId]!,
        approval_id: approvedApproval.id,
        role_id: roleId,
      });
      if (!transfer) {
        toast.error(t(($) => $.role_sources.secret_transfer_invalid_response));
        return;
      }
      delete secretTransferRequestKeysRef.current[roleId];
      toast.success(t(($) => $.role_sources.secret_transfer_requested));
    } catch (error) {
      if (errorCode(error)) delete secretTransferRequestKeysRef.current[roleId];
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.secret_transfer_failed));
    }
  }

  async function generatePlan() {
    const snapshotDigest = latestScanData?.status === "succeeded"
      ? latestScanData.snapshot_digest
      : null;
    if (!snapshotDigest || createPlan.isPending) return;
    try {
      const plan = await createPlan.mutateAsync(snapshotDigest);
      if (!plan) {
        toast.error(t(($) => $.role_sources.plan_invalid_response));
        return;
      }
      toast.success(t(($) => $.role_sources.plan_created));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.plan_create_failed));
    }
  }

  async function generateRollbackPlan() {
    if (!rollbackSnapshotDigest || createRollbackPlan.isPending) return;
    try {
      const plan = await createRollbackPlan.mutateAsync(rollbackSnapshotDigest);
      if (!plan) {
        toast.error(t(($) => $.role_sources.rollback_invalid_response));
        return;
      }
      setRollbackSnapshotDigest("");
      toast.success(t(($) => $.role_sources.rollback_plan_created));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.rollback_plan_failed));
    }
  }

  async function reconcileApplyEvidence() {
    if (!selected) return;
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.all(workspaceId) }),
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.plans(workspaceId, selected.id) }),
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.applies(workspaceId, selected.id) }),
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.applyFailures(workspaceId, selected.id) }),
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.latestScan(workspaceId, selected.id) }),
      queryClient.invalidateQueries({ queryKey: roleSourceKeys.scans(workspaceId, selected.id) }),
    ]);
    toast.success(t(($) => $.role_sources.failed_apply_reconciled));
  }

  async function approveLatestPlan() {
    if (!latest?.plan.applyable || !allArchiveCandidatesDecided || createApproval.isPending) return;
    if (!approvalRequestKeyRef.current) {
      approvalRequestKeyRef.current = `role-source-approval-${globalThis.crypto.randomUUID()}`;
    }
    try {
      const approval = await createApproval.mutateAsync({
        request_key: approvalRequestKeyRef.current,
        decision: "approved",
        decisions: {
          contract_version: latest.plan.contract_version,
          archives: archiveCandidates.map((action) => ({
            ref: action.ref,
            decision: archiveDecisions[objectRefKey(action.ref)]!,
          })),
        },
      });
      if (!approval) {
        toast.error(t(($) => $.role_sources.approval_invalid_response));
        return;
      }
      approvalRequestKeyRef.current = "";
      toast.success(t(($) => $.role_sources.approval_created));
    } catch (error) {
      if (errorCode(error)) approvalRequestKeyRef.current = "";
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.approval_failed));
    }
  }

  async function applyLatestPlan() {
    if (!approvedApproval || !allSecretTransfersSubmitted || applyPlan.isPending) return;
    if (!applyRequestKeyRef.current) {
      applyRequestKeyRef.current = `role-source-apply-${globalThis.crypto.randomUUID()}`;
    }
    try {
      const result = await applyPlan.mutateAsync({
        request_key: applyRequestKeyRef.current,
        approval_id: approvedApproval.id,
        secret_transfer_ids: secretTransferRoles.length ? Object.fromEntries(secretTransferRoles.map((role) => [
          role.id,
          secretTransferByRole.get(role.id)!.id,
        ])) : undefined,
      });
      if (!result) {
        toast.error(t(($) => $.role_sources.apply_invalid_response));
        return;
      }
      applyRequestKeyRef.current = "";
      setApplyDialogOpen(false);
      toast.success(t(($) => $.role_sources.apply_succeeded));
    } catch (error) {
      if (errorCode(error)) applyRequestKeyRef.current = "";
      toast.error(error instanceof Error ? error.message : t(($) => $.role_sources.apply_failed));
    }
  }

  return (
    <SettingsTab
      title={t(($) => $.role_sources.title)}
      description={t(($) => $.role_sources.description)}
    >
      <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-caption leading-5 text-muted-foreground">
        <div className="flex gap-2 text-foreground">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
          <span>{t(($) => roleSourceApplyEnabled
            ? $.role_sources.controlled_apply_notice
            : $.role_sources.read_only_notice)}</span>
        </div>
      </div>

      <SettingsSection title={t(($) => $.role_sources.sources_title)}>
        <SettingsCard>
          {sources.isLoading ? (
            <div className="flex min-h-24 items-center justify-center gap-2 text-caption text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              {t(($) => $.role_sources.loading)}
            </div>
          ) : sources.isError ? (
            <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.load_failed)}</div>
          ) : !sources.data?.length ? (
            <div className="p-6 text-center">
              <CircleSlash2 className="mx-auto h-5 w-5 text-muted-foreground" />
              <p className="mt-2 text-body font-medium">{t(($) => $.role_sources.empty)}</p>
              <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.role_sources.empty_hint)}</p>
            </div>
          ) : (
            sources.data.map((source) => (
              <button
                type="button"
                key={source.id}
                onClick={() => setSelectedId(source.id)}
                aria-pressed={source.id === selectedId}
                className={cn(
                  "flex w-full items-center justify-between gap-4 px-4 py-3 text-left transition-colors hover:bg-surface-hover",
                  source.id === selectedId && "bg-surface-selected",
                )}
              >
                <span className="min-w-0">
                  <span className="block truncate text-body font-medium">{source.name}</span>
                  <span className="mt-0.5 block truncate text-caption text-muted-foreground">
                    {source.kind} · {source.adapter_version} · {shortDigest(source.current_snapshot_digest)}
                  </span>
                  <span className="mt-0.5 block truncate font-mono text-caption text-muted-foreground">
                    {t(($) => $.role_sources.runtime_revision)}: {shortDigest(source.runtime_config.revision)}
                    {source.runtime_config.observed_at ? ` · ${source.runtime_config.observed_at}` : ""}
                  </span>
                </span>
                <span className="flex shrink-0 flex-col items-end gap-1.5">
                  <Badge variant={source.state === "active" ? "secondary" : "outline"}>{source.state}</Badge>
                  <Badge variant={source.runtime_config.status === "loaded" ? "secondary" : "destructive"}>
                    {t(($) => $.role_sources[runtimeConfigTranslationKey(source.runtime_config.status)])}
                  </Badge>
                  {source.runtime_config.status === "runtime_unavailable" ? (
                    <Badge variant="outline">
                      {t(($) => $.role_sources[runtimeConfigTranslationKey(source.runtime_config.attestation_status)])}
                    </Badge>
                  ) : null}
                </span>
              </button>
            ))
          )}
        </SettingsCard>
      </SettingsSection>

      {selected ? (
        <>
          <SettingsSection
            title={t(($) => $.role_sources.lifecycle_title)}
            description={t(($) => $.role_sources.lifecycle_description)}
          >
            <SettingsCard>
              <div className="flex flex-wrap items-center justify-between gap-3 p-4">
                <div>
                  <div className="text-body font-medium">{selected.name}</div>
                  <div className="mt-1 text-caption text-muted-foreground">
                    {t(($) => $.role_sources.lifecycle_current, { state: selected.state, version: selected.version })}
                  </div>
                </div>
                {canManage ? (
                  <div className="flex flex-wrap gap-2">
                    {selected.state === "registered" || selected.state === "active" || selected.state === "error" ? (
                      <Button variant="outline" size="sm" onClick={() => setPendingAction("pause")}>
                        <PauseCircle className="h-4 w-4" />
                        {t(($) => $.role_sources.pause)}
                      </Button>
                    ) : null}
                    {selected.state === "paused" ? (
                      <>
                        <Button variant="outline" size="sm" onClick={() => setPendingAction("resume")}>
                          <PlayCircle className="h-4 w-4" />
                          {t(($) => $.role_sources.resume)}
                        </Button>
                        <Button variant="destructive" size="sm" onClick={() => setPendingAction("detach")}>
                          <Unplug className="h-4 w-4" />
                          {t(($) => $.role_sources.detach)}
                        </Button>
                      </>
                    ) : null}
                    {selected.state === "detached" ? (
                      <Button variant="outline" size="sm" onClick={() => setRebindOpen(true)}>
                        <Repeat2 className="h-4 w-4" />
                        {t(($) => $.role_sources.rebind)}
                      </Button>
                    ) : null}
                  </div>
                ) : (
                  <div className="text-caption text-muted-foreground">{t(($) => $.role_sources.lifecycle_admin_only)}</div>
                )}
              </div>
              <div className="border-t border-surface-border">
                {lifecycleHistory.isLoading ? (
                  <div className="flex min-h-16 items-center justify-center gap-2 text-caption text-muted-foreground">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    {t(($) => $.role_sources.loading)}
                  </div>
                ) : lifecycleHistory.isError ? (
                  <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.lifecycle_history_load_failed)}</div>
                ) : !lifecycleHistory.data?.length ? (
                  <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.lifecycle_history_empty)}</div>
                ) : (
                  <div className="max-h-64 divide-y divide-surface-border overflow-y-auto">
                    {lifecycleHistory.data.map((event) => (
                      <div key={event.event_digest} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
                        <div className="min-w-0">
                          <div className="text-body font-medium">
                            {t(($) => $.role_sources[lifecycleEventTranslationKey(event.event_type)])}
                          </div>
                          <div className="mt-1 text-caption text-muted-foreground">
                            {event.previous_state} → {event.state} · {t(($) => $.role_sources.lifecycle_actor, {
                              type: event.actor_type,
                              id: shortIdentifier(event.actor_id),
                            })} · {event.occurred_at}
                          </div>
                          {event.previous_runtime_id || event.runtime_id ? (
                            <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                              {t(($) => $.role_sources.lifecycle_runtime_change, {
                                from: shortIdentifier(event.previous_runtime_id),
                                to: shortIdentifier(event.runtime_id),
                              })}
                            </div>
                          ) : null}
                          <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                            #{event.sequence} · {shortDigest(event.event_digest)}
                          </div>
                        </div>
                        {event.cancelled_scan_count || event.cancelled_transfer_count ? (
                          <Badge variant="outline">
                            {t(($) => $.role_sources.lifecycle_cancelled_counts, { scans: event.cancelled_scan_count, transfers: event.cancelled_transfer_count })}
                          </Badge>
                        ) : null}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </SettingsCard>
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.role_sources.scan_title)}
            description={t(($) => $.role_sources.scan_description)}
          >
            <SettingsCard>
              {latestScan.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.scan_loading)}
                </div>
              ) : latestScan.isError ? (
                <div className="p-4 text-caption text-destructive">
                  {t(($) => $.role_sources.scan_load_failed)}
                </div>
              ) : (
                <div className="flex flex-wrap items-start justify-between gap-4 p-4">
                  <div className="min-w-0 space-y-1">
                    {latestScanData ? (
                      <>
                        <div className="flex flex-wrap items-center gap-2">
                          <Badge variant={latestScanData.status === "failed" ? "destructive" : "outline"}>
                            {t(($) => $.role_sources[scanStatusTranslationKey(latestScanData.status)])}
                          </Badge>
                          <span className="font-mono text-caption text-muted-foreground">
                            {shortDigest(latestScanData.id)}
                          </span>
                        </div>
                        <div className="text-caption text-muted-foreground">
                          {t(($) => $.role_sources.scan_requested_at, { time: latestScanData.requested_at })}
                          {latestScanData.completed_at
                            ? ` · ${t(($) => $.role_sources.scan_completed_at, { time: latestScanData.completed_at })}`
                            : ""}
                        </div>
                        {latestScanData.snapshot_digest ? (
                          <div className="font-mono text-caption text-muted-foreground">
                            {t(($) => $.role_sources.scan_snapshot)}: {shortDigest(latestScanData.snapshot_digest)}
                          </div>
                        ) : null}
                        {latestScanData.error_code ? (
                          <div className="font-mono text-caption text-destructive">
                            {t(($) => $.role_sources.scan_error_code)}: {latestScanData.error_code}
                          </div>
                        ) : null}
                      </>
                    ) : (
                      <div className="text-caption text-muted-foreground">
                        {t(($) => $.role_sources.scan_empty)}
                      </div>
                    )}
                  </div>
                  {canManage ? (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={
                        requestScan.isPending ||
                        latestScanData?.status === "queued" ||
                        latestScanData?.status === "claimed" ||
                        selected.state === "paused" ||
                        selected.state === "detached"
                      }
                      onClick={() => void runReadOnlyScan()}
                    >
                      {requestScan.isPending || latestScanData?.status === "queued" || latestScanData?.status === "claimed" ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <RefreshCw className="h-4 w-4" />
                      )}
                      {t(($) => $.role_sources.scan_request)}
                    </Button>
                  ) : (
                    <div className="text-caption text-muted-foreground">
                      {t(($) => $.role_sources.scan_admin_only)}
                    </div>
                  )}
                </div>
              )}
            </SettingsCard>
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.role_sources.runtime_attestations_title)}
            description={t(($) => $.role_sources.runtime_attestations_description)}
          >
            <SettingsCard>
              {runtimeAttestations.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.loading)}
                </div>
              ) : runtimeAttestations.isError ? (
                <div className="p-4 text-caption text-destructive">
                  {t(($) => $.role_sources.runtime_attestations_load_failed)}
                </div>
              ) : !runtimeAttestations.data?.length ? (
                <div className="p-4 text-caption text-muted-foreground">
                  {t(($) => $.role_sources.runtime_attestations_empty)}
                </div>
              ) : (
                <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                  {runtimeAttestations.data.map((attestation) => (
                    <div key={attestation.attestation_id} className="flex items-start justify-between gap-4 px-4 py-3">
                      <div className="min-w-0">
                        <div className="font-mono text-caption text-foreground">
                          {shortDigest(attestation.revision ?? attestation.attestation_id)}
                        </div>
                        <div className="mt-1 text-caption text-muted-foreground">
                          {t(($) => $.role_sources.runtime_attestation_metadata, {
                            time: attestation.last_observed_at,
                            count: attestation.observation_count,
                          })}
                        </div>
                      </div>
                      <Badge variant={attestation.status === "loaded" ? "secondary" : "destructive"}>
                        {t(($) => $.role_sources[runtimeConfigTranslationKey(attestation.status)])}
                      </Badge>
                    </div>
                  ))}
                </div>
              )}
            </SettingsCard>
          </SettingsSection>

          {isOwner ? (
            <SettingsSection
              title={t(($) => $.role_sources.legal_holds_title)}
              description={t(($) => $.role_sources.legal_holds_description)}
            >
              <SettingsCard>
                <div className="flex flex-wrap items-start justify-between gap-3 border-b border-surface-border p-4">
                  <div className="flex max-w-2xl gap-2 text-caption leading-5 text-muted-foreground">
                    <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
                    <span>{t(($) => $.role_sources.legal_holds_warning)}</span>
                  </div>
                  <Button variant="outline" size="sm" onClick={() => {
                    setCreateHoldRequestKey(legalHoldRequestKey("create"));
                    setCreateHoldOpen(true);
                  }}>
                    <ShieldAlert className="h-4 w-4" />
                    {t(($) => $.role_sources.legal_hold_create)}
                  </Button>
                </div>
                {legalHolds.isLoading ? (
                  <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    {t(($) => $.role_sources.loading)}
                  </div>
                ) : legalHolds.isError ? (
                  <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.legal_holds_load_failed)}</div>
                ) : !legalHolds.data?.length ? (
                  <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.legal_holds_empty)}</div>
                ) : (
                  <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                    {legalHolds.data.map((hold) => (
                      <div key={hold.id} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2 text-body font-medium">
                            <Badge variant={hold.status === "active" ? "destructive" : "outline"}>
                              {hold.status === "active"
                                ? t(($) => $.role_sources.legal_hold_active)
                                : t(($) => $.role_sources.legal_hold_released_status)}
                            </Badge>
                            <span>{t(($) => $.role_sources[`legal_hold_reason_${hold.reason_code}`])}</span>
                          </div>
                          <div className="mt-1 font-mono text-caption text-muted-foreground">
                            {hold.scope === "source"
                              ? t(($) => $.role_sources.legal_hold_scope_source)
                              : `${t(($) => $.role_sources.legal_hold_scope_snapshot)} · ${shortDigest(hold.snapshot_digest)}`}
                            {` · ${hold.created_at}`}
                          </div>
                          {hold.reference_digest ? (
                            <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                              {t(($) => $.role_sources.legal_hold_reference)}: {shortDigest(hold.reference_digest)}
                            </div>
                          ) : null}
                        </div>
                        {hold.status === "active" ? (
                          <Button variant="outline" size="sm" onClick={() => {
                            setReleaseHoldRequestKey(legalHoldRequestKey("release"));
                            setHoldToRelease(hold);
                          }}>
                            {t(($) => $.role_sources.legal_hold_release)}
                          </Button>
                        ) : null}
                      </div>
                    ))}
                  </div>
                )}
              </SettingsCard>
            </SettingsSection>
          ) : null}

          {isOwner ? (
            <SettingsSection
              title={t(($) => $.role_sources.retention_title)}
              description={t(($) => $.role_sources.retention_description)}
            >
              <SettingsCard>
                {retention.isLoading ? (
                  <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    {t(($) => $.role_sources.loading)}
                  </div>
                ) : retention.isError || !retention.data ? (
                  <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.retention_load_failed)}</div>
                ) : (
                  <div className="divide-y divide-surface-border">
                    <div className="flex flex-wrap items-start justify-between gap-3 p-4">
                      <div>
                        <div className="flex items-center gap-2 text-body font-medium">
                          <Badge variant={retention.data.policy.enabled ? "destructive" : "outline"}>
                            {retention.data.policy.enabled
                              ? t(($) => $.role_sources.retention_enabled)
                              : t(($) => $.role_sources.retention_disabled)}
                          </Badge>
                          <span>{t(($) => $.role_sources.retention_policy_version, { version: retention.data.policy.version })}</span>
                        </div>
                        <p className="mt-2 text-caption text-muted-foreground">
                          {t(($) => $.role_sources.retention_policy_summary, {
                            days: retention.data.policy.minimum_age_days,
                            count: retention.data.policy.keep_successful_snapshots,
                          })}
                        </p>
                      </div>
                      <Button variant="outline" size="sm" onClick={() => {
                        setRetentionRequestKey(`role-source-retention-policy-${globalThis.crypto.randomUUID()}`);
                        setRetentionDialogOpen(true);
                      }}>
                        {t(($) => $.role_sources.retention_edit)}
                      </Button>
                    </div>
                    <div className="p-4">
                      <div className="text-body font-medium">
                        {t(($) => $.role_sources.retention_preview_summary, {
                          count: retention.data.eligible_count,
                          bytes: formatRetentionBytes(retention.data.estimated_bytes),
                        })}
                      </div>
                      <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.role_sources.retention_preview_warning)}</p>
                      {retention.data.candidates.length ? (
                        <div className="mt-3 max-h-48 divide-y divide-surface-border overflow-y-auto rounded-md border border-surface-border">
                          {retention.data.candidates.map((candidate) => (
                            <div key={candidate.snapshot_digest} className="flex items-center justify-between gap-3 px-3 py-2 text-caption">
                              <span className="min-w-0 truncate font-mono">{shortDigest(candidate.snapshot_digest)}</span>
                              <span className="shrink-0 text-muted-foreground">
                                {candidate.created_at} · {formatRetentionBytes(candidate.estimated_bytes)}
                              </span>
                            </div>
                          ))}
                        </div>
                      ) : null}
                      {retention.data.truncated ? (
                        <p className="mt-2 text-caption text-amber-700">{t(($) => $.role_sources.retention_preview_truncated)}</p>
                      ) : null}
                    </div>
                  </div>
                )}
              </SettingsCard>
            </SettingsSection>
          ) : null}

          <div ref={planSectionRef}>
          <SettingsSection
            title={t(($) => $.role_sources.latest_plan_title)}
              description={t(($) => $.role_sources.latest_plan_description, { name: selected.name })}
            >
              <SettingsCard>
            {roleSourceApplyEnabled && canManage ? (
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-surface-border p-4">
                <div className="text-caption text-muted-foreground">
                  {latestScanData?.status === "succeeded" && latestScanData.snapshot_digest
                    ? t(($) => $.role_sources.plan_target_snapshot, { digest: shortDigest(latestScanData.snapshot_digest) })
                    : t(($) => $.role_sources.plan_requires_scan)}
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={createPlan.isPending || latestScanData?.status !== "succeeded" || !latestScanData.snapshot_digest}
                  onClick={() => void generatePlan()}
                >
                  {createPlan.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
                  {t(($) => $.role_sources.plan_generate)}
                </Button>
              </div>
            ) : null}
            {plans.isLoading ? (
              <div className="flex min-h-24 items-center justify-center gap-2 text-caption text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                {t(($) => $.role_sources.loading)}
              </div>
            ) : plans.isError ? (
              <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.plan_load_failed)}</div>
            ) : !latest ? (
              <div className="p-6 text-center text-caption text-muted-foreground">{t(($) => $.role_sources.no_plan)}</div>
            ) : (
              <div className="divide-y divide-surface-border">
                <div className="flex flex-wrap items-center justify-between gap-3 p-4">
                  <div>
                    <div className="flex items-center gap-2 text-body font-medium">
                      {latest.plan.applyable ? <CheckCircle2 className="h-4 w-4 text-emerald-600" /> : <AlertTriangle className="h-4 w-4 text-amber-600" />}
                      {latest.plan.applyable ? t(($) => $.role_sources.plan_ready) : t(($) => $.role_sources.plan_blocked)}
                      {latest.plan.mode === "rollback" ? (
                        <Badge variant="destructive">{t(($) => $.role_sources.plan_mode_rollback)}</Badge>
                      ) : null}
                    </div>
                    <div className="mt-1 font-mono text-caption text-muted-foreground">{shortDigest(latest.plan.plan_digest)}</div>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    <Badge variant="secondary">+{latest.plan.summary.create}</Badge>
                    <Badge variant="secondary">~{latest.plan.summary.update}</Badge>
                    <Badge variant="outline">={latest.plan.summary.unchanged}</Badge>
                    {latest.plan.summary.archive_candidate > 0 ? <Badge variant="destructive">−{latest.plan.summary.archive_candidate}</Badge> : null}
                    {latest.plan.summary.blocked > 0 ? <Badge variant="destructive">!{latest.plan.summary.blocked}</Badge> : null}
                  </div>
                </div>

                {latest.plan.blockers.length > 0 ? (
                  <div className="space-y-2 p-4">
                    <div className="text-caption font-medium">{t(($) => $.role_sources.blockers)}</div>
                    {latest.plan.blockers.map((blocker, index) => (
                      <div key={`${blocker.code}-${index}`} className="rounded-md bg-destructive/5 px-3 py-2 text-caption leading-5">
                        <span className="font-mono text-destructive">{blocker.code}</span>
                        <span className="ml-2 text-muted-foreground">{blocker.message}</span>
                      </div>
                    ))}
                  </div>
                ) : null}

                {roleSourceApplyEnabled && canManage && latest.plan.applyable ? (
                  <div className="space-y-4 p-4">
                    <div>
                      <div className="text-body font-medium">{t(($) => $.role_sources.approval_title)}</div>
                      <p className="mt-1 text-caption text-muted-foreground">
                        {t(($) => $.role_sources.approval_description)}
                      </p>
                    </div>
                    {archiveCandidates.length ? (
                      <div className="space-y-3">
                        <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-surface-border p-3">
                          <div className="text-caption text-muted-foreground">
                            {t(($) => $.role_sources.archive_progress, {
                              decided: decidedArchiveCandidateCount,
                              total: archiveCandidates.length,
                              page: archivePage + 1,
                              pages: archivePageCount,
                            })}
                          </div>
                          <div className="flex flex-wrap gap-2">
                            <Button variant="outline" size="sm" onClick={() => decideVisibleArchiveCandidates("retain")}>
                              {t(($) => $.role_sources.archive_page_retain)}
                            </Button>
                            <Button variant="outline" size="sm" onClick={() => decideVisibleArchiveCandidates("archive")}>
                              {t(($) => $.role_sources.archive_page_archive)}
                            </Button>
                          </div>
                        </div>
                        {visibleArchiveCandidates.map((action) => {
                          const key = objectRefKey(action.ref);
                          const inputId = `archive-decision-${action.ref.kind}-${action.ref.parent_id ?? "root"}-${action.ref.id}`;
                          return (
                            <div key={key} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-surface-border p-3">
                              <div className="min-w-0">
                                <Label htmlFor={inputId}>{action.display_name || action.ref.id}</Label>
                                <div className="mt-1 text-caption text-muted-foreground">
                                  {action.ref.kind} · {action.reason}
                                </div>
                              </div>
                              <Select
                                items={[
                                  { value: "retain", label: t(($) => $.role_sources.archive_retain) },
                                  { value: "archive", label: t(($) => $.role_sources.archive_archive) },
                                ]}
                                value={archiveDecisions[key] ?? null}
                                onValueChange={(value) => value && setArchiveDecisions((current) => ({
                                  ...current,
                                  [key]: value as RoleSourceArchiveDecision,
                                }))}
                              >
                                <SelectTrigger id={inputId} className="w-44">
                                  <SelectValue placeholder={t(($) => $.role_sources.archive_choose)} />
                                </SelectTrigger>
                                <SelectContent>
                                  <SelectItem value="retain">{t(($) => $.role_sources.archive_retain)}</SelectItem>
                                  <SelectItem value="archive">{t(($) => $.role_sources.archive_archive)}</SelectItem>
                                </SelectContent>
                              </Select>
                            </div>
                          );
                        })}
                        {archivePageCount > 1 ? (
                          <div className="flex items-center justify-between gap-3">
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={archivePage === 0}
                              onClick={() => setArchivePage((current) => Math.max(0, current - 1))}
                            >
                              {t(($) => $.role_sources.archive_previous_page)}
                            </Button>
                            <span className="text-caption text-muted-foreground">
                              {t(($) => $.role_sources.archive_page, { page: archivePage + 1, pages: archivePageCount })}
                            </span>
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={archivePage >= archivePageCount - 1}
                              onClick={() => setArchivePage((current) => Math.min(archivePageCount - 1, current + 1))}
                            >
                              {t(($) => $.role_sources.archive_next_page)}
                            </Button>
                          </div>
                        ) : null}
                      </div>
                    ) : (
                      <div className="text-caption text-muted-foreground">
                        {t(($) => $.role_sources.archive_none)}
                      </div>
                    )}
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <div className="text-caption text-muted-foreground">
                        {approvedApproval
                          ? t(($) => $.role_sources.approval_recorded, { time: approvedApproval.created_at })
                          : t(($) => $.role_sources.approval_not_recorded)}
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={createApproval.isPending || !allArchiveCandidatesDecided || Boolean(approvedApproval)}
                          onClick={() => void approveLatestPlan()}
                        >
                          {createApproval.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <CheckCircle2 className="h-4 w-4" />}
                          {t(($) => $.role_sources.approve_plan)}
                        </Button>
                        <Button
                          size="sm"
                          disabled={!approvedApproval || !allSecretTransfersSubmitted || applyPlan.isPending}
                          onClick={() => setApplyDialogOpen(true)}
                        >
                          {applyPlan.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
                          {t(($) => $.role_sources.apply_plan)}
                        </Button>
                      </div>
                    </div>
                    {approvedApproval && secretTransferRoles.length ? (
                      <div className="space-y-3 rounded-md border border-surface-border p-3">
                        <div>
                          <div className="text-caption font-medium">{t(($) => $.role_sources.secret_transfer_title)}</div>
                          <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.role_sources.secret_transfer_description)}</p>
                        </div>
                        {secretTransferRoles.map((role) => {
                          const transfer = secretTransferByRole.get(role.id);
                          const unexpired = transfer ? new Date(transfer.expires_at).getTime() > Date.now() : false;
                          const ready = transfer?.status === "submitted" && unexpired;
                          const active = (transfer?.status === "pending" || transfer?.status === "claimed") && unexpired;
                          return (
                            <div key={role.id} className="flex flex-wrap items-center justify-between gap-3">
                              <div className="min-w-0">
                                <div className="truncate text-caption font-medium">{role.name}</div>
                                <div className="font-mono text-caption text-muted-foreground">
                                  {transfer
                                    ? t(($) => $.role_sources.secret_transfer_status, { status: transfer.status, expires: transfer.expires_at })
                                    : t(($) => $.role_sources.secret_transfer_not_requested)}
                                </div>
                              </div>
                              {ready ? (
                                <Badge variant="secondary">{t(($) => $.role_sources.secret_transfer_ready)}</Badge>
                              ) : (
                                <Button
                                  variant="outline"
                                  size="sm"
                                  disabled={requestSecretTransfer.isPending || active}
                                  onClick={() => void requestTransfer(role.id)}
                                >
                                  {requestSecretTransfer.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
                                  {active ? t(($) => $.role_sources.secret_transfer_waiting) : t(($) => $.role_sources.secret_transfer_request)}
                                </Button>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                <div className="space-y-3 p-4">
                  <div>
                    <div className="text-body font-medium">{t(($) => $.role_sources.impact_title)}</div>
                    <div className="mt-1 text-caption text-muted-foreground">{t(($) => $.role_sources.impact_description)}</div>
                  </div>
                  {impact.isLoading ? (
                    <div className="flex items-center gap-2 text-caption text-muted-foreground">
                      <Loader2 className="h-4 w-4 animate-spin" />
                      {t(($) => $.role_sources.loading)}
                    </div>
                  ) : impact.isError ? (
                    <div className="text-caption text-destructive">{t(($) => $.role_sources.impact_load_failed)}</div>
                  ) : impact.data ? (
                    <div className="space-y-3">
                      <div className="font-mono text-caption text-muted-foreground">
                        {t(($) => $.role_sources.impact_as_of, { time: impact.data.generated_at })}
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        <Badge variant={impact.data.summary.cancel_on_apply > 0 ? "destructive" : "outline"}>
                          {t(($) => $.role_sources.cancel_on_apply, { count: impact.data.summary.cancel_on_apply })}
                        </Badge>
                        <Badge variant={impact.data.summary.conditional_cancel_on_archive > 0 ? "destructive" : "outline"}>
                          {t(($) => $.role_sources.cancel_if_archived, { count: impact.data.summary.conditional_cancel_on_archive })}
                        </Badge>
                        <Badge variant="secondary">
                          {t(($) => $.role_sources.continue_current, { count: impact.data.summary.continue_current_version })}
                        </Badge>
                        {impact.data.summary.unmapped_existing_roles > 0 ? (
                          <Badge variant="destructive">
                            {t(($) => $.role_sources.unmapped_roles, { count: impact.data.summary.unmapped_existing_roles })}
                          </Badge>
                        ) : null}
                      </div>

                      {impact.data.workers.length > 0 ? (
                        <div className="rounded-md border border-surface-border">
                          <div className="border-b border-surface-border px-3 py-2 text-caption font-medium">
                            {t(($) => $.role_sources.affected_workers)}
                          </div>
                          <div className="max-h-48 divide-y divide-surface-border overflow-y-auto">
                            {impact.data.workers.map((worker) => (
                              <div key={worker.agent_id} className="flex items-center justify-between gap-3 px-3 py-2 text-caption">
                                <span className="min-w-0">
                                  <span className="block truncate font-medium text-foreground">{worker.agent_name}</span>
                                  <span className="block truncate text-muted-foreground">{worker.source_role_id}</span>
                                </span>
                                <span className="shrink-0 text-muted-foreground">
                                  {t(($) => $.role_sources.worker_task_counts, {
                                    preStart: worker.pre_start_tasks,
                                    running: worker.running_tasks,
                                  })}
                                </span>
                              </div>
                            ))}
                          </div>
                        </div>
                      ) : null}

                      <div className="rounded-md border border-surface-border">
                        <div className="border-b border-surface-border px-3 py-2 text-caption font-medium">
                          {t(($) => $.role_sources.affected_tasks)}
                        </div>
                        {impact.data.tasks.length > 0 ? (
                          <div className="max-h-56 divide-y divide-surface-border overflow-y-auto">
                            {impact.data.tasks.map((task) => (
                              <div key={task.task_id} className="flex items-center justify-between gap-3 px-3 py-2 text-caption">
                                <span className="min-w-0 truncate font-mono">{task.task_id}</span>
                                <span className="shrink-0 text-muted-foreground">
                                  {task.status} · {task.effect === "cancel_on_apply"
                                    ? t(($) => $.role_sources.effect_cancel)
                                    : task.effect === "cancel_if_archived"
                                      ? t(($) => $.role_sources.effect_conditional_cancel)
                                      : t(($) => $.role_sources.effect_continue)}
                                </span>
                              </div>
                            ))}
                          </div>
                        ) : (
                          <div className="px-3 py-4 text-caption text-muted-foreground">{t(($) => $.role_sources.no_active_tasks)}</div>
                        )}
                        {impact.data.summary.worker_details_truncated || impact.data.summary.task_details_truncated ? (
                          <div className="border-t border-surface-border px-3 py-2 text-caption text-amber-700">
                            {t(($) => $.role_sources.impact_truncated)}
                          </div>
                        ) : null}
                      </div>
                    </div>
                  ) : null}
                </div>

                <div className="max-h-96 divide-y divide-surface-border overflow-y-auto">
                  {latest.plan.actions.slice(0, 200).map((action) => (
                    <div key={`${action.ref.kind}:${action.ref.parent_id ?? ""}:${action.ref.id}`} className="flex items-start justify-between gap-3 px-4 py-3">
                      <div className="min-w-0">
                        <div className="truncate text-body font-medium">{action.display_name || action.ref.id}</div>
                        <div className="mt-0.5 text-caption text-muted-foreground">{action.ref.kind} · {action.reason}</div>
                      </div>
                      <Badge variant={operationVariant(action.operation)}>{action.operation}</Badge>
                    </div>
                  ))}
                  {latest.plan.actions.length > 200 ? (
                    <div className="p-3 text-center text-caption text-muted-foreground">
                      {t(($) => $.role_sources.more_actions, { count: latest.plan.actions.length - 200 })}
                    </div>
                  ) : null}
                </div>
              </div>
            )}
              </SettingsCard>
            </SettingsSection>
          </div>

          <SettingsSection
            title={t(($) => $.role_sources.snapshot_history_title)}
            description={t(($) => $.role_sources.snapshot_history_description)}
          >
            <SettingsCard>
              {snapshotSummaries.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.loading)}
                </div>
              ) : snapshotSummaries.isError ? (
                <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.snapshot_history_load_failed)}</div>
              ) : (snapshotSummaries.data?.length ?? 0) < 2 ? (
                <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.snapshot_history_minimum)}</div>
              ) : (
                <>
                  <div className="grid gap-3 border-b border-surface-border p-4 sm:grid-cols-2">
                    <div className="space-y-2">
                      <Label htmlFor="role-source-snapshot-from">{t(($) => $.role_sources.snapshot_from)}</Label>
                      <Select
                        items={(snapshotSummaries.data ?? []).filter((snapshot) => snapshot.snapshot_digest !== snapshotTo).map((snapshot) => ({
                          value: snapshot.snapshot_digest,
                          label: `${shortDigest(snapshot.snapshot_digest)} · ${snapshot.created_at}`,
                        }))}
                        value={snapshotFrom}
                        onValueChange={(value) => { setSnapshotFrom(value ?? ""); setSnapshotOffset(0); }}
                      >
                        <SelectTrigger id="role-source-snapshot-from" className="w-full"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          {snapshotSummaries.data?.filter((snapshot) => snapshot.snapshot_digest !== snapshotTo).map((snapshot) => (
                            <SelectItem key={snapshot.snapshot_digest} value={snapshot.snapshot_digest}>
                              {shortDigest(snapshot.snapshot_digest)} · {snapshot.created_at}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="role-source-snapshot-to">{t(($) => $.role_sources.snapshot_to)}</Label>
                      <Select
                        items={(snapshotSummaries.data ?? []).filter((snapshot) => snapshot.snapshot_digest !== snapshotFrom).map((snapshot) => ({
                          value: snapshot.snapshot_digest,
                          label: `${shortDigest(snapshot.snapshot_digest)} · ${snapshot.created_at}`,
                        }))}
                        value={snapshotTo}
                        onValueChange={(value) => { setSnapshotTo(value ?? ""); setSnapshotOffset(0); }}
                      >
                        <SelectTrigger id="role-source-snapshot-to" className="w-full"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          {snapshotSummaries.data?.filter((snapshot) => snapshot.snapshot_digest !== snapshotFrom).map((snapshot) => (
                            <SelectItem key={snapshot.snapshot_digest} value={snapshot.snapshot_digest}>
                              {shortDigest(snapshot.snapshot_digest)} · {snapshot.created_at}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                  <div className="border-b border-surface-border px-4 py-3 text-caption text-muted-foreground">
                    {t(($) => $.role_sources.snapshot_content_boundary)}
                  </div>
                  {snapshotComparison.isLoading ? (
                    <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                      <Loader2 className="h-4 w-4 animate-spin" />
                      {t(($) => $.role_sources.loading)}
                    </div>
                  ) : snapshotComparison.isError || !snapshotComparison.data ? (
                    <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.snapshot_comparison_load_failed)}</div>
                  ) : !snapshotComparison.data.total_changes ? (
                    <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.snapshot_no_changes)}</div>
                  ) : (
                    <>
                      <div className="flex items-center justify-between gap-3 border-b border-surface-border px-4 py-3 text-caption text-muted-foreground">
                        <span>{t(($) => $.role_sources.snapshot_change_count, { count: snapshotComparison.data.total_changes })}</span>
                        <span>{snapshotOffset + 1}–{Math.min(snapshotOffset + snapshotComparison.data.limit, snapshotComparison.data.total_changes)}</span>
                      </div>
                      <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                        {snapshotComparison.data.changes.map((change) => (
                          <div key={`${change.object_kind}:${change.parent_id ?? ""}:${change.object_id}`} className="flex items-start justify-between gap-3 px-4 py-3">
                            <div className="min-w-0">
                              <div className="truncate text-body font-medium">{change.display_name || change.object_id}</div>
                              <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                                {change.object_kind} · {change.parent_id ? `${change.parent_id} / ` : ""}{change.object_id}
                              </div>
                            </div>
                            <Badge variant={snapshotOperationVariant(change.operation)}>
                              {t(($) => $.role_sources[snapshotOperationTranslationKey(change.operation)])}
                            </Badge>
                          </div>
                        ))}
                      </div>
                      <div className="flex justify-end gap-2 border-t border-surface-border p-3">
                        <Button variant="outline" size="sm" disabled={snapshotOffset === 0} onClick={() => setSnapshotOffset(Math.max(0, snapshotOffset - 100))}>
                          {t(($) => $.role_sources.snapshot_previous)}
                        </Button>
                        <Button variant="outline" size="sm" disabled={snapshotOffset + 100 >= snapshotComparison.data.total_changes} onClick={() => setSnapshotOffset(snapshotOffset + 100)}>
                          {t(($) => $.role_sources.snapshot_next)}
                        </Button>
                      </div>
                    </>
                  )}
                </>
              )}
            </SettingsCard>
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.role_sources.scan_history_title)}
            description={t(($) => $.role_sources.scan_history_description)}
          >
            <SettingsCard>
              <div className="grid gap-3 border-b border-surface-border p-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="role-source-scan-status-filter">{t(($) => $.role_sources.scan_history_status_filter)}</Label>
                  <Select
                    items={[
                      { value: "all", label: t(($) => $.role_sources.scan_history_status_all) },
                      { value: "succeeded", label: t(($) => $.role_sources.scan_status_succeeded) },
                      { value: "failed", label: t(($) => $.role_sources.scan_status_failed) },
                      { value: "cancelled", label: t(($) => $.role_sources.scan_status_cancelled) },
                      { value: "queued", label: t(($) => $.role_sources.scan_status_queued) },
                      { value: "claimed", label: t(($) => $.role_sources.scan_status_claimed) },
                    ]}
                    value={scanStatusFilter}
                    onValueChange={(value) => setScanStatusFilter(value ?? "all")}
                  >
                    <SelectTrigger id="role-source-scan-status-filter" className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">{t(($) => $.role_sources.scan_history_status_all)}</SelectItem>
                      <SelectItem value="succeeded">{t(($) => $.role_sources.scan_status_succeeded)}</SelectItem>
                      <SelectItem value="failed">{t(($) => $.role_sources.scan_status_failed)}</SelectItem>
                      <SelectItem value="cancelled">{t(($) => $.role_sources.scan_status_cancelled)}</SelectItem>
                      <SelectItem value="queued">{t(($) => $.role_sources.scan_status_queued)}</SelectItem>
                      <SelectItem value="claimed">{t(($) => $.role_sources.scan_status_claimed)}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="role-source-scan-code-filter">{t(($) => $.role_sources.scan_history_code_filter)}</Label>
                  <Input
                    id="role-source-scan-code-filter"
                    value={scanCodeFilter}
                    onChange={(event) => setScanCodeFilter(event.target.value)}
                    placeholder={t(($) => $.role_sources.scan_history_code_placeholder)}
                    autoComplete="off"
                  />
                </div>
              </div>
              {scanHistory.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.loading)}
                </div>
              ) : scanHistory.isError ? (
                <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.scan_history_load_failed)}</div>
              ) : !scanHistory.data?.length ? (
                <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.scan_history_empty)}</div>
              ) : !filteredScanHistory.length ? (
                <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.scan_history_no_matches)}</div>
              ) : (
                <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                  {filteredScanHistory.map((scan) => (
                    <div key={scan.id} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 text-body font-medium">
                          {t(($) => $.role_sources[scanStatusTranslationKey(scan.status)])}
                          {scan.error_code ? <span className="font-mono text-caption text-destructive">{scan.error_code}</span> : null}
                        </div>
                        <div className="mt-1 font-mono text-caption text-muted-foreground">
                          {shortDigest(scan.snapshot_digest)} · {scan.completed_at ?? scan.requested_at}
                        </div>
                        {scan.error_code ? (
                          <div className="mt-1 text-caption text-muted-foreground">
                            {t(($) => $.role_sources[scanRecoveryTranslationKey(scan.error_code ?? "")])}
                          </div>
                        ) : null}
                      </div>
                      <Badge variant={scan.status === "failed" ? "destructive" : scan.status === "succeeded" ? "secondary" : "outline"}>
                        {scan.expected_adapter_version}
                      </Badge>
                    </div>
                  ))}
                </div>
              )}
            </SettingsCard>
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.role_sources.receipts_title)}
            description={t(($) => $.role_sources.receipts_description)}
          >
            <SettingsCard>
              {applyHistory.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.loading)}
                </div>
              ) : applyHistory.isError ? (
                <div className="p-4 text-caption text-destructive">{t(($) => $.role_sources.receipts_load_failed)}</div>
              ) : !applyHistory.data?.length ? (
                <div className="p-4 text-caption text-muted-foreground">{t(($) => $.role_sources.receipts_empty)}</div>
              ) : (
                <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                  {applyHistory.data.map((apply) => (
                    <div key={apply.id} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
                      <div className="min-w-0">
                        <div className="font-mono text-caption text-foreground">{shortDigest(apply.receipt.receipt_digest)}</div>
                        <div className="mt-1 text-caption text-muted-foreground">
                          {t(($) => $.role_sources.receipt_counts, {
                            created: apply.receipt.counts.created,
                            updated: apply.receipt.counts.updated,
                            unchanged: apply.receipt.counts.unchanged,
                            archived: apply.receipt.counts.archived,
                            retained: apply.receipt.counts.retained,
                          })}
                        </div>
                        <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                          {shortDigest(apply.receipt.snapshot_digest)} · {apply.completed_at ?? "—"}
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        {roleSourceApplyEnabled && canManage && apply.status === "succeeded" && apply.receipt.snapshot_digest !== selected.current_snapshot_digest ? (
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={createRollbackPlan.isPending}
                            onClick={() => setRollbackSnapshotDigest(apply.receipt.snapshot_digest)}
                          >
                            <Repeat2 className="h-4 w-4" />
                            {t(($) => $.role_sources.rollback_create)}
                          </Button>
                        ) : null}
                        <Badge variant={apply.status === "succeeded" ? "secondary" : "outline"}>{apply.status}</Badge>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </SettingsCard>
          </SettingsSection>

          <SettingsSection
            title={t(($) => $.role_sources.failed_applies_title)}
            description={t(($) => $.role_sources.failed_applies_description)}
          >
            <SettingsCard>
              <div className="border-b border-surface-border px-4 py-3 text-caption leading-5 text-muted-foreground">
                {t(($) => $.role_sources.failed_applies_commit_notice)}
              </div>
              {applyFailures.isLoading ? (
                <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.loading)}
                </div>
              ) : applyFailures.isError ? (
                <div className="p-4 text-caption text-destructive">
                  {t(($) => $.role_sources.failed_applies_load_failed)}
                </div>
              ) : !applyFailures.data?.length ? (
                <div className="p-4 text-caption text-muted-foreground">
                  {t(($) => $.role_sources.no_failed_applies)}
                </div>
              ) : (
                <div className="max-h-80 divide-y divide-surface-border overflow-y-auto">
                  {applyFailures.data.map((failure) => (
                    <div key={failure.id} className="flex items-start justify-between gap-4 px-4 py-3">
                      <div className="min-w-0">
                        <div className="flex items-center gap-2 text-body font-medium">
                          <AlertTriangle className="h-4 w-4 shrink-0 text-amber-600" />
                          <span className="font-mono">{failure.failure_code}</span>
                        </div>
                        <div className="mt-1 text-caption text-muted-foreground">
                          {t(($) => $.role_sources.failed_apply_metadata, {
                            mode: failure.mode,
                            stage: failure.failure_stage,
                            time: failure.occurred_at,
                          })}
                        </div>
                        <div className="mt-0.5 font-mono text-caption text-muted-foreground">
                          {shortDigest(failure.plan_digest)}
                        </div>
                        <div className="mt-1 max-w-2xl text-caption text-muted-foreground">
                          {t(($) => $.role_sources[applyFailureRecoveryTranslationKey(failure.failure_code, failure.failure_stage)])}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center justify-end gap-2">
                        {failure.plan_digest === latest?.plan.plan_digest ? (
                          <Button variant="outline" size="sm" onClick={() => planSectionRef.current?.scrollIntoView({ behavior: "smooth", block: "start" })}>
                            {t(($) => $.role_sources.failed_apply_review_plan)}
                          </Button>
                        ) : null}
                        <Button variant="outline" size="sm" onClick={() => void reconcileApplyEvidence()}>
                          <RefreshCw className="h-4 w-4" />
                          {t(($) => $.role_sources.failed_apply_reconcile)}
                        </Button>
                        <Badge variant="outline">{failure.failure_stage}</Badge>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </SettingsCard>
          </SettingsSection>
        </>
      ) : null}

      <AlertDialog open={pendingAction !== null} onOpenChange={(open) => !open && !savingLifecycle && setPendingAction(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.role_sources.lifecycle_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {pendingAction ? t(($) => $.role_sources[`lifecycle_confirm_${pendingAction}`]) : ""}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={savingLifecycle}>{t(($) => $.role_sources.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              disabled={savingLifecycle || pendingAction === null}
              onClick={(event) => {
                event.preventDefault();
                if (pendingAction) void updateLifecycle(pendingAction);
              }}
            >
              {savingLifecycle ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={applyDialogOpen} onOpenChange={(open) => !open && !applyPlan.isPending && setApplyDialogOpen(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.role_sources.apply_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.role_sources.apply_confirm_description, {
                digest: shortDigest(latest?.plan.plan_digest),
                create: latest?.plan.summary.create ?? 0,
                update: latest?.plan.summary.update ?? 0,
                archive: archiveCandidates.filter((action) => archiveDecisions[objectRefKey(action.ref)] === "archive").length,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={applyPlan.isPending}>{t(($) => $.role_sources.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              disabled={applyPlan.isPending || !approvedApproval}
              onClick={(event) => {
                event.preventDefault();
                void applyLatestPlan();
              }}
            >
              {applyPlan.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.apply_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={Boolean(rollbackSnapshotDigest)}
        onOpenChange={(open) => !open && !createRollbackPlan.isPending && setRollbackSnapshotDigest("")}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.role_sources.rollback_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.role_sources.rollback_confirm_description, {
                digest: shortDigest(rollbackSnapshotDigest),
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={createRollbackPlan.isPending}>{t(($) => $.role_sources.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              disabled={createRollbackPlan.isPending || !rollbackSnapshotDigest}
              onClick={(event) => {
                event.preventDefault();
                void generateRollbackPlan();
              }}
            >
              {createRollbackPlan.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.rollback_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <Dialog open={rebindOpen} onOpenChange={(open) => !savingLifecycle && setRebindOpen(open)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.role_sources.rebind_title)}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <p className="text-caption text-muted-foreground">{t(($) => $.role_sources.rebind_description)}</p>
            <div className="space-y-2">
              <Label htmlFor="role-source-runtime-id">{t(($) => $.role_sources.rebind_runtime_id)}</Label>
              {runtimes.isLoading ? (
                <div className="flex min-h-9 items-center gap-2 text-caption text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  {t(($) => $.role_sources.rebind_runtimes_loading)}
                </div>
              ) : runtimes.isError ? (
                <div className="text-caption text-destructive">{t(($) => $.role_sources.rebind_runtimes_failed)}</div>
              ) : rebindRuntimeOptions.length ? (
                <Select
                  items={rebindRuntimeOptions}
                  value={rebindRuntimeId || null}
                  onValueChange={(value) => setRebindRuntimeId(value ?? "")}
                >
                  <SelectTrigger id="role-source-runtime-id" className="w-full">
                    <SelectValue placeholder={t(($) => $.role_sources.rebind_runtime_placeholder)} />
                  </SelectTrigger>
                  <SelectContent>
                    {rebindRuntimeOptions.map((runtime) => (
                      <SelectItem key={runtime.value} value={runtime.value}>{runtime.label}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <div className="text-caption text-muted-foreground">{t(($) => $.role_sources.rebind_runtimes_empty)}</div>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="role-source-config-id">{t(($) => $.role_sources.rebind_config_id)}</Label>
              <Input id="role-source-config-id" value={rebindConfigId} onChange={(event) => setRebindConfigId(event.target.value)} autoComplete="off" />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={savingLifecycle} onClick={() => setRebindOpen(false)}>
              {t(($) => $.role_sources.cancel)}
            </Button>
            <Button
              disabled={savingLifecycle || runtimes.isLoading || runtimes.isError || !rebindRuntimeId.trim() || !rebindConfigId.trim()}
              onClick={() => void updateLifecycle("rebind", { runtime_id: rebindRuntimeId.trim(), daemon_config_id: rebindConfigId.trim() })}
            >
              {savingLifecycle ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.rebind)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={createHoldOpen} onOpenChange={(open) => {
        if (savingHold) return;
        setCreateHoldOpen(open);
        if (!open) setCreateHoldRequestKey("");
      }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.role_sources.legal_hold_create_title)}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <p className="text-caption text-muted-foreground">{t(($) => $.role_sources.legal_hold_create_description)}</p>
            <div className="space-y-2">
              <Label>{t(($) => $.role_sources.legal_hold_scope)}</Label>
              <Select
                items={[
                  { value: "source", label: t(($) => $.role_sources.legal_hold_scope_source) },
                  { value: "snapshot", label: t(($) => $.role_sources.legal_hold_scope_snapshot) },
                ]}
                value={holdScope}
                onValueChange={(value) => value && setHoldScope(value as RoleSourceLegalHoldScope)}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="source">{t(($) => $.role_sources.legal_hold_scope_source)}</SelectItem>
                  <SelectItem value="snapshot">{t(($) => $.role_sources.legal_hold_scope_snapshot)}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {holdScope === "snapshot" ? (
              <div className="space-y-2">
                <Label htmlFor="legal-hold-snapshot">{t(($) => $.role_sources.legal_hold_snapshot_digest)}</Label>
                <Input id="legal-hold-snapshot" value={holdSnapshotDigest} onChange={(event) => setHoldSnapshotDigest(event.target.value)} placeholder="sha256:…" autoComplete="off" />
              </div>
            ) : null}
            <div className="space-y-2">
              <Label>{t(($) => $.role_sources.legal_hold_reason)}</Label>
              <Select
                items={[
                  { value: "investigation", label: t(($) => $.role_sources.legal_hold_reason_investigation) },
                  { value: "litigation", label: t(($) => $.role_sources.legal_hold_reason_litigation) },
                  { value: "regulatory", label: t(($) => $.role_sources.legal_hold_reason_regulatory) },
                  { value: "customer_request", label: t(($) => $.role_sources.legal_hold_reason_customer_request) },
                  { value: "security_incident", label: t(($) => $.role_sources.legal_hold_reason_security_incident) },
                ]}
                value={holdReason}
                onValueChange={(value) => value && setHoldReason(value as RoleSourceLegalHoldReason)}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="investigation">{t(($) => $.role_sources.legal_hold_reason_investigation)}</SelectItem>
                  <SelectItem value="litigation">{t(($) => $.role_sources.legal_hold_reason_litigation)}</SelectItem>
                  <SelectItem value="regulatory">{t(($) => $.role_sources.legal_hold_reason_regulatory)}</SelectItem>
                  <SelectItem value="customer_request">{t(($) => $.role_sources.legal_hold_reason_customer_request)}</SelectItem>
                  <SelectItem value="security_incident">{t(($) => $.role_sources.legal_hold_reason_security_incident)}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="legal-hold-reference">{t(($) => $.role_sources.legal_hold_reference_digest)}</Label>
              <Input id="legal-hold-reference" value={holdReferenceDigest} onChange={(event) => setHoldReferenceDigest(event.target.value)} placeholder="sha256:…" autoComplete="off" />
              <p className="text-caption text-muted-foreground">{t(($) => $.role_sources.legal_hold_reference_hint)}</p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={savingHold} onClick={() => {
              setCreateHoldOpen(false);
              setCreateHoldRequestKey("");
            }}>{t(($) => $.role_sources.cancel)}</Button>
            <Button disabled={savingHold || !createHoldValid || !createReferenceValid} onClick={() => void createLegalHold()}>
              {savingHold ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.legal_hold_create)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={holdToRelease !== null} onOpenChange={(open) => {
        if (!open && !savingHold) {
          setHoldToRelease(null);
          setReleaseHoldRequestKey("");
        }
      }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.role_sources.legal_hold_release_title)}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <p className="text-caption text-muted-foreground">{t(($) => $.role_sources.legal_hold_release_description)}</p>
            <div className="space-y-2">
              <Label>{t(($) => $.role_sources.legal_hold_release_reason)}</Label>
              <Select
                items={[
                  { value: "resolved", label: t(($) => $.role_sources.legal_hold_release_reason_resolved) },
                  { value: "court_order", label: t(($) => $.role_sources.legal_hold_release_reason_court_order) },
                  { value: "entered_in_error", label: t(($) => $.role_sources.legal_hold_release_reason_entered_in_error) },
                  { value: "authorization_expired", label: t(($) => $.role_sources.legal_hold_release_reason_authorization_expired) },
                ]}
                value={releaseReason}
                onValueChange={(value) => value && setReleaseReason(value as RoleSourceLegalHoldReleaseReason)}
              >
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="resolved">{t(($) => $.role_sources.legal_hold_release_reason_resolved)}</SelectItem>
                  <SelectItem value="court_order">{t(($) => $.role_sources.legal_hold_release_reason_court_order)}</SelectItem>
                  <SelectItem value="entered_in_error">{t(($) => $.role_sources.legal_hold_release_reason_entered_in_error)}</SelectItem>
                  <SelectItem value="authorization_expired">{t(($) => $.role_sources.legal_hold_release_reason_authorization_expired)}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="legal-hold-release-reference">{t(($) => $.role_sources.legal_hold_reference_digest)}</Label>
              <Input id="legal-hold-release-reference" value={releaseReferenceDigest} onChange={(event) => setReleaseReferenceDigest(event.target.value)} placeholder="sha256:…" autoComplete="off" />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={savingHold} onClick={() => {
              setHoldToRelease(null);
              setReleaseHoldRequestKey("");
            }}>{t(($) => $.role_sources.cancel)}</Button>
            <Button variant="destructive" disabled={savingHold || !releaseReferenceValid} onClick={() => void releaseLegalHold()}>
              {savingHold ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.legal_hold_release)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={retentionDialogOpen} onOpenChange={(open) => {
        if (savingRetention) return;
        setRetentionDialogOpen(open);
        if (!open) setRetentionRequestKey("");
      }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.role_sources.retention_edit_title)}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-2">
            <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-caption leading-5 text-muted-foreground">
              {t(($) => $.role_sources.retention_edit_warning)}
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="role-source-retention-enabled">{t(($) => $.role_sources.retention_enable)}</Label>
              <Switch id="role-source-retention-enabled" checked={retentionEnabled} onCheckedChange={setRetentionEnabled} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="role-source-retention-days">{t(($) => $.role_sources.retention_minimum_days)}</Label>
              <Input id="role-source-retention-days" type="number" min={30} max={3650} value={retentionMinimumDays} onChange={(event) => setRetentionMinimumDays(event.target.value)} />
            </div>
            <div className="space-y-2">
              <Label htmlFor="role-source-retention-successful">{t(($) => $.role_sources.retention_keep_successful)}</Label>
              <Input id="role-source-retention-successful" type="number" min={2} max={100} value={retentionKeepSuccessful} onChange={(event) => setRetentionKeepSuccessful(event.target.value)} />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={savingRetention} onClick={() => {
              setRetentionDialogOpen(false);
              setRetentionRequestKey("");
            }}>{t(($) => $.role_sources.cancel)}</Button>
            <Button variant={retentionEnabled ? "destructive" : "default"} disabled={savingRetention || !retentionInputValid || !retention.data} onClick={() => void updateRetentionPolicy()}>
              {savingRetention ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              {t(($) => $.role_sources.retention_save)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </SettingsTab>
  );
}
