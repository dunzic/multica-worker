// @vitest-environment node
import { describe, expect, it } from "vitest";
import { CHAT_ATTACHMENT_LIMITS } from "../constants/upload";
import { getUploadLimitError } from "./upload-limits";

describe("chat attachment capacity", () => {
  it("accepts 100 files and exactly 500 MiB", () => {
    expect(
      getUploadLimitError(
        Array(100).fill(5 * 1024 * 1024),
        CHAT_ATTACHMENT_LIMITS,
      ),
    ).toBeNull();
  });
  it("rejects 101 files even when empty", () => {
    expect(
      getUploadLimitError(Array(101).fill(0), CHAT_ATTACHMENT_LIMITS),
    ).toBe("count");
  });
  it.each([500 * 1024 * 1024 + 1, -1, NaN, Infinity])(
    "rejects oversized or invalid size %s",
    (size) => {
      expect(getUploadLimitError([size], CHAT_ATTACHMENT_LIMITS)).toBe("size");
    },
  );
  it("sums the whole batch rather than checking each file in isolation", () => {
    expect(
      getUploadLimitError(
        Array(6).fill(100 * 1024 * 1024),
        CHAT_ATTACHMENT_LIMITS,
      ),
    ).toBe("size");
  });
});
