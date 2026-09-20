import styles from "./Spinner.module.css";

export interface SpinnerProps {
  size?: 12 | 16 | 24;
  /** Defaults to `currentColor`; pass `"var(--accent-graphic)"` explicitly where that reads better against the surface. */
  color?: string;
  className?: string;
}

/**
 * Spinner — DESIGN.md §6.12 and Coordinator ruling §3.
 *
 * Under normal motion: a 270° arc rotating 900ms linear.
 *
 * Under `prefers-reduced-motion: reduce`: rotation stops (a user who asked
 * for no movement through space gets none), but the spinner is NOT frozen
 * — the ruling is explicit that a frozen spinner reads as broken. Instead
 * `Spinner.module.css` swaps the rotation for a non-rotational, low-
 * amplitude opacity pulse at 1400ms, driven by the same `.spinner` class;
 * there is no separate reduced-motion prop because the OS setting alone
 * decides which animation runs.
 */
export function Spinner({ size = 16, color, className }: SpinnerProps) {
  const px = size;
  return (
    <svg
      className={[styles.spinner, className].filter(Boolean).join(" ")}
      width={px}
      height={px}
      viewBox="0 0 24 24"
      fill="none"
      role="presentation"
      aria-hidden="true"
      style={color ? { color } : undefined}
    >
      <circle
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray="47 63"
      />
    </svg>
  );
}
