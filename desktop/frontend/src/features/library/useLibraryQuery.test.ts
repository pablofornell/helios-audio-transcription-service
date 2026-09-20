import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ACTIVE_POLL_MS, IDLE_POLL_MS } from "../../lib";
import { useLibraryQuery } from "./useLibraryQuery";

const list = vi.hoisted(() => vi.fn());
vi.mock("../../lib", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib")>()),
  list: (...args: unknown[]) => list(...args),
}));

const row = (id: string, status = "succeeded") => ({
  id,
  status,
  options: { model: "base", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
  audio: { filename: `${id}.wav`, mediaType: "audio/wav", sizeBytes: 1, durationSeconds: 1, sampleRateHz: 16000, channels: 1, codec: "pcm_s16le" },
  attempts: 1,
  maxAttempts: 3,
  createdAt: "2026-09-20T20:00:00Z",
  updatedAt: "2026-09-20T20:00:00Z",
});

const page = (items: unknown[], nextCursor?: string) => ({ items, nextCursor });

// Testing Library's waitFor polls on REAL timers, so it deadlocks against
// vi.useFakeTimers(). Advancing the fake clock by zero flushes the pending
// promise microtasks instead, which is all these assertions need.
const flush = async (ms = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

beforeEach(() => {
  vi.useFakeTimers();
  list.mockReset();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("useLibraryQuery background refresh", () => {
  // The whole point: this service has no SSE stream, so without polling the
  // library is frozen at mount. A job finishing while the user looks at the
  // list must appear without them navigating away and back.
  it("picks up rows that appear after mount", async () => {
    list.mockResolvedValueOnce(page([row("a")]));
    const { result } = renderHook(() => useLibraryQuery());
    await flush();
    expect(result.current.items).toHaveLength(1);

    list.mockResolvedValueOnce(page([row("b"), row("a")]));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(IDLE_POLL_MS + 50);
    });

    expect(result.current.items.map((i) => i.id)).toEqual(["b", "a"]);
  });

  // A list with work in flight must refresh quickly, not on the idle beat.
  it("polls faster while a job is pending or running", async () => {
    list.mockResolvedValue(page([row("a", "running")]));
    const { result } = renderHook(() => useLibraryQuery());
    await flush();
    expect(result.current.items).toHaveLength(1);

    const callsAfterMount = list.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_POLL_MS + 50);
    });
    expect(list.mock.calls.length).toBeGreaterThan(callsAfterMount);
  });

  // Replacing items with page one would yank away everything the user
  // paged in, so the refresh stands down once they have paged.
  it("stops refreshing once the user has loaded a second page", async () => {
    list.mockResolvedValueOnce(page([row("a")], "cursor-1"));
    const { result } = renderHook(() => useLibraryQuery());
    await flush();
    expect(result.current.hasMore).toBe(true);

    list.mockResolvedValueOnce(page([row("b")]));
    await act(async () => {
      result.current.loadMore();
    });
    await flush();
    expect(result.current.items).toHaveLength(2);

    const callsAfterPaging = list.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(IDLE_POLL_MS * 2);
    });
    expect(list.mock.calls.length).toBe(callsAfterPaging);
    expect(result.current.items).toHaveLength(2);
  });

  // A background poll that fails must leave the readable list alone.
  it("keeps showing the list when a background poll fails", async () => {
    list.mockResolvedValueOnce(page([row("a")]));
    const { result } = renderHook(() => useLibraryQuery());
    await flush();
    expect(result.current.items).toHaveLength(1);

    list.mockRejectedValueOnce(new Error("server unreachable"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(IDLE_POLL_MS + 50);
    });

    expect(result.current.items).toHaveLength(1);
    expect(result.current.error).toBeUndefined();
  });
});
