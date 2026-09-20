import { useSyncExternalStore } from "react";
import { health } from "./api";
import { HeliosError } from "./errors";
import type { HealthReport } from "./types";

/*
 * Connection state: "Health() plus stream liveness" (see the module doc on
 * `store.ts` for the event/staleness half of that pair). This module owns
 * the Health()-polling half and the cold-start/reconnect backoff DESIGN.md
 * §5.6/§5.7 specify: 1s -> 30s cap, exponential.
 *
 * - "connecting": no Health() call has resolved yet (app just launched).
 * - "online": the most recent Health() call succeeded.
 * - "reconnecting": the app was online at least once, and the most recent
 *   call failed — DESIGN.md §5.7's 28px connection bar.
 * - "offline": Health() has never once succeeded — DESIGN.md §5.6's
 *   full-surface cold-start state.
 */
export type ConnectionStatus = "connecting" | "online" | "reconnecting" | "offline";

export interface ConnectionState {
  status: ConnectionStatus;
  /** Number of consecutive failed Health() calls (0 while online). */
  attempt: number;
  /** Current backoff delay before the next automatic retry, in ms. */
  retryDelayMs: number;
  /** When the next automatic retry will fire, or undefined while online. */
  retryAt?: number;
  /** The most recent successful HealthReport, kept around while reconnecting so e.g. the disk-space warning does not disappear the instant a poll fails once. */
  report?: HealthReport;
  lastError?: HeliosError;
  lastCheckedAt?: number;
  lastOkAt?: number;
}

const MIN_RETRY_MS = 1000;
const MAX_RETRY_MS = 30000;
const STEADY_POLL_MS = 10000; // how often we re-check while online

type Listener = () => void;

class ConnectionMonitor {
  private state: ConnectionState = { status: "connecting", attempt: 0, retryDelayMs: MIN_RETRY_MS };
  private listeners = new Set<Listener>();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private started = false;
  private inFlight = false;

  getSnapshot = (): ConnectionState => this.state;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    this.ensureStarted();
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Cancels any pending backoff timer and checks immediately — wired to the "Retry now" button. */
  retryNow = (): void => {
    if (this.timer) clearTimeout(this.timer);
    void this.check();
  };

  private ensureStarted(): void {
    if (this.started) return;
    this.started = true;
    void this.check();
  }

  private schedule(delayMs: number): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.check(), delayMs);
  }

  private async check(): Promise<void> {
    if (this.inFlight) return;
    this.inFlight = true;
    try {
      // A resolved Health() call means the process is reachable, even when
      // report.ok is false (e.g. a failing disk check) — HealthReportDTO's
      // own doc says neither adapter returns a transport error for
      // "unreachable"; only a rejected promise here means that.
      const report = await health();
      this.setState({
        status: "online",
        attempt: 0,
        retryDelayMs: MIN_RETRY_MS,
        retryAt: undefined,
        report,
        lastError: undefined,
        lastCheckedAt: Date.now(),
        lastOkAt: Date.now(),
      });
      this.schedule(STEADY_POLL_MS);
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : undefined;
      const everOnline = this.state.lastOkAt !== undefined;
      const attempt = this.state.attempt + 1;
      const retryDelayMs = Math.min(MAX_RETRY_MS, MIN_RETRY_MS * 2 ** (attempt - 1));
      this.setState({
        status: everOnline ? "reconnecting" : "offline",
        attempt,
        retryDelayMs,
        retryAt: Date.now() + retryDelayMs,
        lastError: heliosErr,
        lastCheckedAt: Date.now(),
      });
      this.schedule(retryDelayMs);
    } finally {
      this.inFlight = false;
    }
  }

  private setState(next: ConnectionState): void {
    this.state = next;
    for (const listener of this.listeners) listener();
  }
}

export const connectionMonitor = new ConnectionMonitor();

/** Live connection state, backed by the app-wide `connectionMonitor` singleton (one poller regardless of how many components call this). */
export function useConnection(): ConnectionState {
  return useSyncExternalStore(connectionMonitor.subscribe, connectionMonitor.getSnapshot);
}
