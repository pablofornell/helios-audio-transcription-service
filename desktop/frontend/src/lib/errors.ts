import type { ErrorCode } from "./types";

/**
 * Every error a bound Wails method rejects with crosses the boundary as a
 * JSON string in the rejected promise's `.message` — see desktop/errors.go's
 * `formatError` doc: Wails v2 stringifies whatever `ErrorFormatter` returns
 * into `new Error(...).message`, so a struct would arrive as the useless
 * "[object Object]" and this returns a JSON-encoded `ErrorDTO` instead.
 *
 * `HeliosError` is the parsed, typed form of that envelope. `code` is what
 * the UI keys behaviour off — most importantly which failures are worth a
 * "Retry" action (see `internal/domain.ErrorCode.Retryable`, mirrored in
 * `features/transcribe/errorCopy.ts`) — so never match on `.message` text.
 */
export class HeliosError extends Error {
  readonly code: ErrorCode | "unknown";
  readonly details?: Record<string, unknown>;
  /** The original, unparsed rejection — kept for logging, never for display logic. */
  readonly cause?: unknown;

  constructor(code: ErrorCode | "unknown", message: string, details?: Record<string, unknown>, cause?: unknown) {
    super(message);
    this.name = "HeliosError";
    this.code = code;
    this.details = details;
    this.cause = cause;
  }
}

/**
 * Parses a rejected Wails call (or any thrown value) into a `HeliosError`.
 *
 * Handles three shapes:
 * 1. The expected one — `err.message` is the `{code, message, details}` JSON
 *    envelope `desktop/errors.go` produces.
 * 2. A Wails-level or runtime failure that never went through
 *    `formatError` at all (a panic before the binding ran, a devtools
 *    error, `err.message` not JSON) — code becomes `"unknown"` and the raw
 *    message is preserved rather than thrown further.
 * 3. Something that is not an `Error` at all (a plain string, `undefined`)
 *    — same fallback.
 *
 * This function never throws: every call site can rely on getting a usable
 * `HeliosError` back unconditionally.
 */
export function parseError(err: unknown): HeliosError {
  const raw = errorMessageOf(err);

  if (raw !== undefined) {
    try {
      const parsed = JSON.parse(raw) as { code?: string; message?: string; details?: Record<string, unknown> };
      if (parsed && typeof parsed.code === "string" && typeof parsed.message === "string") {
        return new HeliosError(parsed.code as ErrorCode, parsed.message, parsed.details, err);
      }
    } catch {
      // Not JSON — fall through to the unknown-code fallback below.
    }
  }

  return new HeliosError("unknown", raw ?? "An unexpected error occurred.", undefined, err);
}

function errorMessageOf(err: unknown): string | undefined {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  return undefined;
}
