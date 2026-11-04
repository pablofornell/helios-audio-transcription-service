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

vi.mock("./api", () => ({
  list: (filter: ListFilter) => listImpl(filter),
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

/**
 * A fake server like the real one: rows are newest first, and a cursor holds
 * the position (`createdAt` and id) of the last row served. A cursor still
 * works after that row is deleted. Tests change `server.rows` at any time.
 */
function fakeServer(initial: Transcription[]) {
  const server = {
    rows: initial,
    requests: [] as ListFilter[],
    gate: undefined as Promise<void> | undefined,
  };
  listImpl = async (filter) => {
    server.requests.push(filter);
    if (server.gate) await server.gate;
    const matching = server.rows.filter((t) => !filter.status || t.status === filter.status);
    let start = 0;
    if (filter.cursor) {
      const [createdAt, id] = filter.cursor.split("|");
      start = matching.findIndex((t) => t.createdAt < createdAt || (t.createdAt === createdAt && t.id < id));
      if (start < 0) start = matching.length;
    }
    const limit = Math.min(filter.limit ?? 20, 100);
    const items = matching.slice(start, start + limit);
    const last = items.at(-1);
    const more = start + limit < matching.length;
    return { items, nextCursor: more && last ? `${last.createdAt}|${last.id}` : undefined };
  };
  return server;
}

/** `count` rows, newest first. `id` is `t-0` for the newest. */
function manyRows(count: number, status: (index: number) => Transcription["status"]): Transcription[] {
  return Array.from({ length: count }, (_, i) =>
    makeTranscription({ id: `t-${i}`, status: status(i), createdAt: new Date(Date.UTC(2024, 0, 1) - i * 1000).toISOString() }),
  );
}

const newest = (i: number) => new Date(Date.UTC(2024, 0, 1) + i * 1000).toISOString();

async function started() {
  const { transcriptionStore } = await import("./store");
  const unsubscribe = transcriptionStore.subscribe(() => {});
  await vi.waitFor(() => expect(transcriptionStore.getSnapshot().loading).toBe(false));
  const ids = () => transcriptionStore.getSnapshot().transcriptions.map((t) => t.id);
  const status = (id: string) => transcriptionStore.getSnapshot().transcriptions.find((t) => t.id === id)?.status;
  return { transcriptionStore, unsubscribe, ids, status };
}

describe("transcriptionStore — reach beyond one page", () => {
  it("costs one request per poll when one page holds every row", async () => {
    const server = fakeServer(manyRows(100, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    expect(ids()).toHaveLength(100);
    expect(server.requests).toEqual([{ limit: 100 }]);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    unsubscribe();
  });

  it("loads every active job when more than 100 rows exist, then older rows on request", async () => {
    // 250 rows. The 3 oldest are active, so the newest page cannot hold them.
    const server = fakeServer(manyRows(250, (i) => (i === 249 ? "running" : i >= 247 ? "pending" : i === 150 ? "failed" : "succeeded")));
    const { transcriptionStore, unsubscribe, ids } = await started();
    expect(server.requests.every((r) => (r.limit ?? 0) <= 100)).toBe(true);
    expect(ids()).toHaveLength(103);
    for (const id of ["t-249", "t-248", "t-247"]) expect(ids()).toContain(id);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(true);

    // The failed row is older than the newest page: it needs "load more".
    expect(ids()).not.toContain("t-150");
    await transcriptionStore.loadMore();
    expect(ids()).toContain("t-150");
    expect(ids()).toHaveLength(203);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(true);
    await transcriptionStore.loadMore();
    expect(ids()).toHaveLength(250);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    unsubscribe();
  });

  it("loads a pending priority row that is older than the newest page, with its priorityAt", async () => {
    const rows = manyRows(250, () => "succeeded");
    rows[240] = { ...rows[240], status: "pending", priorityAt: "2024-01-02T00:00:00Z" };
    fakeServer(rows);
    const { transcriptionStore, unsubscribe, ids } = await started();
    expect(ids()).toContain("t-240");
    const row = transcriptionStore.getSnapshot().transcriptions.find((t) => t.id === "t-240");
    expect(row?.priorityAt).toBe("2024-01-02T00:00:00Z");
    expect(transcriptionStore.getSnapshot().hasMore).toBe(true);
    unsubscribe();
  });

  it("walks every loaded page on each poll, so a new row pushes the oldest loaded row off the cache", async () => {
    const server = fakeServer(manyRows(150, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    await transcriptionStore.loadMore();
    expect(ids()).toHaveLength(150);

    server.rows = [makeTranscription({ id: "new", status: "pending", createdAt: newest(1) }), ...server.rows];
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(ids()).toContain("new"));
    // Two pages are loaded, so the cache holds 200 rows or every row: here 151.
    expect(ids()).toHaveLength(151);
    expect(ids()).toContain("t-149");
    unsubscribe();
  });

  it("drops a row that falls off the walked pages, and loads it again with load more", async () => {
    const server = fakeServer(manyRows(150, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    expect(ids()).toHaveLength(100);
    server.rows = [makeTranscription({ id: "new", status: "succeeded", createdAt: newest(1) }), ...server.rows];
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(ids()).toContain("new"));
    expect(ids()).toHaveLength(100);
    expect(ids()).not.toContain("t-99");
    await transcriptionStore.loadMore();
    expect(ids()).toContain("t-99");
    expect(ids()).toHaveLength(151);
    unsubscribe();
  });

  it("removes an older row the server deleted", async () => {
    // Retention deletes the oldest rows first.
    const server = fakeServer(manyRows(150, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    await transcriptionStore.loadMore();
    expect(ids()).toContain("t-149");
    server.rows = server.rows.filter((t) => t.id !== "t-149" && t.id !== "t-120");
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(ids()).toHaveLength(148));
    expect(ids()).not.toContain("t-149");
    expect(ids()).not.toContain("t-120");
    unsubscribe();
  });

  it("shows the new status of an older row that changed between polls", async () => {
    // t-140 failed, then the user retried it and it succeeded.
    const server = fakeServer(manyRows(150, (i) => (i === 140 ? "failed" : "succeeded")));
    const { transcriptionStore, unsubscribe, status } = await started();
    await transcriptionStore.loadMore();
    expect(status("t-140")).toBe("failed");
    server.rows = server.rows.map((t) => (t.id === "t-140" ? { ...t, status: "succeeded" as const } : t));
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(status("t-140")).toBe("succeeded"));
    unsubscribe();
  });

  it("keeps the rows of a load more that runs together with a poll", async () => {
    const server = fakeServer(manyRows(250, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    let open!: () => void;
    server.gate = new Promise<void>((resolve) => { open = resolve; });
    // A poll starts, then load more starts before the poll ends.
    transcriptionStore.refreshNow();
    const loading = transcriptionStore.loadMore();
    expect(transcriptionStore.getSnapshot().loadingMore).toBe(true);
    server.gate = undefined;
    open();
    await loading;
    await vi.waitFor(() => expect(ids()).toHaveLength(200));
    expect(transcriptionStore.getSnapshot().loadingMore).toBe(false);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(true);
    // The poll that ends last must not remove the loaded page.
    await vi.advanceTimersByTimeAsync(10_000);
    expect(ids()).toHaveLength(200);
    unsubscribe();
  });

  it("discards a walk that a server change made obsolete", async () => {
    const old = fakeServer(manyRows(250, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    let open!: () => void;
    old.gate = new Promise<void>((resolve) => { open = resolve; });
    // This load more walks 2 pages of the old server and waits at the gate.
    const loading = transcriptionStore.loadMore();
    fakeServer([makeTranscription({ id: "other", status: "succeeded" })]);
    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(ids()).toEqual(["other"]));
    open();
    await loading;
    await vi.advanceTimersByTimeAsync(0);
    expect(ids()).toEqual(["other"]);
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    expect(transcriptionStore.getSnapshot().loadingMore).toBe(false);
    unsubscribe();
  });

  it("restores the page count when a load more fails", async () => {
    const server = fakeServer(manyRows(250, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    const working = listImpl;
    listImpl = () => Promise.reject(new Error("offline"));
    await expect(transcriptionStore.loadMore()).rejects.toThrow("offline");
    expect(transcriptionStore.getSnapshot().loadingMore).toBe(false);
    listImpl = working;
    transcriptionStore.refreshNow();
    await vi.waitFor(() => expect(server.requests.length).toBeGreaterThan(1));
    await vi.advanceTimersByTimeAsync(0);
    expect(ids()).toHaveLength(100);
    unsubscribe();
  });

  it("forget() removes a row at once and no earlier walk brings it back", async () => {
    const server = fakeServer(manyRows(3, () => "failed"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    let open!: () => void;
    server.gate = new Promise<void>((resolve) => { open = resolve; });
    transcriptionStore.refreshNow(); // a walk that still holds the row
    server.rows = server.rows.filter((t) => t.id !== "t-1");
    transcriptionStore.forget("t-1");
    expect(ids()).toEqual(["t-0", "t-2"]);
    server.gate = undefined;
    open();
    await vi.advanceTimersByTimeAsync(0);
    expect(ids()).toEqual(["t-0", "t-2"]);
    unsubscribe();
  });

  it("upsert() shows the returned row at once and no earlier walk overwrites it", async () => {
    const server = fakeServer(manyRows(2, () => "pending"));
    const { transcriptionStore, unsubscribe } = await started();
    const priorityAt = (id: string) => transcriptionStore.getSnapshot().transcriptions.find((t) => t.id === id)?.priorityAt;
    let open!: () => void;
    server.gate = new Promise<void>((resolve) => { open = resolve; });
    transcriptionStore.refreshNow(); // a walk that still holds the old row
    const updated = { ...server.rows[1], priorityAt: "2026-11-04T10:00:00Z" };
    transcriptionStore.upsert(updated);
    expect(priorityAt("t-1")).toBe("2026-11-04T10:00:00Z");
    server.rows = [server.rows[0], updated];
    server.gate = undefined;
    open();
    await vi.advanceTimersByTimeAsync(0);
    expect(priorityAt("t-1")).toBe("2026-11-04T10:00:00Z");
    unsubscribe();
  });

  it("forgets rows and cursors of the old server on a server change", async () => {
    fakeServer(manyRows(150, () => "succeeded"));
    const { transcriptionStore, unsubscribe, ids } = await started();
    await transcriptionStore.loadMore();
    fakeServer([makeTranscription({ id: "other", status: "succeeded" })]);
    transcriptionStore.resetForServerChange();
    await vi.waitFor(() => expect(ids()).toEqual(["other"]));
    expect(transcriptionStore.getSnapshot().hasMore).toBe(false);
    unsubscribe();
  });
});
