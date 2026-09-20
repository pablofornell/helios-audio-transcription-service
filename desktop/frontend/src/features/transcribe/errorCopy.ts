import type { ErrorCode } from "../../lib";

/*
 * Error code -> user-facing copy for the Transcribe queue, transcribed from
 * internal/domain/errors.go's own doc comments (and `ErrorCode.Retryable`
 * for the "Retry?" column). A failed row must say *why* in language a
 * person can act on, never the raw enum value.
 *
 * `tone` drives colour: `amber` is the running/amber tint for "this is
 * transient, retrying might just work", `red` is reserved for everything
 * else that failed. `retryable` mirrors `domain.ErrorCode.Retryable`
 * exactly — true only for tool_failure and timeout, the two ErrorCodes
 * this API's wire enum actually includes out of that function's three
 * (its third, `network`, is classified only on the Go http client's own
 * side and never appears in a stored `Transcription.error`).
 */
export type ErrorTone = "neutral" | "amber" | "red";

export interface ErrorCopy {
  headline: string;
  tone: ErrorTone;
  retryable: boolean;
}

const ERROR_COPY: Record<ErrorCode, ErrorCopy> = {
  invalid_input: { headline: "The request was rejected as invalid", tone: "neutral", retryable: false },
  unsupported_media: { headline: "whisper.cpp's decoder couldn't read this file", tone: "neutral", retryable: false },
  no_audio_stream: { headline: "This file has no audio track", tone: "neutral", retryable: false },
  payload_too_large: { headline: "File too large or too long", tone: "neutral", retryable: false },
  not_found: { headline: "This transcription is gone", tone: "neutral", retryable: false },
  conflict: { headline: "This item changed elsewhere", tone: "neutral", retryable: false },
  disk_full: { headline: "Out of disk space on the server", tone: "red", retryable: false },
  model_unavailable: { headline: "The requested whisper model isn't installed", tone: "red", retryable: false },
  tool_failure: { headline: "whisper.cpp failed on this one", tone: "amber", retryable: true },
  timeout: { headline: "Took too long and was stopped", tone: "amber", retryable: true },
  // Never reach a row per docs/openapi.yaml's table (handled inline at
  // submit, or by the row's own client-only state) — copy is still
  // provided so a surprising value never renders as a raw enum string.
  canceled: { headline: "Canceled", tone: "neutral", retryable: false },
  internal: { headline: "Something broke inside HELIOS-ATS", tone: "red", retryable: false },
  media_unavailable: { headline: "No local copy of this file is available", tone: "neutral", retryable: false },
};

const FALLBACK: ErrorCopy = { headline: "Something went wrong", tone: "red", retryable: false };

/** Looks up headline/tone/retryability for a transcription's `error.code`, falling back gracefully for an unrecognised or absent code. */
export function errorCopyFor(code: ErrorCode | null | undefined): ErrorCopy {
  if (!code) return FALLBACK;
  return ERROR_COPY[code] ?? FALLBACK;
}
