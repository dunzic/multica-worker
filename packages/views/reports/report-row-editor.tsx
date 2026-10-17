"use client";

import { useEffect, useRef, useState } from "react";
import {
  type ReportField,
  type ReportFeedbackPage,
  type ReportRow,
  type ReportValue,
  type SaveReportRow,
  useReportFeedbackMutations,
} from "@multica/core/report-feedback";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";
import { isReportConflict, ReportFormDialog } from "./report-form-dialog";
import { selectClass } from "./report-schema-editor";

function ValueInput({
  field,
  value,
  change,
}: {
  field: ReportField;
  value: ReportValue | undefined;
  change: (value: ReportValue) => void;
}) {
  const { t } = useT("editor");
  if (field.type === "boolean")
    return (
      <select
        name={field.id}
        aria-label={field.label}
        className={selectClass}
        required={field.required}
        value={value == null ? "" : String(value)}
        onChange={(e) =>
          change(e.target.value === "" ? null : e.target.value === "true")
        }
      >
        <option value="">{t(($) => $.report_feedback.unset)}</option>
        <option value="true">{t(($) => $.report_feedback.yes)}</option>
        <option value="false">{t(($) => $.report_feedback.no)}</option>
      </select>
    );
  if (field.type === "select")
    return (
      <select
        name={field.id}
        aria-label={field.label}
        className={selectClass}
        required={field.required}
        value={typeof value === "string" ? value : ""}
        onChange={(e) => change(e.target.value || null)}
      >
        <option value="">{t(($) => $.report_feedback.unset)}</option>
        {field.options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    );
  if (field.type === "multi_select")
    return (
      <div
        className="flex flex-wrap gap-3"
        role="group"
        aria-label={field.label}
      >
        {field.options.map((o) => (
          <label key={o} className="flex items-center gap-2">
            <input
              type="checkbox"
              name={field.id}
              value={o}
              checked={Array.isArray(value) && value.includes(o)}
              onChange={(e) => {
                const selected = Array.isArray(value) ? value : [];
                change(
                  e.target.checked
                    ? [...selected, o]
                    : selected.filter((v) => v !== o),
                );
              }}
            />
            {o}
          </label>
        ))}
      </div>
    );
  if (field.type === "text")
    return (
      <Textarea
        name={field.id}
        autoComplete="off"
        aria-label={field.label}
        maxLength={4000}
        required={field.required}
        value={typeof value === "string" ? value : ""}
        onChange={(e) => change(e.target.value)}
      />
    );
  return (
    <Input
      name={field.id}
      autoComplete="off"
      aria-label={field.label}
      type={field.type === "number" ? "number" : "date"}
      step={field.type === "number" ? "any" : undefined}
      min={field.type === "number" ? -1e15 : undefined}
      max={field.type === "number" ? 1e15 : "9999-12-31"}
      required={field.required}
      value={
        typeof value === "number" || typeof value === "string" ? value : ""
      }
      onChange={(e) =>
        change(
          e.target.value === ""
            ? null
            : field.type === "number"
              ? Number(e.target.value)
              : e.target.value,
        )
      }
    />
  );
}

export function ReportRowEditor({
  wsId,
  attachmentId,
  page,
  row,
  onEditingRowChange,
}: {
  wsId: string;
  attachmentId: string;
  page: ReportFeedbackPage;
  row?: ReportRow;
  onEditingRowChange?: (row: ReportRow | null) => void;
}) {
  const { t } = useT("editor");
  const mutation = useReportFeedbackMutations(wsId, attachmentId).row;
  const [draft, setDraft] = useState<
    | (SaveReportRow & { id: string; fields: ReportField[]; initial: string })
    | null
  >(null);
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState(false);
  const draftOpen = useRef(false);
  useEffect(
    () => () => {
      if (draftOpen.current) onEditingRowChange?.(null);
    },
    [onEditingRowChange],
  );
  const label = t(($) =>
    row ? $.report_feedback.edit_record : $.report_feedback.add_record,
  );
  return (
    <ReportFormDialog
      open={!!draft}
      onOpenChange={(open) => {
        draftOpen.current = open;
        onEditingRowChange?.(open && row ? row : null);
        setError("");
        setConflict(false);
        mutation.reset();
        if (!open) {
          setDraft(null);
          return;
        }
        const input = {
          label: row?.label ?? "",
          sourceAttachmentId: row?.sourceAttachmentId ?? "",
          values: structuredClone(row?.values ?? {}),
        };
        setDraft({
          ...input,
          id: row?.id ?? crypto.randomUUID(),
          fields: structuredClone(page.fields),
          expectedRevision: row?.revision ?? 0,
          schemaRevision: page.schemaRevision,
          initial: JSON.stringify(input),
        });
      }}
      trigger={label}
      title={label}
      description={t(($) => $.report_feedback.record_help)}
      dirty={
        !!draft &&
        draft.initial !==
          JSON.stringify({
            label: draft.label,
            sourceAttachmentId: draft.sourceAttachmentId,
            values: draft.values,
          })
      }
      pending={mutation.isPending}
      submitDisabled={conflict}
      error={error}
      onSubmit={async (event) => {
        event.preventDefault();
        if (!draft || mutation.isPending || conflict) return;
        if (
          !draft.label.trim() ||
          draft.fields.some(
            (f) =>
              f.required &&
              (draft.values[f.id] == null ||
                draft.values[f.id] === "" ||
                (Array.isArray(draft.values[f.id]) &&
                  (draft.values[f.id] as string[]).length === 0)),
          )
        ) {
          setError(t(($) => $.report_feedback.required_missing));
          return;
        }
        try {
          await mutation.mutateAsync(draft);
          setDraft(null);
          draftOpen.current = false;
          onEditingRowChange?.(null);
        } catch (e) {
          setConflict(isReportConflict(e));
          setError(
            t(($) =>
              isReportConflict(e)
                ? $.report_feedback.conflict
                : $.report_feedback.save_failed,
            ),
          );
        }
      }}
    >
      {draft && (
        <>
          <label className="block space-y-1 text-body-sm">
            {t(($) => $.report_feedback.record_name)}
            <Input
              name="record-label"
              autoComplete="off"
              required
              maxLength={200}
              value={draft.label}
              onChange={(e) =>
                setDraft((d) => d && { ...d, label: e.target.value })
              }
            />
          </label>
          {draft.fields.map((field) => (
            <div key={field.id} className="space-y-1 text-body-sm">
              <p>
                {field.label}
                {field.required && " *"}
              </p>
              <ValueInput
                field={field}
                value={draft.values[field.id]}
                change={(value) =>
                  setDraft(
                    (d) =>
                      d && { ...d, values: { ...d.values, [field.id]: value } },
                  )
                }
              />
            </div>
          ))}
          <label className="block space-y-1 text-body-sm">
            {t(($) => $.report_feedback.source_id)}
            <Input
              name="source-attachment-id"
              autoComplete="off"
              value={draft.sourceAttachmentId}
              pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
              onChange={(e) =>
                setDraft(
                  (d) =>
                    d && { ...d, sourceAttachmentId: e.target.value.trim() },
                )
              }
            />
            <span className="text-caption text-muted-foreground">
              {t(($) => $.report_feedback.source_help)}
            </span>
          </label>
        </>
      )}
    </ReportFormDialog>
  );
}
