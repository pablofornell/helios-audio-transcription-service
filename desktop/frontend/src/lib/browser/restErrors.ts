import { HeliosError } from "../errors";
import type { ErrorCode } from "../types";

/*
 * Parses docs/openapi.yaml's ErrorResponse envelope (every non-2xx JSON
 * body this API returns: `{"error":{"code","message","details"}}`) into
 * the same `HeliosError` the Wails adapter produces from a rejected
 * binding's `.message` — see `../errors.ts`'s `parseError` for that side
 * and its doc for why `code` (never `.message` text) is what the UI
 * branches on.
 *
 * This is deliberately a second, REST-shaped parser rather than a reuse of
 * `parseError`: that function expects the envelope pre-serialised into an
 * `Error.message` string (Wails' `formatError` convention), whereas here
 * the envelope is the parsed JSON response body itself. Both converge on
 * the same `HeliosError`, which is the point.
 *
 * `fallbackCodeForStatus` mirrors docs/openapi.yaml's "Error code -> HTTP
 * status" table exactly (the reverse direction) — only used when a
 * response cannot be parsed as the envelope at all (a malformed body, or a
 * response from something that is not this API, e.g. a proxy's error
 * page).
 */
const fallbackCodeForStatus: Record<number, ErrorCode> = {
  400: "invalid_input",
  415: "unsupported_media",
  422: "no_audio_stream",
  413: "payload_too_large",
  404: "not_found",
  409: "conflict",
  507: "disk_full",
  503: "model_unavailable",
  502: "tool_failure",
  504: "timeout",
  499: "canceled",
  500: "internal",
};

const MAX_SNIPPET_LEN = 200;

/** Bounds an error body snippet so a hostile or merely large non-envelope response (an HTML error page) can never make an error message unbounded. */
function snippetOf(body: string): string {
  const s = body.trim().replace(/\s+/g, " ");
  return s.length > MAX_SNIPPET_LEN ? `${s.slice(0, MAX_SNIPPET_LEN)}...` : s;
}

interface ErrorEnvelope {
  error?: { code?: unknown; message?: unknown; details?: Record<string, unknown> };
}

/**
 * Classifies a non-2xx response's status and body text into a
 * `HeliosError`. The happy path reads `body`'s `error.code` directly,
 * verbatim — the server already classified this failure once — so a
 * caller sees the exact same code whether `app.App` ran in this process
 * (Wails) or across this REST call.
 */
export function classifyRestError(status: number, body: string): HeliosError {
  if (body) {
    try {
      const parsed = JSON.parse(body) as ErrorEnvelope;
      if (parsed?.error && typeof parsed.error.code === "string" && typeof parsed.error.message === "string") {
        return new HeliosError(parsed.error.code as ErrorCode, parsed.error.message, parsed.error.details);
      }
    } catch {
      // Not the envelope — fall through to the status-based fallback below.
    }
  }

  const code = fallbackCodeForStatus[status] ?? "internal";
  const snippet = snippetOf(body);
  const message = snippet
    ? `helios server returned status ${status}: ${snippet}`
    : `helios server returned status ${status} with no error body`;
  return new HeliosError(code, message);
}

/** Builds a `HeliosError` from a rejected `fetch()` call itself (dial failure, connection reset, an aborted request) — never a response `classifyRestError` could see. */
export function classifyTransportError(err: unknown): HeliosError {
  if (err instanceof DOMException && err.name === "AbortError") {
    return new HeliosError("timeout", "request to helios server timed out", undefined, err);
  }
  const detail = err instanceof Error ? err.message : String(err);
  return new HeliosError("internal", `request to helios server failed: ${detail}`, undefined, err);
}
