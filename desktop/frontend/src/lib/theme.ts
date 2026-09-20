import { useSyncExternalStore } from "react";

/*
 * Theme preference. DESIGN.md §2.2: "the app stamps data-theme=\"dark\" on
 * first run (Settings offers System / Light / Dark) — this is a media tool
 * and dark is the working default, not the alternate." So the persisted
 * default is `"dark"`, not `"system"` — a user who never opens Settings
 * still gets the working default rather than inheriting whatever their OS
 * happens to be set to.
 *
 * "System" is expressed as the *absence* of a `data-theme` attribute: the
 * unguarded `@media (prefers-color-scheme: dark)` block in
 * `styles/tokens.css` then takes over. Explicit "light" and "dark" set
 * `data-theme` and always win over the OS preference in both directions —
 * see tokens.css's header comment for exactly how that guarantee holds.
 */
export type ThemePreference = "system" | "light" | "dark";

const STORAGE_KEY = "helios:theme";
const DEFAULT_PREFERENCE: ThemePreference = "dark";

function isThemePreference(value: unknown): value is ThemePreference {
  return value === "system" || value === "light" || value === "dark";
}

function readStored(): ThemePreference | undefined {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return isThemePreference(raw) ? raw : undefined;
  } catch {
    // localStorage can throw in a locked-down WebView; fall back to the default.
    return undefined;
  }
}

function writeStored(pref: ThemePreference): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, pref);
  } catch {
    // Best effort — losing the persisted preference is not worth crashing over.
  }
}

function applyToDocument(pref: ThemePreference): void {
  const root = document.documentElement;
  if (pref === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", pref);
  }
}

type Listener = () => void;

class ThemeStore {
  private preference: ThemePreference;
  private listeners = new Set<Listener>();
  private initialized = false;

  constructor() {
    this.preference = DEFAULT_PREFERENCE;
  }

  getSnapshot = (): ThemePreference => this.preference;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    this.ensureInitialized();
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Persists and applies `pref` immediately — the explicit toggle always wins, per this module's doc. */
  set = (pref: ThemePreference): void => {
    this.preference = pref;
    writeStored(pref);
    applyToDocument(pref);
    for (const listener of this.listeners) listener();
  };

  private ensureInitialized(): void {
    if (this.initialized) return;
    this.initialized = true;
    const stored = readStored();
    if (stored) {
      this.preference = stored;
    } else {
      // First run: persist the default explicitly, so it is stable across
      // an OS-level preference change rather than silently becoming
      // "system" the first time someone inspects localStorage.
      writeStored(DEFAULT_PREFERENCE);
    }
    applyToDocument(this.preference);
  }
}

export const themeStore = new ThemeStore();

/** The current theme preference plus a setter — the app shell's theme toggle and Settings dialog both drive this. */
export function useTheme(): [ThemePreference, (pref: ThemePreference) => void] {
  const preference = useSyncExternalStore(themeStore.subscribe, themeStore.getSnapshot);
  return [preference, themeStore.set];
}
