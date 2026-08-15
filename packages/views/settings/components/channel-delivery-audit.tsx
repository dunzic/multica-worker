"use client";

import * as React from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, CircleAlert, Loader2, MessageSquareReply } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { channelDeliveryListOptions } from "@multica/core/channel-deliveries";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useT } from "../../i18n";
import { SettingsCard } from "./settings-layout";

function shortIdentifier(value: string | null | undefined) {
  if (!value) return "—";
  if (value.length <= 24) return value;
  return `${value.slice(0, 12)}…${value.slice(-8)}`;
}

function deliveryStatusKey(status: string) {
  switch (status) {
    case "pending": return "status_pending" as const;
    case "delivered": return "status_delivered" as const;
    case "readback": return "status_readback" as const;
    case "failed": return "status_failed" as const;
    case "ambiguous": return "status_ambiguous" as const;
    case "retry_authorized": return "status_retry_authorized" as const;
    case "reconciled": return "status_reconciled" as const;
    default: return "status_unknown" as const;
  }
}

function reconciliationOutcomeKey(outcome: string) {
  switch (outcome) {
    case "confirmed_delivered": return "outcome_confirmed_delivered" as const;
    case "confirmed_not_delivered": return "outcome_confirmed_not_delivered" as const;
    case "closed_no_retry": return "outcome_closed_no_retry" as const;
    default: return "outcome_unknown" as const;
  }
}

export function ChannelDeliveryAudit() {
  const { t } = useT("settings");
  const workspaceId = useCurrentWorkspace()?.id ?? "";
  const deliveries = useQuery({
    ...channelDeliveryListOptions(workspaceId),
    enabled: Boolean(workspaceId),
  });
  const [statusFilter, setStatusFilter] = React.useState("all");
  const visible = (deliveries.data ?? []).filter((delivery) =>
    statusFilter === "all" || delivery.status === statusFilter,
  );

  return (
    <SettingsCard>
      <div className="border-b border-surface-border p-4">
        <div className="max-w-xs space-y-2">
          <Label htmlFor="channel-delivery-status-filter">{t(($) => $.channel_delivery.filter_status)}</Label>
          <Select
            items={[
              { value: "all", label: t(($) => $.channel_delivery.filter_all) },
              { value: "pending", label: t(($) => $.channel_delivery.status_pending) },
              { value: "delivered", label: t(($) => $.channel_delivery.status_delivered) },
              { value: "readback", label: t(($) => $.channel_delivery.status_readback) },
              { value: "failed", label: t(($) => $.channel_delivery.status_failed) },
              { value: "ambiguous", label: t(($) => $.channel_delivery.status_ambiguous) },
              { value: "retry_authorized", label: t(($) => $.channel_delivery.status_retry_authorized) },
              { value: "reconciled", label: t(($) => $.channel_delivery.status_reconciled) },
            ]}
            value={statusFilter}
            onValueChange={(value) => setStatusFilter(value ?? "all")}
          >
            <SelectTrigger id="channel-delivery-status-filter" className="w-full"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t(($) => $.channel_delivery.filter_all)}</SelectItem>
              <SelectItem value="pending">{t(($) => $.channel_delivery.status_pending)}</SelectItem>
              <SelectItem value="delivered">{t(($) => $.channel_delivery.status_delivered)}</SelectItem>
              <SelectItem value="readback">{t(($) => $.channel_delivery.status_readback)}</SelectItem>
              <SelectItem value="failed">{t(($) => $.channel_delivery.status_failed)}</SelectItem>
              <SelectItem value="ambiguous">{t(($) => $.channel_delivery.status_ambiguous)}</SelectItem>
              <SelectItem value="retry_authorized">{t(($) => $.channel_delivery.status_retry_authorized)}</SelectItem>
              <SelectItem value="reconciled">{t(($) => $.channel_delivery.status_reconciled)}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      {deliveries.isLoading ? (
        <div className="flex min-h-20 items-center justify-center gap-2 text-caption text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          {t(($) => $.channel_delivery.loading)}
        </div>
      ) : deliveries.isError ? (
        <div className="p-4 text-caption text-destructive">{t(($) => $.channel_delivery.load_failed)}</div>
      ) : !deliveries.data?.length ? (
        <div className="p-4 text-caption text-muted-foreground">{t(($) => $.channel_delivery.empty)}</div>
      ) : !visible.length ? (
        <div className="p-4 text-caption text-muted-foreground">{t(($) => $.channel_delivery.no_matches)}</div>
      ) : (
        <div className="max-h-96 divide-y divide-surface-border overflow-y-auto">
          {visible.map((delivery) => (
            <div key={delivery.id} className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
              <div className="min-w-0 space-y-1">
                <div className="flex flex-wrap items-center gap-2 text-body font-medium">
                  {delivery.status === "readback" ? <MessageSquareReply className="h-4 w-4" />
                    : delivery.status === "failed" ? <CircleAlert className="h-4 w-4 text-destructive" />
                      : delivery.status === "ambiguous" ? <CircleAlert className="h-4 w-4 text-warning" />
                      : delivery.status === "retry_authorized" ? <Loader2 className="h-4 w-4 animate-spin text-warning" />
                      : delivery.status === "pending" ? <Loader2 className="h-4 w-4 animate-spin" />
                        : <CheckCircle2 className="h-4 w-4" />}
                  {t(($) => $.channel_delivery[deliveryStatusKey(delivery.status)])}
                  <Badge variant="outline">{delivery.channel_type}</Badge>
                </div>
                <div className="font-mono text-caption text-muted-foreground">
                  {t(($) => $.channel_delivery.task)}: {shortIdentifier(delivery.task_id)} · {t(($) => $.channel_delivery.correlation)}: {shortIdentifier(delivery.correlation_id)}
                </div>
                <div className="text-caption text-muted-foreground">
                  {t(($) => $.channel_delivery.metadata, {
                    operation: delivery.operation_kind,
                    attempts: delivery.attempt_count,
                    time: delivery.evidence?.readback_at || delivery.evidence?.ambiguous_at || delivery.evidence?.delivered_at || delivery.updated_at,
                  })}
                </div>
                {delivery.status === "readback" ? (
                  <div className="text-caption text-muted-foreground">{t(($) => $.channel_delivery.readback_explanation)}</div>
                ) : null}
                {delivery.status === "ambiguous" ? (
                  <div className="rounded-md border border-warning/30 bg-warning/10 px-2 py-1 text-caption">
                    {t(($) => $.channel_delivery.ambiguous_explanation)}
                  </div>
                ) : null}
                {delivery.reconciliation ? (
                  <div className="rounded-md border border-surface-border bg-surface-subtle px-2 py-1 text-caption">
                    {t(($) => $.channel_delivery.reconciliation, {
                      generation: delivery.reconciliation?.generation,
                      outcome: t(($) => $.channel_delivery[reconciliationOutcomeKey(delivery.reconciliation?.outcome ?? "")]),
                      reason: delivery.reconciliation?.reason_code,
                    })}
                    <div className="font-mono text-muted-foreground">
                      {t(($) => $.channel_delivery.reconciliation_evidence)}: {shortIdentifier(delivery.reconciliation.external_evidence_digest)}
                    </div>
                  </div>
                ) : null}
                {delivery.last_error_code && (delivery.status === "failed" || delivery.status === "ambiguous") ? (
                  <div className={`font-mono text-caption ${delivery.status === "ambiguous" ? "text-warning" : "text-destructive"}`}>
                    {t(($) => $.channel_delivery.error_code)}: {delivery.last_error_code}
                  </div>
                ) : null}
                {delivery.evidence_digest ? (
                  <div className="font-mono text-caption text-muted-foreground">
                    {t(($) => $.channel_delivery.evidence)}: {shortIdentifier(delivery.evidence_digest)}
                  </div>
                ) : null}
              </div>
              <Badge variant={delivery.status === "failed" ? "destructive" : delivery.status === "readback" || delivery.status === "ambiguous" || delivery.status === "reconciled" ? "secondary" : "outline"}>
                {delivery.attempt_count}
              </Badge>
            </div>
          ))}
        </div>
      )}
    </SettingsCard>
  );
}
