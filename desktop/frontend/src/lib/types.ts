/*
 * Domain types for the desktop frontend, re-expressed as plain TS unions
 * and interfaces over the generated `wailsjs/go/models.ts` classes.
 *
 * Why not use the generated `main.*` classes directly everywhere: they are
 * fine as wire-shape input to `JSON.parse`/`createFrom`, but their fields
 * are typed as `any`/loose strings in several places (enums, `Go type:
 * time` fields are `any`), and every one of the generated classes must be
 * `new`'d to get its `convertValues` behaviour. The rest of the app —
 * everything importing from `src/lib` or `src/components` — should never
 * need to import from `wailsjs/**` directly; that boundary is `src/lib/api.ts`
 * alone (there is no `events.ts` here — see this module's own doc on why:
 * DESKTOP.md's binding contract has no SSE endpoint for this service).
 */

// ---------------------------------------------------------------------------
// Enums — mirror internal/domain field for field (see internal/domain/status.go,
// task.go, options.go, errors.go). Values are the exact wire strings; do not
// rename without checking the Go side.
// ---------------------------------------------------------------------------

/** The five API-returned lifecycle states of a Transcription (internal/domain/status.go). */
export type Status = "pending" | "running" | "succeeded" | "failed" | "canceled";

/**
 * Client-only states, layered on top of `Status`. Declared as a separate
 * union so nothing mistakes them for a `Status` the API can actually
 * return.
 *
 * - `canceling`: cancel has been requested optimistically but the
 *   `canceled` status has not yet been confirmed by the next poll.
 */
export type ClientStatus = "canceling";

/** `Status` plus the client-only states, for anything that renders a StatusPill. */
export type DisplayStatus = Status | ClientStatus;

/** Mirrors internal/domain.Task — what whisper-cli produces for a job. */
export type Task = "transcribe" | "translate";

/** Every transcript rendering this service's `/transcript` endpoint supports (docs/openapi.yaml's `TranscriptFormat`). */
export type TranscriptFormat = "txt" | "srt" | "vtt" | "json";

/**
 * Every `errorCode` in docs/openapi.yaml (internal/domain/errors.go), plus
 * the desktop-only "unknown" fallback `lib/errors.ts` uses when a rejection
 * cannot be parsed as the envelope at all.
 */
export type ErrorCode =
  | "invalid_input"
  | "unsupported_media"
  | "no_audio_stream"
  | "payload_too_large"
  | "not_found"
  | "conflict"
  | "disk_full"
  | "model_unavailable"
  | "tool_failure"
  | "timeout"
  | "canceled"
  | "internal"
  /**
   * Desktop-only (declared in `desktop/reveal.go`, not `internal/domain`):
   * `OpenAudio`/`RevealInFileManager` return this when the desktop process
   * has no local filesystem access to the transcription's source audio (no
   * media root configured, or the file is missing under it). The UI should
   * disable both actions rather than treating this like any other row
   * failure.
   */
  | "media_unavailable";

// ---------------------------------------------------------------------------
// Resource shapes — mirror desktop's *DTO Go structs, which themselves
// mirror internal/httpapi/dto.go and docs/openapi.yaml. Field names match
// the JSON tags exactly (camelCase, as sent over the wire). Date/time
// fields are ISO 8601 strings, exactly as JSON.parse leaves them — see
// `lib/format.ts` for the parsing + relative-time helpers callers should
// use to render one.
// ---------------------------------------------------------------------------

/** Mirrors internal/domain.Options field for field. */
export interface Options {
  /** Whisper model name, e.g. "base", "large-v3". Empty means the configured default. */
  model: string;
  /** ISO-639-1/639-2 code. Empty means auto-detect. */
  language: string;
  task: Task;
  wordTimestamps: boolean;
  initialPrompt: string;
  beamSize: number;
  temperature: number;
}

/** Mirrors internal/domain.Audio field for field. Everything past `sizeBytes` is zero/"" until the worker's ffprobe step has run. */
export interface Audio {
  filename: string;
  mediaType: string;
  sizeBytes: number;
  durationSeconds: number;
  sampleRateHz: number;
  channels: number;
  codec: string;
}

/** Mirrors internal/domain.Word field for field — one token merged into a whole word, only present when Options.wordTimestamps was set. */
export interface Word {
  word: string;
  start: number;
  end: number;
  probability: number;
}

/** Mirrors internal/domain.Segment field for field. */
export interface Segment {
  id: number;
  start: number;
  end: number;
  text: string;
  words?: Word[];
}

/** Mirrors internal/domain.Result field for field — the finished transcript of a succeeded job. */
export interface Result {
  text: string;
  language: string;
  durationSeconds: number;
  model: string;
  task: Task;
  segments: Segment[];
}

export interface ApiError {
  code: ErrorCode;
  message: string;
  details?: Record<string, unknown>;
}

/**
 * The central resource. Mirrors internal/httpapi/dto.go's `transcriptionDTO`
 * (and docs/openapi.yaml's `Transcription` schema) exactly.
 */
export interface Transcription {
  id: string;
  status: Status;
  options: Options;
  audio: Audio;
  result?: Result | null;
  error?: ApiError | null;
  attempts: number;
  maxAttempts: number;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  completedAt?: string;
}

/**
 * `transcription.status`, reclassified as a `DisplayStatus` for anything
 * that feeds a StatusPill. This is the one place `canceling` gets folded
 * in — do not re-derive this test elsewhere.
 */
export function displayStatus(transcription: Transcription, canceling = false): DisplayStatus {
  if (canceling) return "canceling";
  return transcription.status;
}

/**
 * What the Composer hands `submit()` — the one place the Wails/browser
 * split shows up above `api.ts`'s transport fork. The Wails bound
 * `Submit` takes an absolute local path (`SubmitRequestDTO.FilePath`): the
 * desktop process reads the file itself, so a native "choose a file"
 * dialog (`ChooseAudioFile()`) or a drag-and-drop drop event that resolves
 * to a real OS path is the only thing it can accept. A browser tab has no
 * such path — a drop event or `<input type="file">` there hands back a
 * `File`'s bytes directly, which `browser/api.ts` uploads as a multipart
 * part instead. `wails/api.ts` rejects a `"file"` source and `browser/api.ts`
 * rejects a `"path"` source, so a caller that gets this wrong fails loudly
 * rather than silently doing nothing.
 */
export type AudioSource = { kind: "path"; path: string; name: string } | { kind: "file"; file: File };

export interface SubmitRequest {
  source: AudioSource;
  options: Options;
}

export interface ListFilter {
  status?: Status;
  cursor?: string;
  limit?: number;
}

export interface ListPage {
  items: Transcription[];
  nextCursor?: string;
}

/**
 * Result of `audioLocation(id)` — whether this desktop process has local
 * filesystem access to a transcription's source audio file, and if so, its
 * real, absolute path. `path` is only meaningful when `exists` is true.
 */
export interface MediaLocation {
  path?: string;
  exists: boolean;
}

export interface CheckResult {
  freeBytes?: number;
  totalBytes?: number;
  ok: boolean;
  detail: string;
}

export interface HealthReport {
  ok: boolean;
  checks: Record<string, CheckResult>;
}

/**
 * Result of `getServerSettings()`/`setServerSettings()` — the backend host
 * and port this process talks to (or would, if `editable`), mirroring
 * `desktop/serversettings.go`'s `ServerSettingsDTO` field for field.
 * `baseUrl` is the exact base URL actually being dialled — not a
 * `http://host:port` reconstruction on this side, so it can never disagree
 * with reality (an https target, or one behind a path prefix, is shown
 * exactly as it is). `editable` is false only in "local" mode
 * (`desktop/mode.go`'s `HELIOS_MODE`) — a plain browser tab
 * (`./browser/serverConfig.ts`) is always editable, defaulting to
 * same-origin. The Settings dialog's Server section disables its fields
 * and explains why whenever this is false, rather than letting a save
 * attempt fail. `warning`, when
 * non-empty, explains why a saved value on disk could not be used at
 * startup (see `GetServerSettings`'s own doc) — shown once and gone only
 * once a save actually succeeds.
 */
export interface ServerSettings {
  host: string;
  port: number;
  baseUrl: string;
  editable: boolean;
  warning?: string;
}
