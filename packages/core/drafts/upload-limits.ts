export interface UploadBatchLimits {
  maxFiles: number;
  maxBytes: number;
}

/** Checks metadata only; never reads file bytes or trusts a client for server authorization. */
export function getUploadLimitError(
  sizes: readonly number[],
  limits: UploadBatchLimits,
): "count" | "size" | null {
  if (sizes.length > limits.maxFiles) return "count";
  let total = 0;
  for (const size of sizes) {
    if (!Number.isFinite(size) || size < 0 || size > limits.maxBytes - total)
      return "size";
    total += size;
  }
  return null;
}
