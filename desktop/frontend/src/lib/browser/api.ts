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

/*
 * REST implementation of the same adapter surface as `../wails/api.ts`,
 * talking to the API `internal/httpapi` serves (docs/openapi.yaml) instead
 * of a bound Wails method. `../api.ts` picks this implementation or
 * `../wails/api.ts` at call time (see `../runtime.ts`) and re-exports
 * whichever one under the same names.
 *
 * Every request is a plain, same-origin, relative path — `/api/v1/...` or
 * `/readyz` — never an absolute URL. In production this only runs inside a
 * plain browser tab pointed at a Helios deployment that serves this build
 * and the API from the same origin; in development, `vite.config.ts`
 * proxies both prefixes to the API so no CORS configuration is ever
 * needed on the Go service.
 *
 * The wire DTOs in `internal/httpapi/dto.go` were deliberately built
 * field-for-field identical to `../types.ts` (same camelCase names, same
 * null-vs-omitted handling) precisely so this module never needs its own
 * parallel DTO layer: a parsed response body already IS a
 * `Transcription`/`ListPage`/`HealthReport`.
 */

const API_BASE = "/api/v1";

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
  return requestJSON<Transcription>(`${API_BASE}/transcriptions`, { method: "POST", body: form }, [202]);
}

/** A browser tab has no native "choose a file" dialog to hand back an OS path from — the Composer's drop zone / `<input type="file">` covers this case directly, without going through this function at all. Always rejects. */
export function chooseAudioFile(): Promise<string> {
  return Promise.reject(new HeliosError("media_unavailable", "Choosing a file this way isn't available from a browser tab — drop a file onto the composer, or use its file picker."));
}

/** Lists transcriptions matching `filter`, newest first. See `app.App.List` / `GET /api/v1/transcriptions`. */
export function list(filter: ListFilter = {}): Promise<ListPage> {
  const q = new URLSearchParams();
  if (filter.status) q.set("status", filter.status);
  if (filter.limit !== undefined) q.set("limit", String(filter.limit));
  if (filter.cursor) q.set("cursor", filter.cursor);

  const qs = q.toString();
  return requestJSON<ListPage>(`${API_BASE}/transcriptions${qs ? `?${qs}` : ""}`, undefined, [200]);
}

/** Fetches one transcription by id. See `app.App.Get` / `GET /api/v1/transcriptions/{id}`. */
export function get(id: string): Promise<Transcription> {
  return requestJSON<Transcription>(`${API_BASE}/transcriptions/${id}`, undefined, [200]);
}

/** Manually re-queues a failed or canceled transcription. See `app.App.Retry` / `POST /api/v1/transcriptions/{id}/retry`. */
export function retry(id: string): Promise<Transcription> {
  return requestJSON<Transcription>(`${API_BASE}/transcriptions/${id}/retry`, { method: "POST" }, [200]);
}

/** Requests that a pending or running transcription stop. See `app.App.Cancel` / `POST /api/v1/transcriptions/{id}/cancel`. */
export async function cancel(id: string): Promise<void> {
  await requestJSON<void>(`${API_BASE}/transcriptions/${id}/cancel`, { method: "POST" }, [200]);
}

/** Removes a transcription's record and stored files. See `app.App.Delete` / `DELETE /api/v1/transcriptions/{id}`. */
export async function deleteTranscription(id: string): Promise<void> {
  await requestJSON<void>(`${API_BASE}/transcriptions/${id}`, { method: "DELETE" }, [204]);
}

/** connectionCheckName mirrors internal/httpclient's own — a transport failure this adapter cannot even get a response for is surfaced as a "connection" check, not a rejection. See `health`'s doc. */
const CONNECTION_CHECK_NAME = "connection";

function unreachableReport(detail: string): HealthReport {
  return { ok: false, checks: { [CONNECTION_CHECK_NAME]: { ok: false, detail } } };
}

/**
 * Runs every configured health check via `GET /readyz`. Never rejects:
 * `/readyz` always answers with a `HealthReport` body on both 200 and 503,
 * and a transport failure or unparseable response is folded into the same
 * shape — `ok: false` with a `"connection"` check naming what went wrong —
 * rather than thrown, so `useConnection`'s online/reconnecting/offline
 * state machine (`../connection.ts`) never needs a REST-specific branch.
 */
export async function health(): Promise<HealthReport> {
  let response: Response;
  try {
    response = await fetch("/readyz", { headers: { Accept: "application/json" } });
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
    return unreachableReport(`unexpected /readyz response (status ${response.status}): ${text.slice(0, 200)}`);
  }
}

/** Fetches the rendered transcript in one of four formats. See `app.App.Transcript` / `GET /api/v1/transcriptions/{id}/transcript`. Works from a plain browser tab — it is just a GET request. */
export function transcript(id: string, format: TranscriptFormat): Promise<string> {
  return requestText(`${API_BASE}/transcriptions/${id}/transcript?format=${format}`, undefined, [200]);
}

/** A browser tab has no arbitrary local folder to write into — there is no `destDir` a page could ever supply. Always rejects; the format switcher's "Copy" action is the browser-tab equivalent. */
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

/** A web page cannot ask the OS to open a file with its default handler. Rejects with `"media_unavailable"` — the exact code `audioLocation`'s `exists: false` already trains the UI to expect from this action. */
export function openAudio(_id: string): Promise<void> {
  return unsupportedInBrowser("Opening the file");
}

/** A web page cannot open the host OS's file manager. Same `"media_unavailable"` rejection as `openAudio`. */
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
 * A browser tab is already served from, and talking to, the one origin
 * `API_BASE`'s own doc describes — there is no separate "which server"
 * setting for it to report, editable or not. Resolves `editable: false`
 * without a network round trip, the same "nothing to ask, so don't ask"
 * posture `audioLocation` takes above.
 */
export function getServerSettings(): Promise<ServerSettings> {
  return Promise.resolve({ host: "", port: 0, baseUrl: "", editable: false });
}

/** A `HeliosError` for the Server setting, which a browser tab has no equivalent of at all — see `getServerSettings`'s doc. */
function unsupportedServerSetting(): Promise<never> {
  return Promise.reject(new HeliosError("invalid_input", "The server address cannot be changed from a browser tab."));
}

/** Always rejects — see `getServerSettings`'s doc; there is nothing here to set. */
export function setServerSettings(_host: string, _port: number): Promise<ServerSettings> {
  return unsupportedServerSetting();
}

/** Always rejects — same reasoning as `setServerSettings`. */
export function testServerConnection(_host: string, _port: number): Promise<HealthReport> {
  return unsupportedServerSetting();
}
