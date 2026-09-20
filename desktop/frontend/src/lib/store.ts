import { useSyncExternalStore } from "react";
import { list } from "./api";
import { HeliosError } from "./errors";
import type { Transcription } from "./types";

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
 * Pagination is deliberately NOT this store's job: `refresh()` fetches one
 * page (`INITIAL_PAGE_SIZE` items, newest first — matches the API's
 * default `createdAt desc` order). Library's own filtered, cursor-paginated
 * view should call `list()` from `./api` directly; only mount this store
 * where you want the live, polling-updated view (the Transcribe queue, and
 * any Library row you want to keep in sync with an in-flight status change
 * without re-fetching).
 */

const INITIAL_PAGE_SIZE = 200;
const ACTIVE_POLL_MS = 1500;
const IDLE_POLL_MS = 10000;

export interface TranscriptionStoreSnapshot {
  /** All known transcriptions, `createdAt` descending. */
  transcriptions: Transcription[];
  /** True only for the very first `refresh()`; never true again afterwards, so a routine poll does not bounce the UI back to a loading state. */
  loading: boolean;
  error?: HeliosError;
  /** When the most recent successful `refresh()` completed. */
  lastSyncAt?: number;
}

type Listener = () => void;

class TranscriptionStore {
  private byId = new Map<string, Transcription>();
  private snapshot: TranscriptionStoreSnapshot = { transcriptions: [], loading: true };
  private listeners = new Set<Listener>();
  private started = false;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private inFlight = false;

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

  /** Re-fetches the first page from the server and replaces the cache wholesale. Safe to call any time, including manually (e.g. right after a submit/cancel/retry so the UI doesn't wait out a full poll interval). */
  refresh = async (): Promise<void> => {
    if (this.inFlight) return;
    this.inFlight = true;
    try {
      const page = await list({ limit: INITIAL_PAGE_SIZE });
      this.byId = new Map(page.items.map((t) => [t.id, t]));
      this.publish({ loading: false, error: undefined, lastSyncAt: Date.now() });
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      this.publish({ loading: false, error: heliosErr });
    } finally {
      this.inFlight = false;
      this.scheduleNext();
    }
  };

  /** Cancels any pending poll timer and refreshes immediately — for an action (submit, cancel, retry, delete) that just changed server state and wants the UI to reflect it without waiting out the current interval. */
  refreshNow = (): void => {
    if (this.timer) clearTimeout(this.timer);
    void this.refresh();
  };

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
    this.snapshot = { ...this.snapshot, transcriptions, ...patch };
    for (const listener of this.listeners) listener();
  }
}

export const transcriptionStore = new TranscriptionStore();

/** Live, polling-updated view of every transcription this client currently knows about (see this module's doc for what "currently knows about" means, its poll cadence and its pagination limits). */
export function useTranscriptions(): TranscriptionStoreSnapshot {
  return useSyncExternalStore(transcriptionStore.subscribe, transcriptionStore.getSnapshot);
}
