import { useSyncExternalStore } from "react";

/*
 * A single shared 1Hz tick, for every "seconds since"/"seconds until"
 * display in the Queue — the retry-backoff countdown and the stale
 * "last update Ns ago" line. DESIGN.md §4.3 is explicit that this is "one
 * setInterval(1000) for the whole list, not one per row"; this module is
 * that one interval, shared via useSyncExternalStore the same way
 * lib/store.ts and lib/connection.ts share their own singletons. It starts
 * lazily on first subscriber and stops when the last one unmounts, so a
 * queue with nothing time-sensitive to show costs nothing.
 */

type Listener = () => void;

class Clock {
  private now = Date.now();
  private listeners = new Set<Listener>();
  private timer: ReturnType<typeof setInterval> | undefined;

  getSnapshot = (): number => this.now;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    if (!this.timer) {
      this.timer = setInterval(() => {
        this.now = Date.now();
        for (const l of this.listeners) l();
      }, 1000);
    }
    return () => {
      this.listeners.delete(listener);
      if (this.listeners.size === 0 && this.timer) {
        clearInterval(this.timer);
        this.timer = undefined;
      }
    };
  };
}

const clock = new Clock();

/** The shared clock's current `Date.now()`, refreshed once a second while at least one caller is mounted. */
export function useClockTick(): number {
  return useSyncExternalStore(clock.subscribe, clock.getSnapshot);
}
