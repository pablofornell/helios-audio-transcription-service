import * as browserApi from "./browser/api";
import { hasWailsRuntime } from "./runtime";
import { bumpServerSettingsVersion } from "./serverSettingsSignal";
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
} from "./types";
import * as wailsApi from "./wails/api";

/*
 * Picks the API adapter at call time: `./wails/api.ts` (the generated
 * Wails bindings in `wailsjs/go/main/App`) when running inside the Wails
 * shell, `./browser/api.ts` (a REST client against `internal/httpapi`)
 * otherwise — see `./runtime.ts` for the detection this is built on.
 *
 * This is the *only* file every feature screen (and everything in
 * `src/components`) is meant to import these functions from — directly, or
 * via the barrel (`./index.ts`) — so that boundary never has to know or
 * care which transport is actually live. Both adapters are structurally
 * identical (same function names, same parameter and return types); this
 * module is nothing but the `hasWailsRuntime() ? wailsApi : browserApi`
 * choice, made once per call so a test can flip the Wails-runtime
 * detection between calls without needing to reset/re-import this module.
 *
 * There is deliberately no `./events.ts` sibling here: this service has no
 * `/api/v1/events` SSE endpoint (see DESKTOP.md's binding contract) — the
 * live view is `./store.ts` polling `list()` instead.
 */

function impl() {
  return hasWailsRuntime() ? wailsApi : browserApi;
}

/** Uploads an audio file for transcription. See `./types.ts`'s `AudioSource` doc for the Wails/browser split `source` carries. */
export function submit(source: AudioSource, options: Options): Promise<Transcription> {
  return impl().submit(source, options);
}

/** Opens the native "choose an audio file" dialog. Resolves `""` if the user cancels. Rejects `"media_unavailable"` in a browser tab, which has no such dialog — use the drop zone or a plain `<input type="file">` there instead. */
export function chooseAudioFile(): Promise<string> {
  return impl().chooseAudioFile();
}

/** Lists transcriptions matching `filter`, newest first. See `app.App.List`. */
export function list(filter: ListFilter = {}): Promise<ListPage> {
  return impl().list(filter);
}

/** Fetches one transcription by id. See `app.App.Get`. */
export function get(id: string): Promise<Transcription> {
  return impl().get(id);
}

/** Manually re-queues a failed or canceled transcription. See `app.App.Retry`. */
export function retry(id: string): Promise<Transcription> {
  return impl().retry(id);
}

/** Requests that a pending or running transcription stop. See `app.App.Cancel`. */
export function cancel(id: string): Promise<void> {
  return impl().cancel(id);
}

/** Removes a transcription's record and stored files. See `app.App.Delete`. */
export function deleteTranscription(id: string): Promise<void> {
  return impl().deleteTranscription(id);
}

/** Runs every configured health check (`/readyz`'s checks, disk space included). */
export function health(): Promise<HealthReport> {
  return impl().health();
}

/** Available bytes on the connected server's audio storage volume. */
export function freeStorageBytes(): Promise<number> {
  return impl().freeStorageBytes();
}

/** Fetches the rendered transcript in one of four formats (txt/srt/vtt/json) as text. Only legal once `status: "succeeded"`. See `app.App.Transcript`. */
export function transcript(id: string, format: TranscriptFormat): Promise<string> {
  return impl().transcript(id, format);
}

/** Writes `<destDir>/<audio stem>-transcription.<format>` and returns the path written. `destDir` must already exist — obtain it from the user (e.g. `chooseSaveDirectory`), do not invent one. See `app.App.SaveTranscript`. */
export function saveTranscript(id: string, format: TranscriptFormat, destDir: string): Promise<string> {
  return impl().saveTranscript(id, format, destDir);
}

/**
 * Reports whether this process has local filesystem access to the
 * transcription's source audio file, and if so, its real, absolute path.
 * Use this to decide whether to offer `openAudio`/`revealInFileManager` at
 * all (disable them when `exists` is false) rather than reacting to the
 * rejection they'd otherwise throw.
 */
export function audioLocation(id: string): Promise<MediaLocation> {
  return impl().audioLocation(id);
}

/** Opens the transcription's source audio file, in place, with the OS default handler for its type. Rejects `"media_unavailable"` when this process has no local filesystem access to it (see `audioLocation`). */
export function openAudio(id: string): Promise<void> {
  return impl().openAudio(id);
}

/** Reveals the transcription's source audio file's containing folder. Same `"media_unavailable"` rejection as `openAudio`. */
export function revealInFileManager(id: string): Promise<void> {
  return impl().revealInFileManager(id);
}

/** Opens a native "choose a folder" dialog for `saveTranscript`'s `destDir`. Resolves `""` if the user cancels. Rejects `"media_unavailable"` in a browser tab. */
export function chooseSaveDirectory(): Promise<string> {
  return impl().chooseSaveDirectory();
}

/** Resolves the user's system downloads directory, to pre-fill a save dialog. Resolves `""`, never a rejection, when there is no sensible default. */
export function defaultSaveDirectory(): Promise<string> {
  return impl().defaultSaveDirectory();
}

/** Reports the backend server host/port this process talks to right now. See `./types.ts`'s `ServerSettings` doc. */
export function getServerSettings(): Promise<ServerSettings> {
  return impl().getServerSettings();
}

/**
 * Validates host/port, persists them, and repoints every subsequent API
 * call at the new server immediately — no restart needed. Rejects with a
 * `HeliosError` whose `code` is `"invalid_input"` for a malformed host or
 * out-of-range port; call this only when `getServerSettings().editable` is
 * true, since neither adapter can honour it otherwise.
 *
 * Also bumps `./serverSettingsSignal.ts`'s version, once, right after a
 * successful save — the one hook the rest of the app has to notice this
 * happened without re-polling `getServerSettings()` on a timer.
 */
export async function setServerSettings(host: string, port: number): Promise<ServerSettings> {
  const settings = await impl().setServerSettings(host, port);
  bumpServerSettingsVersion();
  return settings;
}

/**
 * Checks whether `host:port` answers, without changing which server this
 * process actually talks to — for a "Test connection" action before
 * committing via `setServerSettings`. Never rejects for an unreachable
 * server (mirrors `health`'s own contract).
 */
export function testServerConnection(host: string, port: number): Promise<HealthReport> {
  return impl().testServerConnection(host, port);
}
