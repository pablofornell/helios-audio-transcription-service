import { formatDuration, formatLanguages, formatRelative, type Status, type Transcription } from "../../lib";

/** Builds the Library row's meta line: `model · language · duration · relative date`. */
export function buildLibraryMeta(transcription: Transcription): string {
  const segments: string[] = [];
  segments.push(transcription.result?.model || transcription.options.model || "default model");
  segments.push(formatLanguages(transcription));
  segments.push(formatDuration(transcription.audio.durationSeconds));
  segments.push(formatRelative(transcription.completedAt ?? transcription.createdAt));
  return segments.join(" · ");
}

/**
 * True while `el` sits inside a menu, listbox or dialog — those already own
 * the keyboard (arrow keys, Enter, Escape, typeahead) via their own
 * handlers, so a global Esc/arrow-key listener elsewhere must not also
 * react to the same press.
 */
export function isWithinOwnedWidget(el: Element | null): boolean {
  return Boolean(el?.closest('[role="listbox"], [role="menu"], [role="dialog"]'));
}

/** The client-side search predicate — filename, case-insensitive. */
export function matchesSearch(transcription: Transcription, search: string): boolean {
  const q = search.trim().toLowerCase();
  if (!q) return true;
  return transcription.audio.filename.toLowerCase().includes(q);
}

export function isKnownStatus(value: string): value is Status {
  return value === "pending" || value === "running" || value === "succeeded" || value === "failed" || value === "canceled";
}

/** Match server order: newest completion first, then id. Keep submillisecond timestamp precision. */
export function compareLibraryItems(a: Transcription, b: Transcription): number {
  const timestamp = (t: Transcription) => {
    const raw = t.completedAt ?? t.createdAt;
    const fraction = /\.(\d+)/.exec(raw)?.[1] ?? "";
    return BigInt(Date.parse(raw)) * 1000000n + BigInt(fraction.padEnd(9, "0").slice(3, 9) || "0");
  };
  const at = timestamp(a), bt = timestamp(b);
  if (at !== bt) return at < bt ? 1 : -1;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}
