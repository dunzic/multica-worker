"use client";

import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, CircleSlash2, Loader2 } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import {
  roleSourceListOptions,
  roleSourcePlanListOptions,
  type RoleSourcePlanAction,
} from "@multica/core/role-sources";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

function shortDigest(value: string | null | undefined) {
  if (!value) return "—";
  return `${value.slice(0, 14)}…${value.slice(-8)}`;
}

function operationVariant(operation: RoleSourcePlanAction["operation"]) {
  if (operation === "blocked" || operation === "archive_candidate") return "destructive" as const;
  if (operation === "unchanged") return "outline" as const;
  return "secondary" as const;
}

export function RoleSourcesTab() {
  const { t } = useT("settings");
  const workspaceId = useCurrentWorkspace()?.id ?? "";
  const sources = useQuery({
    ...roleSourceListOptions(workspaceId),
    enabled: Boolean(workspaceId),
  });
  const [selectedId, setSelectedId] = React.useState("");

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
  const latest = plans.data?.[0];

  return (
    <SettingsTab
      title={t(($) => $.role_sources.title)}
      description={t(($) => $.role_sources.description)}
    >
      <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-caption leading-5 text-muted-foreground">
        <div className="flex gap-2 text-foreground">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
          <span>{t(($) => $.role_sources.read_only_notice)}</span>
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
                </span>
                <Badge variant={source.state === "active" ? "secondary" : "outline"}>{source.state}</Badge>
              </button>
            ))
          )}
        </SettingsCard>
      </SettingsSection>

      {selected ? (
        <SettingsSection
          title={t(($) => $.role_sources.latest_plan_title)}
          description={t(($) => $.role_sources.latest_plan_description, { name: selected.name })}
        >
          <SettingsCard>
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
      ) : null}
    </SettingsTab>
  );
}
