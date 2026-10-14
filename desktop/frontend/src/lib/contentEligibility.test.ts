import { describe, expect, it } from "vitest";
import { hasDownloadableTranscript, hasStoredAudio } from "./contentEligibility";
import type { Status, Transcription } from "./types";
import { compareLibraryItems } from "../features/library/helpers";

const row = (status: Status, createdAt = "2026-10-01T12:00:00.123456Z") => ({
  id: "a", status, createdAt, audio: { filename: "source.wav", sizeBytes: 123 },
} as Transcription);

describe("stored content eligibility", () => {
  it("retains accepted upload audio across every status without inventing transcripts", () => {
    for (const status of ["pending", "running", "failed", "canceled", "succeeded"] as const) {
      expect(hasStoredAudio(row(status))).toBe(true);
      expect(hasDownloadableTranscript(row(status))).toBe(false);
      const withResult = { ...row(status), result: { text: "done" } } as Transcription;
      expect(hasDownloadableTranscript(withResult)).toBe(status === "succeeded");
    }
    expect(hasStoredAudio({ ...row("running"), audio: { filename: "", sizeBytes: 123 } } as Transcription)).toBe(false);
    expect(hasStoredAudio({ ...row("running"), audio: { filename: "source.wav", sizeBytes: 0 } } as Transcription)).toBe(false);
  });
  it("preserves timestamp fractions, FIFO, completion fallback and id ties", () => {
    const first = row("running", "2026-10-01T12:00:00.123456Z");
    const later = { ...row("running", "2026-10-01T14:00:00.123457+02:00"), id: "b" };
    expect(compareLibraryItems(first, later)).toBeLessThan(0);
    expect(compareLibraryItems({ ...first, status: "pending" }, later)).toBeGreaterThan(0);
    expect(compareLibraryItems({ ...first, status: "succeeded" }, { ...later, status: "failed" })).toBeGreaterThan(0);
    expect(compareLibraryItems({ ...first, status: "succeeded", completedAt: "2026-10-02T00:00:00Z" }, { ...later, status: "failed" })).toBeLessThan(0);
    expect(compareLibraryItems(first, { ...first, id: "b" })).toBeLessThan(0);
  });
});
