import { z } from "zod";

export const REPORT_FIELD_LIMIT = 20;
export const REPORT_FIELD_TYPES = [
  "text",
  "number",
  "date",
  "boolean",
  "select",
  "multi_select",
] as const;
export type ReportValue = string | number | boolean | string[] | null;
export interface ReportField {
  id: string;
  label: string;
  /** Unknown server types render read-only instead of becoming text inputs. */
  type: string;
  required: boolean;
  options: string[];
}

export const ReportFieldSchema = z.object({
  id: z.string().min(1),
  label: z.string().min(1),
  type: z.string(),
  required: z.boolean().optional().default(false),
  options: z
    .array(z.string())
    .nullish()
    .transform((v) => v ?? []),
});
const ValueSchema = z.union([
  z.string(),
  z.number().finite(),
  z.boolean(),
  z.array(z.string()),
  z.null(),
]);
export const ReportRowSchema = z
  .object({
    id: z.string().uuid(),
    label: z.string(),
    source_attachment_id: z.string().optional().default(""),
    values: z.record(z.string(), ValueSchema),
    revision: z.number().int().positive(),
    archived: z.boolean().optional().default(false),
    updated_at: z.string(),
  })
  .transform((row) => ({
    id: row.id,
    label: row.label,
    sourceAttachmentId: row.source_attachment_id,
    values: row.values,
    revision: row.revision,
    archived: row.archived,
    updatedAt: row.updated_at,
  }));
export type ReportRow = z.output<typeof ReportRowSchema>;

export const ReportFeedbackPageSchema = z
  .object({
    fields: z.array(ReportFieldSchema),
    schema_revision: z.number().int().nonnegative(),
    rows: z.array(ReportRowSchema),
    next_cursor: z.string().optional().default(""),
    has_records: z.boolean().optional().default(false),
    can_manage: z.boolean().optional().default(false),
    can_create: z.boolean().optional().default(false),
    can_edit: z.boolean().optional().default(false),
  })
  .transform((page) => ({
    fields: page.fields,
    schemaRevision: page.schema_revision,
    rows: page.rows,
    nextCursor: page.next_cursor,
    hasRecords: page.has_records || page.rows.length > 0,
    canManage: page.can_manage,
    canCreate: page.can_create,
    canEdit: page.can_edit,
  }));
export type ReportFeedbackPage = z.output<typeof ReportFeedbackPageSchema>;

export const ReportHistoryPageSchema = z
  .object({
    changes: z.array(
      z
        .object({
          id: z.string(),
          row_id: z.string(),
          kind: z.string(),
          actor_type: z.string(),
          actor_id: z.string(),
          actor_name: z.string(),
          before: z.unknown(),
          after: z.unknown(),
          created_at: z.string(),
        })
        .transform((change) => ({
          id: change.id,
          rowId: change.row_id,
          kind: change.kind,
          actorType: change.actor_type,
          actorId: change.actor_id,
          actorName: change.actor_name,
          before: change.before,
          after: change.after,
          createdAt: change.created_at,
        })),
    ),
    next_cursor: z.string().optional().default(""),
  })
  .transform((page) => ({
    changes: page.changes,
    nextCursor: page.next_cursor,
  }));
export type ReportHistoryPage = z.output<typeof ReportHistoryPageSchema>;
export const ReportSchemaSavedSchema = z.object({
  fields: z.array(ReportFieldSchema),
  revision: z.number().int().positive(),
});
export const ReportArchivedSchema = z.object({ archived: z.literal(true) });

export interface SaveReportRow {
  expectedRevision: number;
  schemaRevision: number;
  label: string;
  sourceAttachmentId: string;
  values: Record<string, ReportValue>;
}

export function isReportFieldEditable(field: ReportField): boolean {
  return REPORT_FIELD_TYPES.some((type) => type === field.type);
}

export function displayReportValue(value: unknown): string {
  if (value == null || value === "") return "—";
  if (Array.isArray(value)) return value.map(String).join(", ");
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
