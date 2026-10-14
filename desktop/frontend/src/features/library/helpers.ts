import { displayStatus, formatDuration, formatRelative, type DisplayStatus, type Status, type Transcription } from "../../lib";

/** Builds the Library row's meta line: `task · model · language · duration · relative date`. */
export function buildLibraryMeta(transcription: Transcription): string {
  const segments: string[] = [];
  segments.push(transcription.options.task === "translate" ? "translate" : "transcribe");
  segments.push(transcription.options.model || "default model");
  segments.push(transcription.options.language || "auto-detect");
  segments.push(formatDuration(transcription.audio.durationSeconds));
  segments.push(formatRelative(transcription.completedAt ?? transcription.createdAt));
  return segments.join(" · ");
}

/** `DisplayStatus` a StatusPill wants, `undefined` for `succeeded` — Library omits the pill for the norm. */
export function libraryStatusSlot(transcription: Transcription, canceling = false): DisplayStatus | undefined {
  const status = displayStatus(transcription, canceling);
  return status === "succeeded" ? undefined : status;
}

/** Human labels for the status filter chips ("All · Complete · Failed · Canceled"). */
export const STATUS_FILTER_LABELS: Record<"succeeded" | "failed" | "canceled", string> = {
  succeeded: "Complete",
  failed: "Failed",
  canceled: "Canceled",
};

export type StatusFilterValue = keyof typeof STATUS_FILTER_LABELS;

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

/** Match server order, retaining submillisecond timestamp precision. */
export function compareLibraryItems(a: Transcription, b: Transcription): number {
  const rank = (t: Transcription) => t.status === "running" ? 0 : t.status === "pending" ? 1 : 2;
  const ar = rank(a), br = rank(b);
  if (ar !== br) return ar - br;
  const timestamp = (t: Transcription) => {
    const raw = ar < 2 ? t.createdAt : t.completedAt ?? t.createdAt;
    const fraction = /\.(\d+)/.exec(raw)?.[1] ?? "";
    return BigInt(Date.parse(raw)) * 1000000n + BigInt(fraction.padEnd(9, "0").slice(3, 9) || "0");
  };
  const at = timestamp(a), bt = timestamp(b);
  if (at !== bt) return (at < bt ? -1 : 1) * (ar === 2 ? -1 : 1);
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}
