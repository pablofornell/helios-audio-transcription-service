import type { DisplayStatus, ErrorCode } from "../lib/types";
import styles from "./StatusPill.module.css";

export interface StatusPillProps {
  status: DisplayStatus;
  /** Unused for rendering beyond documenting intent — accepted so callers can pass a transcription's `error.code` straight through without a branch. */
  errorCode?: ErrorCode | null;
  /** 16px, for Library rows (rendered only for non-succeeded rows there). Default 20px, for the Queue. */
  dense?: boolean;
  className?: string;
}

const LABELS: Record<DisplayStatus, string> = {
  pending: "Queued",
  running: "Downloading",
  succeeded: "Complete",
  failed: "Failed",
  canceled: "Canceled",
  canceling: "Canceling…",
};

/**
 * StatusPill — DESIGN.md §3 and §6.6. The most-repeated element in the
 * product. Carries all four required channels so it stays legible in
 * greyscale: glyph silhouette, fill treatment (including the 45° hatch on
 * `failed`), an always-present text label, and motion ONLY on `running`.
 *
 * `status` is a `DisplayStatus` — the five API `Status` values plus the
 * client-only `canceling` state from `lib/types.ts`. Deriving which one
 * applies to a given `Transcription` is `lib/types.ts`'s `displayStatus()`
 * helper; do that once at the call site rather than re-deriving
 * `canceling` here.
 */
export function StatusPill({ status, dense = false, className }: StatusPillProps) {
  return (
    <span
      className={[styles.pill, styles[status], dense ? styles.dense : "", className].filter(Boolean).join(" ")}
    >
      <Glyph status={status} className={styles.glyph} />
      <span className={styles.label}>{LABELS[status]}</span>
    </span>
  );
}

function Glyph({ status, className }: { status: DisplayStatus; className?: string }) {
  const common = { width: 12, height: 12, viewBox: "0 0 12 12", "aria-hidden": true as const, className };
  switch (status) {
    case "pending":
      return (
        <svg {...common}>
          <circle cx="6" cy="6" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeDasharray="2.2 2.2" />
        </svg>
      );
    case "running":
      return (
        <svg {...common}>
          <circle
            cx="6"
            cy="6"
            r="4.5"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeDasharray="21 28"
          />
        </svg>
      );
    case "succeeded":
      return (
        <svg {...common}>
          <path d="M2.5 6.2l2.4 2.4 4.6-5.2" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "failed":
      return (
        <svg {...common}>
          <path d="M3 3l6 6M9 3l-6 6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="square" />
        </svg>
      );
    case "canceled":
    case "canceling":
      return (
        <svg {...common}>
          <path d="M2.5 6h7" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
        </svg>
      );
    default:
      return null;
  }
}
