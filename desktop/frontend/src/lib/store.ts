import { useSyncExternalStore } from "react";
import { list } from "./api";
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
 * this store must reach every row. The store remembers how many pages the
 * user has loaded (`pages`, starting at 1). Each refresh walks those pages
 * from the newest (`PAGE_SIZE` items per page, `createdAt` descending — the
 * API's default order and its maximum page size). When older rows remain, it
 * also walks the `running` and `pending` statuses, so every active job loads
 * and can be canceled. Then it REPLACES the cache with that result, so every
 * row shown is as current as the server. A poll with one page costs one
 * request. `loadMore()` raises `pages` by one. A row that falls off the
 * walked pages leaves the cache and returns with "load more". Use `forget()`
 * after a delete.
 *
 * A refresh that a server change or a newer refresh made obsolete never
 * touches the cache.
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
  // Counts refreshes. Only the newest refresh may apply its result.
  private latest = 0;
  // How many pages of rows a refresh walks (see this module's doc).
  private pages = 1;
  // Cursor for the page after the walked pages, or undefined when none remain.
  private olderCursor: string | undefined;
  private loadingMore = false;

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
   * Walks the loaded pages and the active jobs, then replaces the cache. Safe
   * to call any time, including manually (e.g. right after a
   * submit/cancel/retry so the UI doesn't wait out a full poll interval). A
   * call that starts while another is in flight makes the older call
   * obsolete. `gen` is snapshotted up front: if `resetForServerChange()`
   * bumps `generation` before `list()` (built from whatever `apiBase()` was
   * effective when this call started — see `./api.ts`) resolves, this call's
   * result describes a server that is no longer the effective one, so it is
   * discarded rather than published or used to reschedule the next poll.
   */
  refresh = (): Promise<void> => this.run(true);

  /**
   * Loads the next page of older rows. Does nothing when no older rows exist
   * or a load is already in flight. Rejects when the request fails, so the
   * caller can tell the user.
   */
  loadMore = async (): Promise<void> => {
    if (this.olderCursor === undefined || this.loadingMore) return;
    const gen = this.generation;
    this.loadingMore = true;
    this.pages++;
    this.publish({});
    try {
      await this.run(false);
    } catch (err) {
      if (gen === this.generation) this.pages--;
      throw err;
    } finally {
      if (gen === this.generation) {
        this.loadingMore = false;
        this.publish({});
      }
    }
  };

  /** Puts a row the server just returned into the cache and publishes it at once, then refreshes so no walk that was in flight overwrites it with an older row. */
  upsert = (t: Transcription): void => {
    this.byId.set(t.id, t);
    this.publish({});
    this.refreshNow();
  };

  /** Removes a row from the cache right away, for a row the user just deleted, then refreshes so no walk that was in flight brings it back. */
  forget = (id: string): void => {
    if (this.byId.delete(id)) this.publish({});
    this.refreshNow();
  };

  /** Cancels any pending poll timer and refreshes immediately — for an action (submit, cancel, retry, delete) that just changed server state and wants the UI to reflect it without waiting out the current interval. */
  refreshNow = (): void => {
    if (this.timer) clearTimeout(this.timer);
    void this.refresh();
  };

  /**
   * `refreshNow()`'s counterpart for Settings -> Server: bumps
   * `generation` before calling `refresh()`, so a fresh walk against the new
   * `apiBase()` starts immediately even while the previous one (still built
   * against the old server) is outstanding. Once the old walk resolves,
   * `run()`'s own checks discard its result instead of letting rows from the
   * old server land in the cache after the new server's own, correct ones
   * already did. Call this right after `setServerSettings` resolves.
   */
  resetForServerChange = (): void => {
    if (this.timer) clearTimeout(this.timer);
    this.generation++;
    this.loadingMore = false;
    // Rows, cursors and loaded pages of the old server must not carry over.
    this.byId = new Map();
    this.olderCursor = undefined;
    this.pages = 1;
    void this.refresh();
  };

  private async run(reportError: boolean): Promise<void> {
    const gen = this.generation;
    const mine = ++this.latest;
    const current = () => gen === this.generation && mine === this.latest;
    try {
      const walked = await this.walk(this.pages);
      if (!current()) return;
      this.byId = new Map(walked.rows.map((t) => [t.id, t]));
      this.olderCursor = walked.cursor;
      this.publish({ loading: false, error: undefined, lastSyncAt: Date.now() });
    } catch (err) {
      if (!current()) return;
      if (!reportError) throw err;
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      this.publish({ loading: false, error: heliosErr });
    } finally {
      if (current()) this.scheduleNext();
    }
  }

  /** Reads `pages` pages from the newest. When older rows remain, it also reads every active job. */
  private async walk(pages: number): Promise<{ rows: Transcription[]; cursor: string | undefined }> {
    const rows = new Map<string, Transcription>();
    let cursor: string | undefined;
    for (let i = 0; i < pages; i++) {
      const page = await list({ limit: PAGE_SIZE, cursor });
      for (const t of page.items) rows.set(t.id, t);
      cursor = page.nextCursor;
      if (!cursor) break;
    }
    if (cursor) {
      for (const status of ACTIVE_STATUSES) {
        let next: string | undefined;
        do {
          const page = await list({ limit: PAGE_SIZE, status, cursor: next });
          for (const t of page.items) rows.set(t.id, t);
          next = page.nextCursor;
        } while (next);
      }
    }
    return { rows: [...rows.values()], cursor };
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
      loadingMore: this.loadingMore,
      ...patch,
    };
    for (const listener of this.listeners) listener();
  }
}

export const transcriptionStore = new TranscriptionStore();

/** Live, polling-updated view of every transcription this client currently knows about (see this module's doc for what "currently knows about" means, its poll cadence and its pagination limits). */
export function useTranscriptions(): TranscriptionStoreSnapshot {
  return useSyncExternalStore(transcriptionStore.subscribe, transcriptionStore.getSnapshot);
}
