import styles from "./ProgressBar.module.css";

export type ProgressVariant = "lane" | "bar";
export type ProgressMode =
  | "determinate"
  | "indeterminate"
  | "stale"
  | "complete"
  | "failed"
  | "canceled"
  | "locked";

export interface ProgressBarProps {
  /** `lane`: 3px, full-bleed, a Queue row's bottom edge. `bar`: 6px, rounded, detail panel and the first-run composer. */
  variant?: ProgressVariant;
  mode: ProgressMode;
  /** 0–100. Ignored (but harmless) for `indeterminate`. */
  value?: number;
  /** Accessible name — a Queue row's title, e.g. "Never Gonna Give You Up". */
  label: string;
  className?: string;
}

/**
 * ProgressBar — DESIGN.md §6.7. The lane is ALWAYS in the DOM in every row
 * status (DESIGN.md §4.3 rule 2) — callers should render this
 * unconditionally with the mode that matches the row's current status
 * rather than conditionally mounting/unmounting it, or the 3px lane
 * shifts everything under it.
 *
 * `role="progressbar"` with `aria-live="off"` — DESIGN.md §8: a bar that
 * announces itself thirty times a second makes the app unusable, so the
 * value is available on demand (via `aria-valuenow`) rather than announced.
 */
export function ProgressBar({ variant = "lane", mode, value = 0, label, className }: ProgressBarProps) {
  const clamped = Math.max(0, Math.min(100, value));
  const width = mode === "complete" ? 100 : mode === "indeterminate" ? undefined : clamped;

  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={mode === "indeterminate" ? undefined : Math.round(clamped)}
      aria-live="off"
      className={[styles.track, styles[variant], className].filter(Boolean).join(" ")}
    >
      <div
        className={[styles.fill, styles[mode]].filter(Boolean).join(" ")}
        style={width !== undefined ? { width: `${width}%` } : undefined}
      />
    </div>
  );
}
