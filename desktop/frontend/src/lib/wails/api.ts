import * as Bound from "../../../wailsjs/go/main/App";
import { HeliosError, parseError } from "../errors";
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

/*
 * Thin, typed wrapper over the generated Wails bindings in
 * `wailsjs/go/main/App`. This is the *only* module that imports from
 * `wailsjs/**` — `../api.ts` picks this implementation or `../browser/api.ts`
 * at call time (see `../runtime.ts`) and re-exports whichever one under the
 * same names, so every feature screen keeps importing from `./lib`'s barrel
 * and never needs to know which adapter is live.
 *
 * Every function here:
 * - takes and returns the plain types from `../types`, not the generated
 *   `main.*` classes (those still work structurally as plain objects on
 *   the way in; on the way out this module reads their fields into plain
 *   objects so nothing downstream needs `wailsjs` imports or `instanceof`
 *   checks against generated classes);
 * - converts a rejection into a `HeliosError` (see `../errors`) and
 *   re-throws that instead of the raw Wails rejection, so every caller can
 *   `catch (e) { if (e instanceof HeliosError) ... }` uniformly.
 */

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn();
  } catch (err) {
    throw parseError(err);
  }
}

/**
 * Casts a plain wire-shaped object to a generated binding's expected
 * argument type. The generated `main.*DTO` classes carry instance methods
 * (`convertValues`, ...) that a plain object literal never has, so a
 * direct `as` would fail TS's structural overlap check even though the
 * Wails-generated JS underneath (`wailsjs/go/main/App.js`) does nothing
 * but forward the plain object to `window.go...`. Routing through
 * `unknown` is the deliberate, narrow way to say "this is a wire shape,
 * not a real instance" at exactly this one boundary.
 */
function asArg<T>(value: unknown): T {
  return value as unknown as T;
}

/** Uploads an audio file for transcription. See `app.App.Submit`. `source` must be `{ kind: "path" }` — a Wails window has no `File` object to hand a Go binding at all, see `../types.ts`'s `AudioSource` doc. */
export function submit(source: AudioSource, options: Options): Promise<Transcription> {
  if (source.kind !== "path") {
    return Promise.reject(new HeliosError("invalid_input", "The desktop app needs a chosen file path, not raw file bytes."));
  }
  return call(() => Bound.Submit(asArg({ filePath: source.path, options }))) as unknown as Promise<Transcription>;
}

/** Opens the native "choose an audio file" dialog. Resolves `""` if the user cancels. */
export function chooseAudioFile(): Promise<string> {
  return call(() => Bound.ChooseAudioFile());
}

/** Lists transcriptions matching `filter`, newest first. See `app.App.List`. */
export function list(filter: ListFilter = {}): Promise<ListPage> {
  return call(() =>
    Bound.List(
      asArg({
        library: filter.library,
        status: filter.status,
        cursor: filter.cursor,
        limit: filter.limit,
      }),
    ),
  ) as unknown as Promise<ListPage>;
}

/** Fetches one transcription by id. See `app.App.Get`. */
export function get(id: string): Promise<Transcription> {
  return call(() => Bound.Get(id)) as unknown as Promise<Transcription>;
}

/** Manually re-queues a failed or canceled transcription. See `app.App.Retry`. */
export function retry(id: string): Promise<Transcription> {
  return call(() => Bound.Retry(id)) as unknown as Promise<Transcription>;
}

/** Puts a pending or failed transcription first in the queue and resets its retry state. See `app.App.Prioritize`. */
export function prioritize(id: string): Promise<Transcription> {
  return call(() => Bound.Prioritize(id)) as unknown as Promise<Transcription>;
}

/** Requests that a pending or running transcription stop. See `app.App.Cancel`. */
export function cancel(id: string): Promise<void> {
  return call(() => Bound.Cancel(id));
}

/** Removes a transcription's record and stored files. See `app.App.Delete`. */
export function deleteTranscription(id: string): Promise<void> {
  return call(() => Bound.Delete(id));
}

/** Runs every configured health check (`/readyz`'s checks, disk space included). */
export function health(): Promise<HealthReport> {
  return call(() => Bound.Health()) as unknown as Promise<HealthReport>;
}

/** Available bytes on the connected server's audio storage volume. */
export function freeStorageBytes(): Promise<number> {
  return call(() => Bound.FreeStorageBytes());
}

/** Fetches the rendered transcript in one of four formats. See `app.App.Transcript`. */
export function transcript(id: string, format: TranscriptFormat): Promise<string> {
  return call(() => Bound.Transcript(id, format));
}

/** Writes `<destDir>/<audio stem>-transcription.<format>` and returns the path written. See `app.App.SaveTranscript`. */
export function saveTranscript(id: string, format: TranscriptFormat, destDir: string): Promise<string> {
  return call(() => Bound.SaveTranscript(id, format, destDir));
}

/** Reports whether this desktop process has local filesystem access to the transcription's source audio file. See `app.App.AudioLocation`. */
export function audioLocation(id: string): Promise<MediaLocation> {
  return call(() => Bound.AudioLocation(id)) as unknown as Promise<MediaLocation>;
}

/** Opens the transcription's source audio file, in place, with the OS default handler for its type. See `app.App.OpenAudio`. */
export function openAudio(id: string): Promise<void> {
  return call(() => Bound.OpenAudio(id));
}

/** Reveals the transcription's source audio file's containing folder. See `app.App.RevealInFileManager`. */
export function revealInFileManager(id: string): Promise<void> {
  return call(() => Bound.RevealInFileManager(id));
}

/** Opens a native "choose a folder" dialog. Resolves `""` if the user cancels. See `app.App.ChooseSaveDirectory`. */
export function chooseSaveDirectory(): Promise<string> {
  return call(() => Bound.ChooseSaveDirectory());
}

/** Resolves the user's system downloads directory. Resolves `""`, never a rejection, when this process could not find one. See `app.App.DefaultSaveDirectory`. */
export function defaultSaveDirectory(): Promise<string> {
  return call(() => Bound.DefaultSaveDirectory());
}

/** Reports the backend server host/port this process talks to right now. See `desktop/serversettings.go`'s `GetServerSettings`. */
export function getServerSettings(): Promise<ServerSettings> {
  return call(() => Bound.GetServerSettings()) as unknown as Promise<ServerSettings>;
}

/** Validates, persists and applies a new host/port live. See `desktop/serversettings.go`'s `SetServerSettings`. */
export function setServerSettings(host: string, port: number): Promise<ServerSettings> {
  return call(() => Bound.SetServerSettings(host, port)) as unknown as Promise<ServerSettings>;
}

/** Checks whether `host:port` answers, without changing the live server. See `desktop/serversettings.go`'s `TestServerConnection`. */
export function testServerConnection(host: string, port: number): Promise<HealthReport> {
  return call(() => Bound.TestServerConnection(host, port)) as unknown as Promise<HealthReport>;
}
