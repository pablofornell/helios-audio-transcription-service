import { HeliosError } from "../errors";
import type { ServerSettings } from "../types";

/*
 * The browser adapter's own "which server" setting — `./api.ts`'s
 * `getServerSettings`/`setServerSettings`/`testServerConnection` are thin
 * wrappers around what lives here. There is no Go process behind this the
 * way `desktop/serversettings.go` has one behind the Wails adapter — a
 * browser tab persists its own choice, per browser, in `localStorage` —
 * but the validation rules (`cleanHost`/`validatePort` below) and the
 * overall shape (host/port/scheme, a typed "http://"/"https://" prefix on
 * Host overrides the scheme, Test before Save) deliberately mirror that
 * file's `cleanServerHost`/`validateServerPort`/`assembleCandidateURL`
 * exactly, so the two Settings → Server sections behave identically from
 * the user's point of view.
 *
 * Default (nothing ever saved, or `localStorage` unavailable/cleared) is
 * same-origin: every request stays a plain relative path
 * (`apiBase() === ""`), exactly this module's predecessor's only mode, so
 * a deployment that never touches this setting keeps working with zero
 * config and no CORS needed on the API (see `internal/httpapi`'s CORS
 * middleware doc for the one case that does need it: a *saved*,
 * cross-origin target). Saving the page's own origin's host/port back
 * (shown, pre-filled, in Settings — see `getServerSettings`, and
 * `ServerSettingsSection`'s read-only "Currently connected to" line) is
 * this module's supported way back to that same effective behaviour, per
 * this feature's brief — there is no separate "reset" control to keep this
 * surface small.
 */

const STORAGE_KEY = "helios:server";

const MIN_PORT = 1;
const MAX_PORT = 65535;

type Scheme = "http" | "https";

/** localStorage's exact persisted shape — every field already normalised (cleanHost/http(s)) by the time it is written, so a read never needs to re-validate it, only re-check it still parses as JSON with the right field types (see `readStored`). */
interface StoredServer {
  host: string;
  port: number;
  scheme: Scheme;
}

function isStoredServer(value: unknown): value is StoredServer {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return typeof v.host === "string" && v.host !== "" && typeof v.port === "number" && (v.scheme === "http" || v.scheme === "https");
}

/** Reads and validates the persisted server, or `undefined` when nothing valid is there — no distinction is made between "never saved", "cleared" and "localStorage threw/returned garbage": all three mean "use the default" (see this module's own doc). */
function readStored(): StoredServer | undefined {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return undefined;
    const parsed: unknown = JSON.parse(raw);
    return isStoredServer(parsed) ? parsed : undefined;
  } catch {
    // localStorage can throw in a locked-down context (private browsing in
    // some browsers, a strict CSP, a full quota) — treated exactly like
    // "nothing saved", mirroring lib/theme.ts's own try/catch.
    return undefined;
  }
}

function writeStored(server: StoredServer): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(server));
  } catch {
    // Best effort — see readStored's doc. The setting still takes effect
    // for the rest of this page's lifetime (the in-memory save below this
    // module's callers actually read from is `readStored()` re-run on
    // demand, but nothing here caches a value across it — a failed write
    // simply means the *next* read falls back to the default again, same
    // as it would after this tab closes).
  }
}

/** This page's own origin, split the way `StoredServer` is shaped — the default `apiBase()`/`getServerSettings()` fall back to. Never persisted; recomputed every time, so it always reflects wherever this page is actually being served from right now. */
function locationServer(): StoredServer {
  const scheme: Scheme = window.location.protocol === "https:" ? "https" : "http";
  const port = window.location.port ? Number(window.location.port) : scheme === "https" ? 443 : 80;
  return { host: window.location.hostname, port, scheme };
}

/** The effective server right now: the saved one if there is a valid one, else this page's own origin. Every function below is built on this single source of truth. */
function effective(): StoredServer {
  return readStored() ?? locationServer();
}

function originOf(server: StoredServer): string {
  return `${server.scheme}://${server.host}:${server.port}`;
}

/**
 * True when `candidate` is not this page's own origin — used only by
 * `./api.ts`'s `testServerConnection`, to decide whether a bare transport
 * failure is worth a short "...or this origin isn't allowed" hint: a
 * same-origin request is never subject to CORS at all, so that hint would
 * be actively misleading there.
 */
export function isCrossOrigin(candidate: StoredServer): boolean {
  const page = locationServer();
  return candidate.host !== page.host || candidate.port !== page.port || candidate.scheme !== page.scheme;
}

/**
 * The base every request in `./api.ts` is built from: `""` (so a path
 * like `${apiBase()}/api/v1/transcriptions` stays the same same-origin
 * relative path this adapter has always sent) when nothing is saved, else
 * the saved server's absolute origin. Never `locationServer()` spelled out
 * as an absolute URL even when it happens to equal the page's own origin
 * — only an explicit save does that (see this module's own doc), so the
 * common case never pays for an absolute URL or a CORS preflight it does
 * not need.
 */
export function apiBase(): string {
  const stored = readStored();
  return stored ? originOf(stored) : "";
}

/**
 * `getServerSettings`'s answer: mirrors
 * `desktop/serversettings.go`'s `ServerSettingsDTO` field for field, but
 * `editable` is always `true` here (a browser tab can always change this —
 * see `./api.ts`'s doc on what used to be true instead) and `warning` is
 * always absent (there is no on-disk file here that can be found invalid
 * at startup the way `desktop.json` can — an invalid `localStorage` value
 * is simply treated as "nothing saved", not surfaced as a warning; see
 * `readStored`).
 */
export function currentSettings(): ServerSettings {
  const server = effective();
  return { host: server.host, port: server.port, baseUrl: originOf(server), editable: true };
}

/**
 * True exactly when `scheme` would be blocked by the browser's own
 * mixed-content policy from this page as it is currently loaded: an https
 * page may not fetch a plain http target at all (no user-facing override
 * exists for `fetch`, unlike an image or a script). Checked up front,
 * before ever attempting the network round trip, so the error this
 * produces names the actual cause instead of the opaque `TypeError: Failed
 * to fetch` a blocked request would otherwise surface as (see
 * `classifyTransportError`, which cannot tell the two apart after the
 * fact).
 */
function mixedContentBlocked(scheme: Scheme): boolean {
  return window.location.protocol === "https:" && scheme === "http";
}

function mixedContentError(host: string, port: number): HeliosError {
  return new HeliosError(
    "invalid_input",
    `this page is loaded over https, so a plain http server (${host}:${port}) would be blocked by the browser's mixed-content protection — type "https://${host}" to use https instead, or open this page itself over http`,
  );
}

/**
 * Validates and normalises a user-typed Host exactly like
 * `desktop/serversettings.go`'s `cleanServerHost`: trims whitespace,
 * recognises (and strips) a leading `"http://"`/`"https://"` as
 * `typedScheme` rather than discarding it — the match is case-sensitive,
 * exactly like `cleanServerHost`'s own `strings.HasPrefix`, so e.g.
 * `"HTTPS://example.com"` is not recognised as a scheme prefix at all and
 * falls through to the `"://"` rejection below, the same outcome the Go
 * side gives it — and rejects — as a `HeliosError` with code
 * `"invalid_input"`, the same code `desktop/serversettings.go` reports for
 * the same mistakes — anything left over that is not a bare hostname:
 * empty, whitespace, a stray `"://"`, a path/query/fragment separator, a
 * `:port` (use the Port field instead), `"@"`/`"%"`/`"\\"`, or anything
 * else that fails to form a valid URL host component at all.
 */
function cleanHost(host: string): { cleanHost: string; typedScheme?: Scheme } {
  let h = host.trim();
  let typedScheme: Scheme | undefined;
  if (h.startsWith("https://")) {
    typedScheme = "https";
    h = h.slice(8);
  } else if (h.startsWith("http://")) {
    typedScheme = "http";
    h = h.slice(7);
  }

  if (h === "") throw new HeliosError("invalid_input", "server host must not be empty");
  if (/[ \t\r\n]/.test(h)) throw new HeliosError("invalid_input", "server host must not contain whitespace");
  if (h.includes("://")) throw new HeliosError("invalid_input", "server host must not include a scheme other than http/https");
  if (/[/?#]/.test(h)) throw new HeliosError("invalid_input", "server host must not include a path");
  if (h.includes(":")) throw new HeliosError("invalid_input", "server host must not include a port — use the Port field");
  if (/[@%\\]/.test(h)) throw new HeliosError("invalid_input", `server host must not contain "@", "%" or "\\"`);
  try {
    // Thrown away on purpose — exactly like cleanServerHost's own
    // buildServerURL(host, minServerPort) placeholder, only h's ability to
    // form a valid URL host component at all is being checked here.
    void new URL(`http://${h}:1`);
  } catch {
    throw new HeliosError("invalid_input", `server host "${h}" is not a valid hostname`);
  }
  return { cleanHost: h, typedScheme };
}

/** Validates a user-typed Port exactly like `desktop/serversettings.go`'s `validateServerPort`: an integer TCP listeners can actually bind to. */
function validatePort(port: number): void {
  if (!Number.isInteger(port) || port < MIN_PORT || port > MAX_PORT) {
    throw new HeliosError("invalid_input", `server port must be between ${MIN_PORT} and ${MAX_PORT}, got ${port}`);
  }
}

/**
 * Validates host/port, resolves the scheme to use (host's own typed
 * prefix, else `fallbackScheme` — the currently-effective scheme, so an
 * untyped edit never silently downgrades an https target to http), and
 * rejects a candidate mixed-content policy would block outright — mirrors
 * `desktop/serversettings.go`'s `assembleCandidateURL`, with the
 * mixed-content check added on top (that file has no browser to be
 * mixed-content-blocked in). Both `setServerSettings` and
 * `testServerConnection` build their candidate through this one function,
 * so the two can never disagree about what a given host/port resolves to.
 */
function assembleCandidate(host: string, port: number, fallbackScheme: Scheme): StoredServer {
  const { cleanHost: h, typedScheme } = cleanHost(host);
  validatePort(port);
  const scheme = typedScheme ?? fallbackScheme;
  if (mixedContentBlocked(scheme)) {
    throw mixedContentError(h, port);
  }
  return { host: h, port, scheme };
}

/** `setServerSettings`'s implementation — see `./api.ts`'s re-export for its documented contract. */
export function setServer(host: string, port: number): ServerSettings {
  const candidate = assembleCandidate(host, port, effective().scheme);
  writeStored(candidate);
  return currentSettings();
}

/**
 * `assembleCandidate`'s validation, exposed standalone for
 * `testServerConnection`, which must reject a malformed host/port (or a
 * mixed-content-blocked one) the exact same way `setServer` would, without
 * ever calling `writeStored` — a test must never change which server this
 * page is actually pointed at, successful or not.
 */
export function validateCandidate(host: string, port: number): StoredServer {
  return assembleCandidate(host, port, effective().scheme);
}
