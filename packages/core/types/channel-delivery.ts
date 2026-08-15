export type ChannelDeliveryStatus = "pending" | "delivered" | "readback" | "failed" | "ambiguous" | "retry_authorized" | "reconciled" | string;
export type ChannelDeliveryAmbiguityReason = "response_unknown" | "partial_delivery" | "receipt_persist_failed" | "lease_expired" | "missing_provider_id";

export interface ChannelDeliveryEvidence {
  contract_version: string;
  delivery_id: string;
  correlation_id: string;
  workspace_id: string;
  task_id: string;
  chat_session_id: string;
  channel_type: string;
  channel_chat_id: string;
  operation_kind: string;
  payload_digest: string;
  status: string;
  attempt_count: number;
  external_message_id: string;
  delivered_at: string;
  readback_message_id?: string;
  readback_at?: string;
  ambiguity_reason?: ChannelDeliveryAmbiguityReason;
  ambiguous_at?: string;
}

export interface ChannelDeliveryReconciliation {
  generation: number;
  outcome: "confirmed_delivered" | "confirmed_not_delivered" | "closed_no_retry" | string;
  reason_code: string;
  external_evidence_digest: string;
  expected_ambiguity_evidence_digest: string;
  reconciliation_digest: string;
  created_at: string;
}

export interface ChannelDelivery {
  id: string;
  workspace_id: string;
  installation_id?: string;
  task_id: string;
  chat_session_id: string;
  channel_type: string;
  channel_chat_id: string;
  operation_kind: string;
  correlation_id: string;
  payload_digest: string;
  status: ChannelDeliveryStatus;
  attempt_count: number;
  external_message_id?: string;
  evidence_digest?: string;
  evidence?: ChannelDeliveryEvidence;
  last_error_code?: string;
  ambiguous_at?: string;
  reconciliation_count: number;
  last_reconciled_at?: string;
  reconciliation?: ChannelDeliveryReconciliation;
  created_at: string;
  updated_at: string;
}
