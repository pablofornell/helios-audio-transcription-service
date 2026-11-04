/*
 * Shared display-formatting helpers. Centralised here so both feature
 * screens render timestamps, sizes and durations identically.
 */

import type { Transcription } from "./types";

/** Formats bytes as SI units (MB = 10^6), matching what a Linux file manager reports. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1000 && unitIndex < units.length - 1) {
    value /= 1000;
    unitIndex += 1;
  }
  const precision = unitIndex === 0 ? 0 : value < 10 ? 1 : 0;
  return `${value.toFixed(precision)} ${units[unitIndex]}`;
}

/** Formats a duration in seconds as `m:ss` (or `h:mm:ss` past an hour). `0` or absent renders `—`, never `0:00` — this is for a media *length*, where zero means "not yet known", not "zero seconds long". */
export function formatDuration(seconds: number | undefined | null): string {
  if (!seconds || seconds <= 0) return "—";
  return formatTimestamp(seconds);
}

/**
 * Formats a non-negative offset into the audio as `m:ss` (or `h:mm:ss` past
 * an hour), including a genuine `0:00` — unlike `formatDuration`, this is
 * for a *position* (a segment/word's start/end), where zero is the
 * legitimate start of the recording, not "unknown". Used by the transcript
 * viewer's clickable `[mm:ss]` segment timestamps.
 */
export function formatTimestamp(seconds: number): string {
  const totalSeconds = Math.max(0, Math.round(seconds));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const secs = totalSeconds % 60;
  const pad = (n: number) => n.toString().padStart(2, "0");
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(secs)}` : `${minutes}:${pad(secs)}`;
}

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 60 * 60 * 24 * 365],
  ["month", 60 * 60 * 24 * 30],
  ["week", 60 * 60 * 24 * 7],
  ["day", 60 * 60 * 24],
  ["hour", 60 * 60],
  ["minute", 60],
  ["second", 1],
];

const relativeFormatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

/** Formats an ISO timestamp as a relative string ("2 minutes ago"). Pair with `formatAbsolute` for the tooltip/`title`. */
export function formatRelative(iso: string | undefined | null, now: Date = new Date()): string {
  if (!iso) return "—";
  const then = new Date(iso);
  if (Number.isNaN(then.getTime())) return "—";
  const diffSeconds = Math.round((then.getTime() - now.getTime()) / 1000);
  const absSeconds = Math.abs(diffSeconds);
  if (absSeconds < 5) return "just now";
  for (const [unit, secondsInUnit] of RELATIVE_UNITS) {
    if (absSeconds >= secondsInUnit || unit === "second") {
      const value = Math.round(diffSeconds / secondsInUnit);
      return relativeFormatter.format(value, unit);
    }
  }
  return "just now";
}

/** Formats an ISO timestamp for a tooltip or `title` attribute — the absolute counterpart to `formatRelative`. */
export function formatAbsolute(iso: string | undefined | null): string {
  if (!iso) return "—";
  const then = new Date(iso);
  if (Number.isNaN(then.getTime())) return "—";
  return then.toLocaleString(undefined, {
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Seconds elapsed since an ISO timestamp, for staleness displays ("last update 14s ago"). */
export function secondsSince(iso: string | undefined | null, now: Date = new Date()): number | undefined {
  if (!iso) return undefined;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return undefined;
  return Math.max(0, Math.round((now.getTime() - then) / 1000));
}

/** English name of an ISO-639 language code ("fr" → "French"), or the code itself when the runtime does not know it. */
export function languageName(code: string): string {
  try {
    return new Intl.DisplayNames(["en"], { type: "language" }).of(code) ?? code;
  } catch {
    return code;
  }
}

/**
 * The language line of a job. For a finished job it shows the language the
 * service detected in the audio and, when it differs, the language of the
 * transcript: "French → English". Before a result exists the service has not
 * detected anything yet, and it ignores any language hint in the request, so
 * the line says "auto-detect".
 *
 * A result stored before `outputLanguage` existed has no output language. If
 * its task was "translate", its text is English, so the line still shows
 * "→ English" (an old row with a French hint and a translate task shows
 * "French → English", not "French").
 */
export function formatLanguages(transcription: Pick<Transcription, "result">): string {
  const result = transcription.result;
  if (!result?.language) return "auto-detect";
  const detected = languageName(result.language);
  const output = result.outputLanguage || (result.task === "translate" ? "en" : "");
  if (output && output !== result.language) {
    return `${detected} → ${languageName(output)}`;
  }
  return detected;
}
