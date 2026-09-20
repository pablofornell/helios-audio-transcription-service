/*
 * Detects whether this page is running inside the Wails desktop shell, as
 * opposed to an ordinary browser tab pointed at the REST API. `api.ts` and
 * `events.ts` are the only two callers — see their module docs for what
 * each side of the fork looks like.
 *
 * Wails' generated bindings (`wailsjs/go/main/App.js`) call through
 * `window['go']['main']['App'][...]`, and `wailsjs/runtime/runtime.js`
 * calls through `window.runtime`. Both are injected into `window` by the
 * WebKitGTK/WebView2/WebKit host before the frontend's own script runs;
 * neither exists in a plain browser tab, so their absence is exactly the
 * signal to fall back to the REST adapter. Checking `go.main.App`
 * specifically, not just `go`, matches what the bound methods themselves
 * dereference — see `wailsjs/go/main/App.js`.
 */

interface MaybeWailsWindow {
  go?: { main?: { App?: Record<string, unknown> } };
  runtime?: Record<string, unknown>;
}

/** True when the generated Wails bindings this app calls actually resolve. */
export function hasWailsRuntime(): boolean {
  if (typeof window === "undefined") return false;
  const w = window as unknown as MaybeWailsWindow;
  return Boolean(w.go?.main?.App) && Boolean(w.runtime);
}
