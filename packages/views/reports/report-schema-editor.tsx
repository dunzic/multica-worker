"use client";

import { useState } from "react";
import {
  REPORT_FIELD_LIMIT,
  REPORT_FIELD_TYPES,
  type ReportField,
  type ReportFeedbackPage,
  useReportFeedbackMutations,
} from "@multica/core/report-feedback";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";
import { isReportConflict, ReportFormDialog } from "./report-form-dialog";

const selectClass =
  "h-9 w-full rounded-md border border-input bg-background px-2 text-body text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring";
export { selectClass };

export function ReportSchemaEditor({
  wsId,
  attachmentId,
  page,
}: {
  wsId: string;
  attachmentId: string;
  page: ReportFeedbackPage;
}) {
  const { t } = useT("editor");
  const { schema } = useReportFeedbackMutations(wsId, attachmentId);
  const [draft, setDraft] = useState<{
    fields: ReportField[];
    base: ReportField[];
    revision: number;
    hasRecords: boolean;
  } | null>(null);
  const [error, setError] = useState("");
  const patch = (id: string, update: Partial<ReportField>) =>
    setDraft(
      (d) =>
        d && {
          ...d,
          fields: d.fields.map((f) => (f.id === id ? { ...f, ...update } : f)),
        },
    );
  const types: Record<string, string> = {
    text: t(($) => $.report_feedback.type_text),
    number: t(($) => $.report_feedback.type_number),
    date: t(($) => $.report_feedback.type_date),
    boolean: t(($) => $.report_feedback.type_boolean),
    select: t(($) => $.report_feedback.type_select),
    multi_select: t(($) => $.report_feedback.type_multi_select),
  };
  return (
    <ReportFormDialog
      open={!!draft}
      onOpenChange={(open) => {
        setError("");
        schema.reset();
        setDraft(
          open
            ? {
                fields: structuredClone(page.fields),
                base: page.fields,
                revision: page.schemaRevision,
                hasRecords: page.hasRecords,
              }
            : null,
        );
      }}
      trigger={t(($) => $.report_feedback.configure)}
      title={t(($) => $.report_feedback.configure)}
      description={t(($) => $.report_feedback.fields_help)}
      dirty={
        !!draft && JSON.stringify(draft.fields) !== JSON.stringify(draft.base)
      }
      pending={schema.isPending}
      error={error}
      onSubmit={async (event) => {
        event.preventDefault();
        if (!draft || schema.isPending) return;
        if (!draft.fields.length) {
          setError(t(($) => $.report_feedback.fields_required));
          return;
        }
        try {
          await schema.mutateAsync({
            fields: draft.fields,
            revision: draft.revision,
          });
          setDraft(null);
        } catch (e) {
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
      {draft?.hasRecords && (
        <p className="text-body-sm text-muted-foreground">
          {t(($) => $.report_feedback.schema_locked)}
        </p>
      )}
      {draft?.fields.map((field, index) => {
        const previous = draft.base.find((f) => f.id === field.id);
        const locked = draft.hasRecords && !!previous;
        return (
          <fieldset
            key={field.id}
            className="space-y-3 rounded-md border border-border p-3"
          >
            <legend className="px-1 text-body-sm">
              {t(($) => $.report_feedback.field_number, { number: index + 1 })}
            </legend>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label className="space-y-1 text-body-sm">
                {t(($) => $.report_feedback.field_name)}
                <Input
                  name={`field-${field.id}-label`}
                  autoComplete="off"
                  required
                  maxLength={80}
                  value={field.label}
                  onChange={(e) => patch(field.id, { label: e.target.value })}
                />
              </label>
              <label className="space-y-1 text-body-sm">
                {t(($) => $.report_feedback.field_type)}
                <select
                  name={`field-${field.id}-type`}
                  className={selectClass}
                  value={field.type}
                  disabled={locked}
                  onChange={(e) =>
                    patch(field.id, { type: e.target.value, options: [] })
                  }
                >
                  {REPORT_FIELD_TYPES.map((type) => (
                    <option key={type} value={type}>
                      {types[type]}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            {(field.type === "select" || field.type === "multi_select") && (
              <label className="block space-y-1 text-body-sm">
                {t(($) => $.report_feedback.options)}
                <Textarea
                  name={`field-${field.id}-options`}
                  autoComplete="off"
                  required
                  value={field.options.join("\n")}
                  onChange={(e) =>
                    patch(field.id, { options: e.target.value.split("\n") })
                  }
                />
              </label>
            )}
            <div className="flex flex-wrap items-center justify-between gap-2">
              <label className="flex items-center gap-2 text-body-sm">
                <input
                  type="checkbox"
                  name={`field-${field.id}-required`}
                  checked={field.required}
                  disabled={draft.hasRecords && !previous?.required}
                  onChange={(e) =>
                    patch(field.id, { required: e.target.checked })
                  }
                />
                {t(($) => $.report_feedback.required)}
              </label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={locked}
                onClick={() =>
                  setDraft(
                    (d) =>
                      d && {
                        ...d,
                        fields: d.fields.filter((f) => f.id !== field.id),
                      },
                  )
                }
              >
                {t(($) => $.report_feedback.remove_field)}
              </Button>
            </div>
          </fieldset>
        );
      })}
      <div className="flex items-center justify-between gap-2">
        <Button
          type="button"
          variant="outline"
          disabled={(draft?.fields.length ?? 0) >= REPORT_FIELD_LIMIT}
          onClick={() =>
            setDraft(
              (d) =>
                d && {
                  ...d,
                  fields: [
                    ...d.fields,
                    {
                      id: `f_${crypto.randomUUID().replaceAll("-", "")}`,
                      label: "",
                      type: "text",
                      required: false,
                      options: [],
                    },
                  ],
                },
            )
          }
        >
          {t(($) => $.report_feedback.add_field)}
        </Button>
        <span className="text-caption text-muted-foreground">
          {draft?.fields.length ?? 0} / {REPORT_FIELD_LIMIT}
        </span>
      </div>
    </ReportFormDialog>
  );
}
