import { useSyncExternalStore } from "react";

/*
 * A tiny "the effective server just changed" signal — `./api.ts`'s
 * `setServerSettings` wrapper bumps it once, right after either adapter's
 * save actually resolves. Nothing here holds the settings themselves
 * (still `getServerSettings()`'s job); this is only the "something you may
 * have cached is now stale" notification a plain `useEffect` keyed on,
 * say, `transcription.id` has no way to receive on its own — see
 * `features/library/DetailPanel.tsx`'s `<audio src>` for the one place
 * that mattered enough to fix. Deliberately not folded into
 * `./connection.ts`'s ConnectionMonitor or `./store.ts`'s
 * TranscriptionStore: this has no state of its own besides a bump count,
 * nothing to poll, and nothing to reset.
 */

let version = 0;
const listeners = new Set<() => void>();

/** Bumped by `./api.ts`'s `setServerSettings` right after a save actually takes effect — not meant to be called from feature code directly. */
export function bumpServerSettingsVersion(): void {
  version += 1;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** Synchronous version check for async action completions, before React rerenders. */
export function getServerSettingsVersion(): number {
  return version;
}

/** Changes every time `setServerSettings` succeeds, Wails or browser adapter alike — include it in a `useEffect`'s dependency array to re-run that effect after the server changes, even when nothing else about it (e.g. a `transcription.id`) did. */
export function useServerSettingsVersion(): number {
  return useSyncExternalStore(subscribe, getServerSettingsVersion);
}
