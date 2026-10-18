"use client";

import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import {
  displayReportValue,
  isReportFieldEditable,
  reportFeedbackOptions,
  reportHistoryOptions,
  useReportFeedbackMutations,
  type ReportField,
  type ReportRow,
} from "@multica/core/report-feedback";
import { paths, useWorkspaceSlug } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@multica/ui/components/ui/alert-dialog";
import { AppLink } from "../navigation";
import { useT } from "../i18n";
import { useDownloadAttachment } from "../editor/use-download-attachment";
import { ReportSchemaEditor } from "./report-schema-editor";
import { ReportRowEditor } from "./report-row-editor";
import { isReportAccessDenied, isReportConflict } from "./report-form-dialog";

function ArchiveRecord({
  wsId,
  attachmentId,
  row,
}: {
  wsId: string;
  attachmentId: string;
  row: ReportRow;
}) {
  const { t } = useT("editor");
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const mutation = useReportFeedbackMutations(wsId, attachmentId).archive;
  // Keep the confirmed version stable while collaborators continue editing.
  const [revision, setRevision] = useState(row.revision);
  return (
    <AlertDialog
      open={open}
      onOpenChange={(value) => {
        if (!mutation.isPending) {
          setOpen(value);
          setError("");
          if (value) setRevision(row.revision);
        }
      }}
    >
      <AlertDialogTrigger render={<Button variant="ghost" size="sm" />}>
        {t(($) => $.report_feedback.archive)}
      </AlertDialogTrigger>
      <AlertDialogContent data-report-feedback-dialog="true">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t(($) => $.report_feedback.archive_title, { name: row.label })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.report_feedback.archive_help)}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error && (
          <p role="alert" className="text-body-sm text-destructive">
            {error}
          </p>
        )}
        <AlertDialogFooter>
          <Button
            variant="outline"
            disabled={mutation.isPending}
            onClick={() => setOpen(false)}
          >
            {t(($) => $.report_feedback.cancel)}
          </Button>
          <Button
            disabled={mutation.isPending}
            aria-busy={mutation.isPending}
            onClick={async () => {
              try {
                await mutation.mutateAsync({ id: row.id, revision });
                setOpen(false);
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
            {t(($) => $.report_feedback.archive)}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function RecordSources({ id }: { id: string }) {
  const { t } = useT("editor");
  const slug = useWorkspaceSlug();
  const download = useDownloadAttachment();
  if (!id || !slug) return null;
  return (
    <span className="flex flex-wrap items-center gap-3">
      <AppLink
        href={paths.workspace(slug).attachmentPreview(id)}
        target="_blank"
        rel="noopener noreferrer"
        className="text-link underline underline-offset-2"
      >
        {t(($) => $.attachment.preview)}
      </AppLink>
      <Button variant="link" size="sm" onClick={() => void download(id)}>
        {t(($) => $.image.download)}
      </Button>
    </span>
  );
}

function Snapshot({
  value,
  fields,
}: {
  value: unknown;
  fields: ReportField[];
}) {
  const { t } = useT("editor");
  if (!value || typeof value !== "object") return <span>—</span>;
  const data = value as Record<string, unknown>;
  const values = data.values;
  if (!values || typeof values !== "object" || Array.isArray(values))
    return (
      <pre className="whitespace-pre-wrap break-all text-caption">
        {JSON.stringify(value, null, 2)}
      </pre>
    );
  return (
    <dl className="space-y-1 text-caption">
      <div>
        <dt className="font-medium">
          {t(($) => $.report_feedback.record_name)}
        </dt>
        <dd>{displayReportValue(data.label)}</dd>
      </div>
      {Object.entries(values).map(([id, v]) => (
        <div key={id}>
          <dt className="font-medium">
            {fields.find((f) => f.id === id)?.label ?? id}
          </dt>
          <dd className="whitespace-pre-wrap break-words">
            {typeof v === "boolean"
              ? t(($) => (v ? $.report_feedback.yes : $.report_feedback.no))
              : displayReportValue(v)}
          </dd>
        </div>
      ))}
      <div>
        <dt className="font-medium">{t(($) => $.report_feedback.source_id)}</dt>
        <dd className="break-all">
          {displayReportValue(data.source_attachment_id)}
        </dd>
      </div>
    </dl>
  );
}

function ReportHistory({
  wsId,
  attachmentId,
  fields,
}: {
  wsId: string;
  attachmentId: string;
  fields: ReportField[];
}) {
  const { t } = useT("editor");
  const query = useInfiniteQuery(reportHistoryOptions(wsId, attachmentId));
  if (query.isError)
    return <p role="alert">{t(($) => $.report_feedback.load_failed)}</p>;
  if (!query.data)
    return <p role="status">{t(($) => $.report_feedback.loading)}</p>;
  const changes = query.data.pages.flatMap((p) => p.changes);
  const kinds: Record<string, string> = {
    schema: t(($) => $.report_feedback.change_schema),
    create: t(($) => $.report_feedback.change_create),
    update: t(($) => $.report_feedback.change_update),
    archive: t(($) => $.report_feedback.change_archive),
  };
  return (
    <div className="space-y-3">
      {!changes.length && <p>{t(($) => $.report_feedback.empty_history)}</p>}
      {changes.map((change) => (
        <details
          key={change.id}
          className="rounded-md border border-border p-3"
        >
          <summary className="cursor-pointer break-words text-body-sm">
            {change.actorName} · {kinds[change.kind] ?? change.kind}
            <br />
            <time
              dateTime={change.createdAt}
              className="text-caption text-muted-foreground"
            >
              {new Date(change.createdAt).toLocaleString()}
            </time>
          </summary>
          <div className="mt-3 grid grid-cols-1 gap-4 xl:grid-cols-2">
            <section>
              <h4 className="mb-2 text-body-sm font-medium">
                {t(($) => $.report_feedback.before)}
              </h4>
              <Snapshot value={change.before} fields={fields} />
            </section>
            <section>
              <h4 className="mb-2 text-body-sm font-medium">
                {t(($) => $.report_feedback.after)}
              </h4>
              <Snapshot value={change.after} fields={fields} />
            </section>
          </div>
        </details>
      ))}
      {query.hasNextPage && (
        <Button
          variant="outline"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t(($) => $.report_feedback.load_more)}
        </Button>
      )}
    </div>
  );
}

/** Trusted native controls beside an untrusted report, shared by web/desktop. */
export function ReportFeedbackPanel({
  wsId,
  attachmentId,
}: {
  wsId: string;
  attachmentId: string;
}) {
  const { t } = useT("editor");
  const query = useInfiniteQuery(reportFeedbackOptions(wsId, attachmentId));
  const [history, setHistory] = useState(false);
  // Retain only the row backing an open local draft if a collaborator archives
  // it. The server rejects its stale revision; polling must not discard input.
  const [editingRow, setEditingRow] = useState<ReportRow | null>(null);
  // A revoked/refused refresh must never leave private cached rows or drafts visible.
  if (query.isError && (!query.data || isReportAccessDenied(query.error)))
    return (
      <section className="p-4" aria-label={t(($) => $.report_feedback.title)}>
        <p role="alert">{t(($) => $.report_feedback.load_failed)}</p>
        <Button variant="outline" onClick={() => void query.refetch()}>
          {t(($) => $.attachment.preview_retry)}
        </Button>
      </section>
    );
  if (!query.data)
    return (
      <p role="status" className="p-4">
        {t(($) => $.report_feedback.loading)}
      </p>
    );
  const page = query.data.pages[0]!;
  const rows = query.data.pages.flatMap((p) => p.rows);
  if (editingRow && !rows.some((row) => row.id === editingRow.id))
    rows.push(editingRow);
  const editable = page.fields.every(isReportFieldEditable);
  return (
    <section
      className="space-y-4 p-4 text-body"
      aria-label={t(($) => $.report_feedback.title)}
    >
      {query.isError && (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-destructive">
          <p>{t(($) => $.report_feedback.refresh_failed)}</p>
          <Button variant="outline" disabled={query.isFetching} aria-busy={query.isFetching} onClick={() => void query.refetch()}>
            {t(($) => $.attachment.preview_retry)}
          </Button>
        </div>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-heading-sm font-semibold">
          {t(($) => $.report_feedback.title)}
        </h2>
        <Button
          variant={history ? "secondary" : "outline"}
          size="sm"
          aria-pressed={history}
          onClick={() => setHistory((v) => !v)}
        >
          {t(($) => $.report_feedback.history)}
        </Button>
      </div>
      <p className="text-caption text-muted-foreground">
        {t(($) => $.report_feedback.collaboration_help)}
      </p>
      {history ? (
        <ReportHistory
          wsId={wsId}
          attachmentId={attachmentId}
          fields={page.fields}
        />
      ) : (
        <>
          {!editable && (
            <p role="status">{t(($) => $.report_feedback.unsupported)}</p>
          )}
          <div className="flex flex-wrap gap-2">
            {page.canManage && editable && (
              <ReportSchemaEditor
                wsId={wsId}
                attachmentId={attachmentId}
                page={page}
              />
            )}
            {page.canCreate && editable && page.fields.length > 0 && (
              <ReportRowEditor
                wsId={wsId}
                attachmentId={attachmentId}
                page={page}
              />
            )}
          </div>
          {!page.fields.length ? (
            <p className="text-muted-foreground">
              {t(($) => $.report_feedback.not_configured)}
            </p>
          ) : !rows.length ? (
            <p className="text-muted-foreground">
              {t(($) => $.report_feedback.empty_records)}
            </p>
          ) : (
            <div
              className="max-w-full overflow-x-auto rounded-md border border-border"
              tabIndex={0}
              role="region"
              aria-label={t(($) => $.report_feedback.records)}
            >
              <table className="w-full text-body-sm">
                <caption className="sr-only">
                  {t(($) => $.report_feedback.records)}
                </caption>
                <thead className="bg-muted">
                  <tr>
                    <th scope="col" className="sticky left-0 z-10 min-w-56 bg-muted p-3 text-left">
                      {t(($) => $.report_feedback.record_name)}
                    </th>
                    {page.fields.map((f) => (
                      <th
                        key={f.id}
                        scope="col"
                        className="min-w-32 max-w-72 p-3 text-left break-words"
                      >
                        {f.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row) => (
                    <tr key={row.id} className="border-t border-border">
                      <th
                        scope="row"
                        className="sticky left-0 z-10 min-w-56 max-w-72 bg-background p-3 text-left align-top font-medium break-words"
                      >
                        {row.label}
                        <div className="mt-2 flex flex-wrap gap-2">
                          {page.canEdit && editable && <ReportRowEditor wsId={wsId} attachmentId={attachmentId} page={page} row={row} onEditingRowChange={setEditingRow} />}
                          {page.canManage && <ArchiveRecord wsId={wsId} attachmentId={attachmentId} row={row} />}
                        </div>
                        <RecordSources id={row.sourceAttachmentId} />
                      </th>
                      {page.fields.map((f) => (
                        <td
                          key={f.id}
                          className="max-w-80 p-3 align-top whitespace-pre-wrap break-words"
                        >
                          {typeof row.values[f.id] === "boolean"
                            ? t(($) =>
                                row.values[f.id]
                                  ? $.report_feedback.yes
                                  : $.report_feedback.no,
                              )
                            : displayReportValue(row.values[f.id])}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {query.hasNextPage && (
            <Button
              variant="outline"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {t(($) => $.report_feedback.load_more)}
            </Button>
          )}
        </>
      )}
    </section>
  );
}
