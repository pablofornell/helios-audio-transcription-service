import { HeliosError } from "../errors";
import type {
  AudioSource,
  HealthReport,
  ListFilter,
  ListPage,
  MediaLocation,
  Options,
  ServerSettings,
  Transcription,
  TranscriptFormat,
} from "../types";
import { classifyRestError, classifyTransportError } from "./restErrors";
import { apiBase, currentSettings, isCrossOrigin, setServer, validateCandidate } from "./serverConfig";

/*
 * REST implementation of the same adapter surface as `../wails/api.ts`,
 * talking to the API `internal/httpapi` serves (docs/openapi.yaml) instead
 * of a bound Wails method. `../api.ts` picks this implementation or
 * `../wails/api.ts` at call time (see `../runtime.ts`) and re-exports
 * whichever one under the same names.
 *
 * Every request is built from `apiBase()` (`./serverConfig.ts`) plus a
 * fixed path — `${apiBase()}/api/v1/...` or `${apiBase()}/readyz`. By
 * default `apiBase()` is `""`, so those stay the exact same same-origin
 * relative paths this module has always sent: a deployment that never
 * opens Settings → Server keeps working with zero config, and in
 * development `vite.config.ts` still proxies both prefixes to the API, no
 * CORS configuration needed there either. Once Settings → Server saves a
 * different host/port, `apiBase()` instead returns that server's absolute
 * origin, and every request below becomes a genuine cross-origin one — see
 * `internal/httpapi`'s CORS middleware (`HELIOS_CORS_ALLOWED_ORIGINS`) for
 * the one thing that then has to be true on the API side for it to work at
 * all.
 *
 * The wire DTOs in `internal/httpapi/dto.go` were deliberately built
 * field-for-field identical to `../types.ts` (same camelCase names, same
 * null-vs-omitted handling) precisely so this module never needs its own
 * parallel DTO layer: a parsed response body already IS a
 * `Transcription`/`ListPage`/`HealthReport`.
 */

const API_BASE = "/api/v1";

/** Builds one `/api/v1/...` request URL against the currently effective server (`./serverConfig.ts`'s `apiBase()`) — every REST call below goes through this, never `API_BASE` directly, so a saved server takes effect immediately without this module needing to re-derive it per call site. */
function apiPath(path: string): string {
  return `${apiBase()}${API_BASE}${path}`;
}

/** Issues `fetch(path, init)`, turning a transport failure (never a non-2xx response — see `requestJSON`) into a `HeliosError`. */
async function doFetch(path: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(path, init);
  } catch (err) {
    throw classifyTransportError(err);
  }
}

/**
 * Issues one JSON request and decodes its body as `T`, or throws a
 * `HeliosError` — via `classifyRestError` for a response whose status is
 * not in `okStatuses`, via `classifyTransportError` for a failure to even
 * complete the round trip.
 */
async function requestJSON<T>(
  path: string,
  init: RequestInit | undefined,
  okStatuses: readonly number[],
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", ...(init?.headers as Record<string, string>) };
  if (init?.body !== undefined && !(init.body instanceof FormData)) headers["Content-Type"] = "application/json";

  const response = await doFetch(path, { ...init, headers });
  const text = await response.text();

  if (!okStatuses.includes(response.status)) {
    throw classifyRestError(response.status, text);
  }
  return (text ? JSON.parse(text) : undefined) as T;
}

/** Issues one request and decodes its body as plain text (never JSON — `transcript`'s txt/srt/vtt formats aren't). */
async function requestText(path: string, init: RequestInit | undefined, okStatuses: readonly number[]): Promise<string> {
  const response = await doFetch(path, init);
  const text = await response.text();
  if (!okStatuses.includes(response.status)) {
    throw classifyRestError(response.status, text);
  }
  return text;
}

/**
 * Uploads an audio file for transcription via `POST /api/v1/transcriptions`
 * (multipart/form-data). `source` must be `{ kind: "file" }` — a browser
 * tab has no arbitrary local filesystem path to stream from (see
 * `../types.ts`'s `AudioSource` doc). The `options` part is appended
 * before `file` deliberately: docs/openapi.yaml's `submitTranscription`
 * requires `options` to come first when both are sent, since
 * internal/httpapi never buffers the whole request to look ahead.
 */
export function submit(source: AudioSource, options: Options): Promise<Transcription> {
  if (source.kind !== "file") {
    return Promise.reject(
      new HeliosError("invalid_input", "A browser tab needs the file's bytes, not a filesystem path — drop or choose a file instead."),
    );
  }
  const form = new FormData();
  form.append("options", JSON.stringify(options));
  form.append("file", source.file, source.file.name);
  return requestJSON<Transcription>(apiPath("/transcriptions"), { method: "POST", body: form }, [202]);
}

/** A browser tab has no native "choose a file" dialog to hand back an OS path from — the Composer's drop zone / `<input type="file">` covers this case directly, without going through this function at all. Always rejects. */
export function chooseAudioFile(): Promise<string> {
  return Promise.reject(new HeliosError("media_unavailable", "Choosing a file this way isn't available from a browser tab — drop a file onto the composer, or use its file picker."));
}

/** Lists transcriptions matching `filter`, newest first. See `app.App.List` / `GET /api/v1/transcriptions`. */
export function list(filter: ListFilter = {}): Promise<ListPage> {
  const q = new URLSearchParams();
  if (filter.library !== undefined) q.set("library", String(filter.library));
  if (filter.status) q.set("status", filter.status);
  if (filter.limit !== undefined) q.set("limit", String(filter.limit));
  if (filter.cursor) q.set("cursor", filter.cursor);

  const qs = q.toString();
  return requestJSON<ListPage>(apiPath(`/transcriptions${qs ? `?${qs}` : ""}`), undefined, [200]);
}

/** Fetches one transcription by id. See `app.App.Get` / `GET /api/v1/transcriptions/{id}`. */
export function get(id: string): Promise<Transcription> {
  return requestJSON<Transcription>(apiPath(`/transcriptions/${id}`), undefined, [200]);
}

/** Manually re-queues a failed or canceled transcription. See `app.App.Retry` / `POST /api/v1/transcriptions/{id}/retry`. */
export function retry(id: string): Promise<Transcription> {
  return requestJSON<Transcription>(apiPath(`/transcriptions/${id}/retry`), { method: "POST" }, [200]);
}

/** Puts a pending or failed transcription first in the queue and resets its retry state. See `app.App.Prioritize` / `POST /api/v1/transcriptions/{id}/prioritize`. */
export function prioritize(id: string): Promise<Transcription> {
  return requestJSON<Transcription>(apiPath(`/transcriptions/${id}/prioritize`), { method: "POST" }, [202]);
}

/** Requests that a pending or running transcription stop. See `app.App.Cancel` / `POST /api/v1/transcriptions/{id}/cancel`. */
export async function cancel(id: string): Promise<void> {
  await requestJSON<void>(apiPath(`/transcriptions/${id}/cancel`), { method: "POST" }, [200]);
}

/** Removes a transcription's record and stored files. See `app.App.Delete` / `DELETE /api/v1/transcriptions/{id}`. */
export async function deleteTranscription(id: string): Promise<void> {
  await requestJSON<void>(apiPath(`/transcriptions/${id}`), { method: "DELETE" }, [204]);
}

/** connectionCheckName mirrors internal/httpclient's own — a transport failure this adapter cannot even get a response for is surfaced as a "connection" check, not a rejection. See `health`'s doc. */
const CONNECTION_CHECK_NAME = "connection";

function unreachableReport(detail: string): HealthReport {
  return { ok: false, checks: { [CONNECTION_CHECK_NAME]: { ok: false, detail } } };
}

/**
 * Fetches `url` (a `/readyz` endpoint, same-origin or absolute) and parses
 * its body as a `HealthReport`, never rejecting: a transport failure (`err`
 * from `fetch` itself), an aborted request (`signal`, used only by
 * `testServerConnection`'s timeout below), or an unparseable body all fold
 * into the same `unreachableReport` shape a caller already knows how to
 * render. Shared by `health()` (against the effective server) and
 * `testServerConnection` (against a candidate one that may never become
 * the effective server at all) so this parsing/never-rejects behaviour
 * exists in exactly one place.
 */
async function fetchHealthReport(url: string, signal?: AbortSignal): Promise<HealthReport> {
  let response: Response;
  try {
    response = await fetch(url, { headers: { Accept: "application/json" }, signal });
  } catch (err) {
    return unreachableReport(err instanceof Error ? err.message : String(err));
  }

  let text: string;
  try {
    text = await response.text();
  } catch (err) {
    return unreachableReport(err instanceof Error ? err.message : String(err));
  }

  try {
    return JSON.parse(text) as HealthReport;
  } catch {
    return unreachableReport(`unexpected ${url} response (status ${response.status}): ${text.slice(0, 200)}`);
  }
}

/**
 * Runs every configured health check via `GET /readyz`, against the
 * currently effective server (`./serverConfig.ts`'s `apiBase()`). Never
 * rejects: `/readyz` always answers with a `HealthReport` body on both 200
 * and 503, and a transport failure or unparseable response is folded into
 * the same shape — `ok: false` with a `"connection"` check naming what
 * went wrong — rather than thrown, so `useConnection`'s
 * online/reconnecting/offline state machine (`../connection.ts`) never
 * needs a REST-specific branch.
 */
export function health(): Promise<HealthReport> {
  return fetchHealthReport(`${apiBase()}/readyz`);
}

/** Available bytes on the server volume that stores uploaded audio. */
export async function freeStorageBytes(): Promise<number> {
  const response = await requestJSON<{ freeBytes: number }>(apiPath("/storage"), undefined, [200]);
  return response.freeBytes;
}

/** Fetches the rendered transcript in one of four formats. See `app.App.Transcript` / `GET /api/v1/transcriptions/{id}/transcript`. Works from a plain browser tab — it is just a GET request. */
export function transcript(id: string, format: TranscriptFormat): Promise<string> {
  return requestText(apiPath(`/transcriptions/${id}/transcript?format=${format}`), undefined, [200]);
}

/**
 * The URL a browser tab's own "Save as…"/"Save transcript as…" navigates a
 * temporary `<a download>` to (see `../../features/library/SaveAsDialog.tsx`)
 * — there is no `destDir` a page could ever supply (see `saveTranscript`
 * below), so downloading through the browser's own download manager is
 * this adapter's equivalent. `download=1` is what makes
 * `internal/httpapi`'s `getTranscript` send `Content-Disposition:
 * attachment` for every format, `json` included (which otherwise renders
 * inline) — the same header `transcript()`'s txt/srt/vtt requests above
 * already get without it. `id` is URL-encoded defensively; transcription
 * ids are server-generated UUIDs today, never user input, but a path
 * segment built by string interpolation should not assume that stays true.
 */
export function transcriptDownloadUrl(id: string, format: TranscriptFormat): string {
  return apiPath(`/transcriptions/${encodeURIComponent(id)}/transcript?format=${encodeURIComponent(format)}&download=1`);
}

/**
 * The URL `DetailPanel`'s "Download audio" browser-tab action (see
 * `../../features/library/DetailPanel.tsx`'s `MediaActions`) points an
 * `<a download>` at — the exact same URL its `<audio>` element already
 * uses as `src`, since `internal/httpapi`'s `getAudio` already sends
 * `Content-Disposition: attachment` unconditionally (see that handler's
 * own doc) and needs no `download=1` query param the way the transcript
 * endpoint does.
 */
export function audioDownloadUrl(id: string): string {
  return apiPath(`/transcriptions/${encodeURIComponent(id)}/audio`);
}

/** A browser tab has no arbitrary local folder to write into — there is no `destDir` a page could ever supply. Always rejects; `transcriptDownloadUrl` above (a real browser download) is the browser-tab equivalent. */
export function saveTranscript(_id: string, _format: TranscriptFormat, _destDir: string): Promise<string> {
  return unsupportedInBrowser("Saving the transcript to a folder");
}

/**
 * No browser tab has local filesystem access to a transcription's real
 * source audio file — there is no path here comparable to the desktop
 * process' media access at all, not even a sometimes-unavailable one — so
 * this always resolves `exists: false` without a network round trip.
 */
export function audioLocation(_id: string): Promise<MediaLocation> {
  return Promise.resolve({ exists: false });
}

/** A `HeliosError` for a capability a browser tab has no equivalent of at all. */
function unsupportedInBrowser(action: string): Promise<never> {
  return Promise.reject(
    new HeliosError("media_unavailable", `${action} is not available from a browser tab — open HELIOS-ATS from the desktop app instead.`),
  );
}

/** A web page cannot ask the OS to open a file with its default handler. Rejects with `"media_unavailable"` — the exact code `audioLocation`'s `exists: false` already trains the UI to expect from this action. `DetailPanel`'s `MediaActions` never actually calls this from a browser tab (it renders `audioDownloadUrl` as a plain download link there instead), but the rejection stays correct as a fallback contract. */
export function openAudio(_id: string): Promise<void> {
  return unsupportedInBrowser("Opening the file");
}

/** A web page cannot open the host OS's file manager. Same `"media_unavailable"` rejection, and the same `DetailPanel` note, as `openAudio`. */
export function revealInFileManager(_id: string): Promise<void> {
  return unsupportedInBrowser("Revealing the file");
}

/** A web page has no native "choose a folder" dialog. Always rejects. */
export function chooseSaveDirectory(): Promise<string> {
  return unsupportedInBrowser("Choosing a folder");
}

/** A browser tab has no notion of the host OS's downloads directory — or any local filesystem path at all — to suggest. Resolves to `""` rather than rejecting, exactly like `../wails/api.ts`'s own "could not resolve one" case. */
export function defaultSaveDirectory(): Promise<string> {
  return Promise.resolve("");
}

/**
 * Reports the server this browser tab currently talks to — same-origin by
 * default, or whatever `./serverConfig.ts`'s `localStorage` entry says
 * once Settings → Server has saved one. Always `editable: true`: unlike
 * `../wails/api.ts`'s "local" mode, a browser tab always has *some* origin
 * it could instead point at another one — see `./serverConfig.ts`'s own
 * doc. Never rejects, mirroring `desktop/serversettings.go`'s
 * `GetServerSettings` (reading `localStorage` cannot fail in a way worth
 * surfacing any differently than "nothing saved" — see
 * `serverConfig.ts`'s `readStored`).
 */
export function getServerSettings(): Promise<ServerSettings> {
  return Promise.resolve(currentSettings());
}

/**
 * Validates host/port (`./serverConfig.ts`'s `setServer`, which mirrors
 * `desktop/serversettings.go`'s `cleanServerHost`/`validateServerPort`
 * exactly), persists them, and returns the new effective settings. Every
 * REST call above picks up the new `apiBase()` on its very next request,
 * since none of them cache it — there is no live event stream to move
 * onto the new server the way the sibling ingestion service's `reconnect()`
 * would (this service has none at all — `lib/store.ts`'s own doc); the
 * polling loop it drives instead picks up the new server on its own very
 * next tick, the same "no restart, applies live" contract
 * `desktop/serversettings.go`'s `SetServerSettings` documents for the
 * Wails adapter. Rejects with a `HeliosError` (`code: "invalid_input"`)
 * for a malformed host/port, or one this page's own mixed-content policy
 * would block outright (see `serverConfig.ts`'s `mixedContentError`) —
 * never partially saves.
 */
export async function setServerSettings(host: string, port: number): Promise<ServerSettings> {
  return setServer(host, port);
}

/** How long `testServerConnection` waits for `/readyz` to answer before giving up — mirrors `desktop/serversettings.go`'s `testConnectionTimeout` (same value, same reasoning: long enough for a real round trip, short enough that a silently-dropping port reads as "unreachable" within a few seconds, not "hung"). */
const TEST_CONNECTION_TIMEOUT_MS = 5000;

/**
 * Checks whether `host:port` answers `GET /readyz`, without saving
 * anything — mirrors `desktop/serversettings.go`'s `TestServerConnection`:
 * a malformed host/port (or one mixed-content policy would block) rejects
 * with a `HeliosError` exactly like `setServerSettings` would, the same
 * "call this only after validating, or expect the same rejection" contract
 * `../api.ts`'s own doc states; an unreachable server never rejects,
 * coming back instead as `{ ok: false, ... }` with a `"connection"` check,
 * `fetchHealthReport`'s ordinary contract. Two additions beyond a plain
 * `fetchHealthReport` call: on a timeout specifically (as opposed to a
 * connection refused or a DNS failure, which resolve well within
 * `TEST_CONNECTION_TIMEOUT_MS`), the check's detail is replaced with a
 * message naming the host/port actually tested and how long this page
 * waited, rather than the raw, uninformative `AbortError` a bare timeout
 * would otherwise surface as; and on any other transport failure against a
 * cross-origin candidate, a short CORS hint is appended — `fetch` rejects
 * with the exact same opaque `TypeError` for "nothing is listening there"
 * and for "something is listening, but its CORS policy rejects this page's
 * origin", and this page has no way to tell the two apart itself.
 */
export async function testServerConnection(host: string, port: number): Promise<HealthReport> {
  const candidate = validateCandidate(host, port);
  const url = `${candidate.scheme}://${candidate.host}:${candidate.port}/readyz`;

  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, TEST_CONNECTION_TIMEOUT_MS);

  try {
    const report = await fetchHealthReport(url, controller.signal);
    if (timedOut) {
      return unreachableReport(
        `no response from ${candidate.host}:${candidate.port} after ${TEST_CONNECTION_TIMEOUT_MS / 1000}s — check the port, or whether the server is behind a reverse proxy (e.g. https on 443)`,
      );
    }
    return maybeHintCORS(report, candidate);
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Appends a short CORS hint to `report`'s connection check's detail when
 * it looks like a genuine transport failure (`fetchHealthReport`'s own
 * `fetch()`-threw path — see its doc; excludes both the timeout case,
 * already handled by `testServerConnection`'s own caller before this ever
 * runs, and an "unexpected ... response" body-parse failure, which means a
 * response — from *something* — was received, so CORS cannot be why)
 * against a `candidate` this page's origin actually differs from — see
 * `./serverConfig.ts`'s `isCrossOrigin` doc for why a same-origin one is
 * skipped outright. `report` itself is otherwise returned unchanged.
 */
function maybeHintCORS(report: HealthReport, candidate: ReturnType<typeof validateCandidate>): HealthReport {
  if (report.ok || !isCrossOrigin(candidate)) return report;
  const check = report.checks[CONNECTION_CHECK_NAME];
  if (!check || check.ok || check.detail.startsWith("unexpected ")) return report;
  return unreachableReport(`${check.detail} — unreachable, or it does not allow this origin (see HELIOS_CORS_ALLOWED_ORIGINS on the target)`);
}
