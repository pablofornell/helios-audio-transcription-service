import { useSyncExternalStore } from "react";
import { get, list } from "./api";
import { HeliosError } from "./errors";
import type { Status, Transcription } from "./types";

/*
 * The live transcription cache: a single, app-wide source of truth for
 * "every transcription this client currently knows about", kept in sync by
 * POLLING — this service has no `/api/v1/events` SSE endpoint (see
 * DESKTOP.md's binding contract), unlike the sibling project this frontend
 * was copied from. Both feature screens read from this instead of
 * independently calling `list()` on their own timers, so a job's status is
 * never seen to disagree between the Transcribe queue and a Library row for
 * the same id.
 *
 * Poll cadence is the one behavioural rule this store exists to enforce:
 * every ACTIVE_POLL_MS (1.5s) while at least one known transcription is
 * `pending` or `running`, every IDLE_POLL_MS (10s) otherwise — checked
 * fresh after every refresh, so the cadence speeds up the instant a submit
 * lands a new pending row and slows back down the instant the last active
 * job resolves.
 *
 * Reach: the queue is the only list of failed, canceled and active jobs, so
 * this store must reach every row. Each `refresh()` fetches the newest page
 * (`PAGE_SIZE` items, `createdAt` descending — the API's default order and
 * its maximum page size). When more rows exist, it also follows cursors for
 * the `running` and `pending` statuses, so every active job loads and can be
 * canceled. A normal poll with one page costs one request. Older rows load
 * on request with `loadMore()` and then stay in the cache. A cached row that
 * is older than the newest page keeps its last known state. If that state
 * is active, the next refresh reads the row again with `get()`. Use
 * `forget()` after a delete.
 *
 * Library's own filtered, cursor-paginated view calls `list()` from `./api`
 * directly.
 */

/** The API's maximum page size (`maxListLimit` in internal/app/service.go). */
const PAGE_SIZE = 100;
const ACTIVE_STATUSES: Status[] = ["running", "pending"];
export const ACTIVE_POLL_MS = 1500;
export const IDLE_POLL_MS = 10000;

export interface TranscriptionStoreSnapshot {
  /** All known transcriptions, `createdAt` descending. */
  transcriptions: Transcription[];
  /** True only for the very first `refresh()`; never true again afterwards, so a routine poll does not bounce the UI back to a loading state. */
  loading: boolean;
  error?: HeliosError;
  /** When the most recent successful `refresh()` completed. */
  lastSyncAt?: number;
  /** True while rows older than the loaded ones exist on the server. */
  hasMore: boolean;
  /** True while `loadMore()` is in flight. */
  loadingMore: boolean;
}

type Listener = () => void;

class TranscriptionStore {
  private byId = new Map<string, Transcription>();
  private snapshot: TranscriptionStoreSnapshot = { transcriptions: [], loading: true, hasMore: false, loadingMore: false };
  private listeners = new Set<Listener>();
  private started = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  // Bumped by resetForServerChange(); a refresh() call's own gen is
  // captured up front (see refresh's doc) and checked again once its
  // list() resolves, so a request that was already in flight against the
  // old server when the bump happened cannot apply its (stale) result on
  // top of a newer refresh's — see resetForServerChange's own doc.
  private generation = 0;
  // The generation a refresh() call is currently in flight for, or
  // undefined between calls — a plain boolean would let
  // resetForServerChange's forced refresh() get skipped by this same
  // dedupe guard while the old server's request is still outstanding.
  private inFlightGen: number | undefined;
  // Cursor for the next older page, and whether `loadMore()` ran. Until it
  // runs, every refresh moves the cursor to the end of the newest page.
  private olderCursor: string | undefined;
  private moreLoaded = false;
  private loadingMoreGen: number | undefined;

  getSnapshot = (): TranscriptionStoreSnapshot => this.snapshot;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    this.ensureStarted();
    return () => {
      this.listeners.delete(listener);
      // Last subscriber gone: stop polling rather than running forever in
      // the background — the next subscriber (a screen remount) restarts
      // it via ensureStarted().
      if (this.listeners.size === 0) {
        if (this.timer) clearTimeout(this.timer);
        this.timer = undefined;
        this.started = false;
      }
    };
  };

  /**
   * Re-fetches the newest page and every active job from the server and
   * rebuilds the cache (see this module's doc for the older rows it keeps).
   * Safe to call any time, including manually (e.g. right after
   * a submit/cancel/retry so the UI doesn't wait out a full poll
   * interval). `gen` is snapshotted up front: if `resetForServerChange()`
   * bumps `generation` again before `list()` (built from whatever
   * `apiBase()` was effective when this call started — see `./api.ts`)
   * resolves, this call's result describes a server that is no longer the
   * effective one, so it is discarded rather than published or used to
   * reschedule the next poll.
   */
  refresh = async (): Promise<void> => {
    if (this.inFlightGen !== undefined) return;
    const gen = this.generation;
    this.inFlightGen = gen;
    try {
      const page = await list({ limit: PAGE_SIZE });
      const live = new Map(page.items.map((t) => [t.id, t]));
      // More rows exist than one page holds: also load every active job.
      if (page.nextCursor) {
        for (const status of ACTIVE_STATUSES) {
          for (let cursor: string | undefined; ; ) {
            const active = await list({ limit: PAGE_SIZE, status, cursor });
            for (const t of active.items) live.set(t.id, t);
            if (!active.nextCursor) break;
            cursor = active.nextCursor;
          }
        }
      }
      // With one page only, the page holds every row.
      const last = page.nextCursor ? page.items.at(-1) : undefined;
      const stale = this.staleRows(live, last);
      const reread = stale.length ? await this.reread(stale, gen) : new Map<string, Transcription | null>();
      if (gen !== this.generation) return;
      this.byId = this.merge(live, last, reread);
      if (!page.nextCursor) {
        this.olderCursor = undefined;
        this.moreLoaded = false;
      } else if (!this.moreLoaded) {
        this.olderCursor = page.nextCursor;
      }
      this.publish({ loading: false, error: undefined, lastSyncAt: Date.now() });
    } catch (err) {
      if (gen !== this.generation) return;
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      this.publish({ loading: false, error: heliosErr });
    } finally {
      if (this.inFlightGen === gen) this.inFlightGen = undefined;
      if (gen === this.generation) this.scheduleNext();
    }
  };

  /**
   * Loads the next page of older rows into the cache. Does nothing when no
   * older rows exist or a load is already in flight. Rejects when the request
   * fails, so the caller can tell the user.
   */
  loadMore = async (): Promise<void> => {
    const cursor = this.olderCursor;
    if (!cursor || this.loadingMoreGen !== undefined) return;
    const gen = this.generation;
    this.loadingMoreGen = gen;
    this.publish({});
    try {
      const page = await list({ limit: PAGE_SIZE, cursor });
      if (gen !== this.generation) return;
      for (const t of page.items) this.byId.set(t.id, t);
      this.moreLoaded = true;
      this.olderCursor = page.nextCursor;
    } finally {
      if (this.loadingMoreGen === gen) this.loadingMoreGen = undefined;
      if (gen === this.generation) this.publish({});
    }
  };

  /** Removes a row from the cache right away, for a row the user just deleted. */
  forget = (id: string): void => {
    if (this.byId.delete(id)) this.publish({});
  };

  /** Cancels any pending poll timer and refreshes immediately — for an action (submit, cancel, retry, delete) that just changed server state and wants the UI to reflect it without waiting out the current interval. */
  refreshNow = (): void => {
    if (this.timer) clearTimeout(this.timer);
    void this.refresh();
  };

  /**
   * `refreshNow()`'s counterpart for Settings -> Server: bumps
   * `generation` and clears `inFlightGen` before calling `refresh()`, so a
   * fresh request against the new `apiBase()` starts immediately even
   * while the previous one (still built against the old server) is
   * outstanding — that older call's dedupe slot is abandoned, and once it
   * eventually resolves, `refresh()`'s own `gen` check discards its result
   * instead of letting rows from the old server land in the cache after
   * the new server's own, correct ones already did. Call this right after
   * `setServerSettings` resolves.
   */
  resetForServerChange = (): void => {
    if (this.timer) clearTimeout(this.timer);
    this.generation++;
    this.inFlightGen = undefined;
    this.loadingMoreGen = undefined;
    // Rows and cursors of the old server must not carry over.
    this.byId = new Map();
    this.olderCursor = undefined;
    this.moreLoaded = false;
    void this.refresh();
  };

  /** The cached active rows that the refresh did not return. They are older than the newest page and have since finished. */
  private staleRows(live: Map<string, Transcription>, last: Transcription | undefined): Transcription[] {
    if (!last) return [];
    return [...this.byId.values()].filter((t) => !live.has(t.id) && isOlder(t, last) && (t.status === "pending" || t.status === "running"));
  }

  /** Reads the given rows again. A value of `null` means the server no longer has the row. */
  private async reread(rows: Transcription[], gen: number): Promise<Map<string, Transcription | null>> {
    const reread = new Map<string, Transcription | null>();
    // Bound the number of concurrent requests.
    for (let start = 0; start < rows.length && gen === this.generation; start += 8) {
      await Promise.all(rows.slice(start, start + 8).map(async (t) => {
        try {
          reread.set(t.id, await get(t.id));
        } catch (err) {
          if (err instanceof HeliosError && err.code === "not_found") reread.set(t.id, null);
        }
      }));
    }
    return reread;
  }

  /**
   * Builds the cache after a refresh. A cached row that the refresh did not
   * return is kept only when it is older than the newest page. A newer row
   * that is missing was deleted.
   */
  private merge(live: Map<string, Transcription>, last: Transcription | undefined, reread: Map<string, Transcription | null>): Map<string, Transcription> {
    const next = new Map<string, Transcription>();
    if (last) {
      for (const t of this.byId.values()) {
        if (live.has(t.id) || !isOlder(t, last)) continue;
        const again = reread.get(t.id);
        if (again !== null) next.set(t.id, again ?? t);
      }
    }
    for (const [id, t] of live) next.set(id, t);
    return next;
  }

  private ensureStarted(): void {
    if (this.started) return;
    this.started = true;
    void this.refresh();
  }

  private scheduleNext(): void {
    if (this.listeners.size === 0) return;
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.refresh(), this.hasActiveJob() ? ACTIVE_POLL_MS : IDLE_POLL_MS);
  }

  private hasActiveJob(): boolean {
    for (const t of this.byId.values()) {
      if (t.status === "pending" || t.status === "running") return true;
    }
    return false;
  }

  private publish(patch: Partial<TranscriptionStoreSnapshot>): void {
    const transcriptions = Array.from(this.byId.values()).sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1));
    this.snapshot = {
      ...this.snapshot,
      transcriptions,
      hasMore: this.olderCursor !== undefined,
      loadingMore: this.loadingMoreGen !== undefined,
      ...patch,
    };
    for (const listener of this.listeners) listener();
  }
}

/** True when `t` sorts after `last` in the API's default order (`createdAt` descending, then id descending). */
function isOlder(t: Transcription, last: Transcription): boolean {
  const milliseconds = Date.parse(t.createdAt) - Date.parse(last.createdAt);
  if (milliseconds) return milliseconds < 0;
  const fraction = (value: string) => (/\.(\d+)/.exec(value)?.[1] ?? "").padEnd(9, "0");
  const order = fraction(t.createdAt).localeCompare(fraction(last.createdAt));
  return order ? order < 0 : t.id < last.id;
}

export const transcriptionStore = new TranscriptionStore();

/** Live, polling-updated view of every transcription this client currently knows about (see this module's doc for what "currently knows about" means, its poll cadence and its pagination limits). */
export function useTranscriptions(): TranscriptionStoreSnapshot {
  return useSyncExternalStore(transcriptionStore.subscribe, transcriptionStore.getSnapshot);
}
