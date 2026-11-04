import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ListFilter, ListPage, Transcription } from "./types";

/*
 * `transcriptionStore` is a module-level singleton (see `./store.ts`'s own
 * doc), so each test resets modules and dynamically re-imports `./store`
 * to get a fresh instance, rather than letting state leak between tests.
 * `./api` is mocked directly (not through `../../lib`'s barrel) since this
 * suite exercises `./store.ts` itself.
 *
 * This is the one behavioural rule DESKTOP.md's binding contract states
 * outright for this store — poll every 1.5s while any known transcription
 * is pending/running, every 10s otherwise — so it gets its own direct
 * test rather than relying on a feature screen to exercise it
 * incidentally.
 */

let listImpl: (filter: ListFilter) => Promise<ListPage>;
let getImpl: (id: string) => Promise<Transcription> = () => Promise.reject(new Error("get is not expected"));

vi.mock("./api", () => ({
  list: (filter: ListFilter) => listImpl(filter),
  get: (id: string) => getImpl(id),
}));

function makeTranscription(overrides: Partial<Transcription> = {}): Transcription {
  return {
    id: "t-1",
    status: "pending",
    options: { model: "", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
    audio: { filename: "clip.mp3", mediaType: "audio/mpeg", sizeBytes: 1000, durationSeconds: 0, sampleRateHz: 0, channels: 0, codec: "" },
    attempts: 0,
    maxAttempts: 3,
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  vi.resetModules();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("transcriptionStore — poll cadence", () => {
  it("polls every 1.5s while a known transcription is pending or running", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      return Promise.resolve({ items: [makeTranscription({ status: "running" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1));

    await vi.advanceTimersByTimeAsync(1_500);
    expect(calls).toBe(2);
    await vi.advanceTimersByTimeAsync(1_500);
    expect(calls).toBe(3);

    unsubscribe();
  });

  it("polls every 10s once nothing is pending or running", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      return Promise.resolve({ items: [makeTranscription({ status: "succeeded" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1));

    // The fast cadence must not fire once everything is terminal.
    await vi.advanceTimersByTimeAsync(1_500);
    expect(calls).toBe(1);

    await vi.advanceTimersByTimeAsync(8_500); // total 10s
    expect(calls).toBe(2);

    unsubscribe();
  });

  it("speeds back up the moment a poll observes a fresh pending/running row", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      const status = calls === 1 ? "succeeded" : "pending";
      return Promise.resolve({ items: [makeTranscription({ status })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1)); // idle cadence scheduled

    await vi.advanceTimersByTimeAsync(1_500);
    expect(calls).toBe(1); // still on the 10s idle timer

    await vi.advanceTimersByTimeAsync(8_500); // total 10s: second poll lands, sees "pending"
    expect(calls).toBe(2);

    await vi.advanceTimersByTimeAsync(1_500); // now on the fast cadence
    expect(calls).toBe(3);

    unsubscribe();
  });

  it("stops polling once the last subscriber unsubscribes", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      return Promise.resolve({ items: [makeTranscription({ status: "running" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1));
    unsubscribe();

    await vi.advanceTimersByTimeAsync(30_000);
    expect(calls).toBe(1);
  });

  it("refreshNow() cancels the pending timer and polls immediately", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      return Promise.resolve({ items: [makeTranscription({ status: "succeeded" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1));

    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(calls).toBe(2));

    unsubscribe();
  });

  it("resetForServerChange() polls immediately, against whatever server list() now targets", async () => {
    let calls = 0;
    listImpl = () => {
      calls += 1;
      return Promise.resolve({ items: [makeTranscription({ status: "succeeded" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(calls).toBe(1));

    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(calls).toBe(2));

    unsubscribe();
  });

  it("resetForServerChange() discards an old-server list() that was still in flight when it fired, instead of letting it overwrite the new server's rows", async () => {
    // Simulates Settings -> Server saving mid-poll: the first list() call
    // (against the old server) is left deliberately unresolved until
    // after resetForServerChange() has already kicked off, and resolved,
    // a second one (against the "new" server) — see this test's own
    // assertions for which one's row must end up in the cache.
    let resolveOldServer: (page: ListPage) => void = () => {};
    let call = 0;
    listImpl = () => {
      call += 1;
      if (call === 1) {
        return new Promise<ListPage>((resolve) => {
          resolveOldServer = resolve;
        });
      }
      return Promise.resolve({ items: [makeTranscription({ id: "new-server-row", status: "succeeded" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(call).toBe(1)); // the old server's list() is now in flight, unresolved

    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(call).toBe(2));
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(1));
    expect(transcriptionStore.getSnapshot().transcriptions[0].id).toBe("new-server-row");

    // The old server's call finally resolves — its stale result must not
    // clobber the new server's row that already landed above.
    resolveOldServer({ items: [makeTranscription({ id: "old-server-row", status: "succeeded" })] });
    await Promise.resolve();
    await Promise.resolve();
    expect(transcriptionStore.getSnapshot().transcriptions.map((t) => t.id)).toEqual(["new-server-row"]);

    unsubscribe();
  });

  it("does not push the next poll back out when a stale generation's list() resolves late", async () => {
    // Covers the `if (gen === this.generation) this.scheduleNext()` guard
    // in refresh()'s `finally` specifically: without it, the old server's
    // list() resolving after resetForServerChange() would call
    // scheduleNext() a second time and reset the timer the new server's
    // own refresh() already scheduled, delaying the next poll instead of
    // leaving it alone.
    let resolveOldServer: (page: ListPage) => void = () => {};
    let call = 0;
    listImpl = () => {
      call += 1;
      if (call === 1) {
        return new Promise<ListPage>((resolve) => {
          resolveOldServer = resolve;
        });
      }
      return Promise.resolve({ items: [makeTranscription({ status: "succeeded" })] });
    };

    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(call).toBe(1)); // the old server's list() is now in flight, unresolved

    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(call).toBe(2)); // the new server's refresh() resolved and scheduled its own next poll

    // Let all but the last 100ms of the idle interval (10s) pass, then let
    // the stale call resolve.
    await vi.advanceTimersByTimeAsync(9_900);
    resolveOldServer({ items: [makeTranscription({ id: "old-server-row", status: "succeeded" })] });
    await Promise.resolve();
    await Promise.resolve();

    // Only the 100ms left on the original schedule should be needed -- if
    // the stale call's finally had reset the timer, this would not fire yet.
    await vi.advanceTimersByTimeAsync(100);
    expect(call).toBe(3);

    unsubscribe();
  });
});

/** A fake server: rows are newest first, and a cursor is the offset of the next row. */
function fakeServer(rows: Transcription[]) {
  const requests: ListFilter[] = [];
  listImpl = (filter) => {
    requests.push(filter);
    const matching = rows.filter((t) => !filter.status || t.status === filter.status);
    const start = filter.cursor ? Number(filter.cursor) : 0;
    const limit = Math.min(filter.limit ?? 20, 100);
    const items = matching.slice(start, start + limit);
    return Promise.resolve({ items, nextCursor: start + limit < matching.length ? String(start + limit) : undefined });
  };
  return requests;
}

/** `count` rows, newest first. `id` is `t-0` for the newest. */
function manyRows(count: number, status: (index: number) => Transcription["status"]): Transcription[] {
  return Array.from({ length: count }, (_, i) =>
    makeTranscription({ id: `t-${i}`, status: status(i), createdAt: new Date(Date.UTC(2024, 0, 1) - i * 1000).toISOString() }),
  );
}

describe("transcriptionStore — reach beyond one page", () => {
  it("costs one request per poll when one page holds every row", async () => {
    const requests = fakeServer(manyRows(100, () => "succeeded"));
    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(100));
    expect(requests).toEqual([{ limit: 100 }]);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    unsubscribe();
  });

  it("loads every active job when more than 100 rows exist, then older rows on request", async () => {
    // 250 rows. The 3 oldest are active, so the newest page cannot hold them.
    const rows = manyRows(250, (i) => (i === 249 ? "running" : i >= 247 ? "pending" : i === 150 ? "failed" : "succeeded"));
    const requests = fakeServer(rows);
    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().loading).toBe(false));

    let snapshot = transcriptionStore.getSnapshot();
    expect(requests.every((r) => (r.limit ?? 0) <= 100)).toBe(true);
    expect(snapshot.transcriptions).toHaveLength(103);
    const ids = new Set(snapshot.transcriptions.map((t) => t.id));
    for (const id of ["t-249", "t-248", "t-247"]) expect(ids.has(id)).toBe(true);
    expect(snapshot.hasMore).toBe(true);

    // The failed row is older than the newest page: it needs "load more".
    expect(ids.has("t-150")).toBe(false);
    await transcriptionStore.loadMore();
    snapshot = transcriptionStore.getSnapshot();
    expect(snapshot.transcriptions.map((t) => t.id)).toContain("t-150");
    expect(snapshot.transcriptions).toHaveLength(203);
    expect(snapshot.hasMore).toBe(true);
    await transcriptionStore.loadMore();
    snapshot = transcriptionStore.getSnapshot();
    expect(snapshot.transcriptions).toHaveLength(250);
    expect(snapshot.hasMore).toBe(false);
    unsubscribe();
  });

  it("keeps loaded older rows on the next poll, even when a new row pushes one off the newest page", async () => {
    const rows = manyRows(150, () => "succeeded");
    fakeServer(rows);
    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().loading).toBe(false));
    await transcriptionStore.loadMore();
    expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(150);

    // A new job arrives. The newest page now ends at t-98.
    rows.unshift(makeTranscription({ id: "new", status: "pending", createdAt: new Date(Date.UTC(2024, 0, 1) + 1000).toISOString() }));
    fakeServer(rows);
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(151));
    const ids = transcriptionStore.getSnapshot().transcriptions.map((t) => t.id);
    expect(ids).toContain("t-99");
    expect(ids).toContain("t-149");
    // A newer row that the server no longer returns was deleted.
    rows.splice(rows.findIndex((t) => t.id === "t-5"), 1);
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(150));
    expect(transcriptionStore.getSnapshot().transcriptions.map((t) => t.id)).not.toContain("t-5");
    unsubscribe();
  });

  it("reads again an older cached row that was active, and drops it when the server deleted it", async () => {
    const rows = manyRows(150, (i) => (i === 140 || i === 141 ? "running" : "succeeded"));
    fakeServer(rows);
    const { transcriptionStore } = await import("./store");
    // The store checks `instanceof`, so the error class must come from the same module graph.
    const { HeliosError } = await import("./errors");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().loading).toBe(false));
    await transcriptionStore.loadMore();
    const status = (id: string) => transcriptionStore.getSnapshot().transcriptions.find((t) => t.id === id)?.status;
    expect(status("t-140")).toBe("running");

    // t-140 failed. The server deleted t-141. Both are older than the newest page.
    const failed = { ...rows[140], status: "failed" as const };
    rows.splice(140, 2, failed);
    fakeServer(rows);
    getImpl = (id) => (id === "t-140" ? Promise.resolve(failed) : Promise.reject(new HeliosError("not_found", "gone")));
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(status("t-140")).toBe("failed"));
    expect(status("t-141")).toBeUndefined();
    unsubscribe();
  });

  it("forget() removes a row at once", async () => {
    fakeServer(manyRows(3, () => "failed"));
    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions).toHaveLength(3));
    transcriptionStore.forget("t-1");
    expect(transcriptionStore.getSnapshot().transcriptions.map((t) => t.id)).toEqual(["t-0", "t-2"]);
    unsubscribe();
  });

  it("forgets rows and cursors of the old server on a server change", async () => {
    fakeServer(manyRows(150, () => "succeeded"));
    const { transcriptionStore } = await import("./store");
    const unsubscribe = transcriptionStore.subscribe(() => {});
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().loading).toBe(false));
    await transcriptionStore.loadMore();
    fakeServer([makeTranscription({ id: "other", status: "succeeded" })]);
    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(transcriptionStore.getSnapshot().transcriptions.map((t) => t.id)).toEqual(["other"]));
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    unsubscribe();
  });
});
