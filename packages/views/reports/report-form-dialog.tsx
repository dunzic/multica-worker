"use client";

import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../i18n";

/** Never silently dismiss a local draft or a request whose outcome is pending. */
export function ReportFormDialog({
  open,
  onOpenChange,
  trigger,
  title,
  description,
  dirty,
  pending,
  submitDisabled,
  error,
  onSubmit,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: ReactNode;
  title: string;
  description: string;
  dirty: boolean;
  pending: boolean;
  error?: string;
  submitDisabled?: boolean;
  onSubmit: (event: FormEvent) => void;
  children: ReactNode;
}) {
  const { t } = useT("editor");
  const [discard, setDiscard] = useState(false);
  useEffect(() => {
    if (!open || (!dirty && !pending)) return;
    const guard = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [open, dirty, pending]);
  const requestOpen = (value: boolean) => {
    if (!value && pending) return;
    if (!value && dirty) {
      setDiscard(true);
      return;
    }
    onOpenChange(value);
  };
  return (
    <Dialog open={open} onOpenChange={requestOpen}>
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        {trigger}
      </DialogTrigger>
      <DialogContent
        className="overscroll-contain sm:max-w-2xl"
        showCloseButton={false}
        data-report-feedback-dialog="true"
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="min-w-0 space-y-4">
          <fieldset disabled={pending} className="min-w-0 space-y-4">
            {children}
          </fieldset>
          {error && (
            <p role="alert" className="text-body-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter className="sticky bottom-0 bg-surface-raised py-2">
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => requestOpen(false)}
            >
              {t(($) => $.report_feedback.cancel)}
            </Button>
            <Button
              type="submit"
              disabled={pending || submitDisabled}
              aria-busy={pending}
            >
              {t(($) =>
                pending ? $.report_feedback.saving : $.report_feedback.save,
              )}
            </Button>
          </DialogFooter>
        </form>
        <AlertDialog open={discard} onOpenChange={setDiscard}>
          <AlertDialogContent data-report-feedback-dialog="true">
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t(($) => $.report_feedback.discard_title)}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.report_feedback.discard_description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <Button variant="outline" onClick={() => setDiscard(false)}>
                {t(($) => $.report_feedback.keep_editing)}
              </Button>
              <Button
                variant="destructive"
                onClick={() => {
                  setDiscard(false);
                  onOpenChange(false);
                }}
              >
                {t(($) => $.report_feedback.discard)}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  );
}

export function isReportConflict(error: unknown): boolean {
  return error instanceof Error && "status" in error && error.status === 409;
}

export function isReportAccessDenied(error: unknown): boolean {
  return (
    error instanceof Error &&
    "status" in error &&
    [401, 403, 404].includes(Number(error.status))
  );
}
