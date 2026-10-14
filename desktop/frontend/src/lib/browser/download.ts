import { useSyncExternalStore } from "react";

// A native attachment navigation exposes no download-complete event. This
// short launch cooldown blocks double clicks across mounted copies of an
// action, without tying other items to a slow server response or disk save.
const LAUNCH_COOLDOWN_MS = 1500;
const launching = new Set<string>();
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
};
function notify() { listeners.forEach((listener) => listener()); }

export function useBrowserDownloadBusy(url: string): boolean {
  return useSyncExternalStore(subscribe, () => launching.has(url), () => false);
}

/** Call synchronously from a user gesture. Native downloads stream to disk. */
export function triggerBrowserDownloadFromClick(url: string): boolean {
  if (launching.has(url)) return false;
  launching.add(url);
  notify();
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "";
  // Cross-origin download attributes are ignored. A fresh context confines
  // error documents and keeps the app open; the synchronous click retains
  // user activation. Same-origin downloads need no extra browsing context.
  if (new URL(url, window.location.href).origin !== window.location.origin) {
    anchor.target = "_blank";
    anchor.rel = "noopener";
  }
  document.body.appendChild(anchor);
  try {
    anchor.click();
  } finally {
    anchor.remove();
    setTimeout(() => {
      launching.delete(url);
      notify();
    }, LAUNCH_COOLDOWN_MS);
  }
  return true;
}
