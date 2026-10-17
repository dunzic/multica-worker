import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { createLogger } from "../logger";
import { MAX_CONCURRENT_UPLOADS } from "../constants/upload";
import { getCurrentSlug } from "../platform/workspace-storage";

/**
 * Module-level file-upload coordinator (MUL-5181, L2).
 *
 * Ownership inversion: an upload is owned here, NOT by the React component that
 * started it. The composer only records a persisted placeholder (a
 * `DraftUpload`) in its draft and hands the file to `startUpload`; the request
 * then outlives the composer's mount. Closing the editor/modal no longer aborts
 * the upload — its result lands in the persisted draft through `onSettled`, so
 * reopening the composer shows the attachment.
 *
 * The coordinator itself knows nothing about drafts. `onSettled` fires with the
 * outcome and the CALLER is responsible for the generation guard: it must check
 * that its draft still tracks `clientUploadId` before writing (the draft may
 * have been cleared/submitted, or the placeholder removed, while the request
 * was in flight).
 *
 * Abort (`abortAll`) is the one thing a plain fire-and-forget promise cannot do:
 * on logout, every tracked request is cancelled so the previous user's bytes
 * never bind to an attachment under the next session.
 */

const logger = createLogger("drafts.upload-coordinator");

export interface UploadCoordinatorContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
  topicId?: string;
}

export type UploadOutcome =
  | { clientUploadId: string; status: "uploaded"; attachment: Attachment }
  | { clientUploadId: string; status: "failed"; error: Error };

export interface StartUploadArgs {
  /** Client-minted id tying this request to its persisted `DraftUpload`. */
  clientUploadId: string;
  file: File;
  /** Injected so the coordinator is framework-agnostic and unit-testable. */
  api: Pick<ApiClient, "uploadFile">;
  ctx?: UploadCoordinatorContext;
  /**
   * Settled outcome. NOT called on abort — an aborted upload leaves its
   * placeholder in `uploading`, which the store drops on the next load (aborts
   * happen on logout, where the placeholder is cleared anyway). The caller MUST
   * re-check its draft still tracks `clientUploadId` before writing the result.
   */
  onSettled: (outcome: UploadOutcome) => void;
}

interface UploadJob {
  args: StartUploadArgs;
  controller: AbortController;
  workspaceSlug: string | null;
}

// Map insertion order is the FIFO queue; active requests keep their slot until
// they actually settle, including after cancellation. This bounds transport
// concurrency across composers in this client, not just within one file pick.
const jobs = new Map<string, UploadJob>();
const active = new Set<UploadJob>();

function pumpUploads(): void {
  for (const job of jobs.values()) {
    if (active.size >= MAX_CONCURRENT_UPLOADS) break;
    if (active.has(job)) continue;
    active.add(job);
    void runUpload(job);
  }
}

async function runUpload(job: UploadJob): Promise<void> {
  const { clientUploadId, file, api, ctx, onSettled } = job.args;
  const { controller } = job;
  let outcome: UploadOutcome | undefined;
  try {
    const attachment = await api.uploadFile(
      file,
      {
        issueId: ctx?.issueId,
        commentId: ctx?.commentId,
        chatSessionId: ctx?.chatSessionId,
        topicId: ctx?.topicId,
        workspaceSlug: job.workspaceSlug,
      },
      controller.signal,
    );
    outcome = { clientUploadId, status: "uploaded", attachment };
  } catch (err) {
    if (!(err instanceof Error && err.name === "AbortError")) {
      outcome = {
        clientUploadId,
        status: "failed",
        error: err instanceof Error ? err : new Error("Upload failed"),
      };
    }
  } finally {
    const stillOwned = jobs.get(clientUploadId) === job;
    if (stillOwned) jobs.delete(clientUploadId);
    active.delete(job);
    // Logout/removal may race an API implementation that ignores abort. Such
    // a completion must never write into the next session's drafts.
    try {
      if (stillOwned && !controller.signal.aborted && outcome)
        onSettled(outcome);
    } catch (err) {
      logger.error("upload result callback failed", {
        clientUploadId,
        error: String(err),
      });
    } finally {
      pumpUploads();
    }
  }
}

/**
 * Queue an upload owned by this module. Returns immediately; the outcome is
 * delivered through `onSettled`. A duplicate queued/active ID is a no-op.
 */
export function startUpload(args: StartUploadArgs): void {
  if (jobs.has(args.clientUploadId)) return;
  jobs.set(args.clientUploadId, {
    args,
    controller: new AbortController(),
    workspaceSlug: getCurrentSlug(),
  });
  pumpUploads();
}

/** Abort a single tracked upload, if present. */
export function abortUpload(clientUploadId: string): void {
  const job = jobs.get(clientUploadId);
  if (!job) return;
  jobs.delete(clientUploadId);
  job.controller.abort();
}

/**
 * Abort every tracked upload. Called on logout (before drafts are cleared) so
 * no in-flight upload can bind an attachment under the next session.
 */
export function abortAll(): void {
  if (jobs.size === 0) return;
  logger.info("aborting all uploads", { count: jobs.size });
  const cancelled = [...jobs.values()];
  jobs.clear();
  for (const job of cancelled) job.controller.abort();
}

/** Test-only: number of uploads currently tracked. */
export function __trackedUploadCountForTest(): number {
  return jobs.size;
}
