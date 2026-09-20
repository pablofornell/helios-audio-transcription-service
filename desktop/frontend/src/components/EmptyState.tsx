import type { ReactNode } from "react";
import styles from "./EmptyState.module.css";

export interface EmptyStateProps {
  /**
   * `surface`: full-area, centred, optionally with a serif headline — for
   * the two states DESIGN.md §5 rations Instrument Serif to (first run,
   * §5.1; backend unreachable, §5.6). `inline`: one quiet 13px
   * `--text-tertiary` line — DESIGN.md §5.2/§5.3/§5.4's "Queue is clear",
   * "Nothing finished yet", "No Instagram items…" states.
   */
  variant?: "surface" | "inline";
  /**
   * Renders in Instrument Serif. Only ever pass this for the two states
   * named above — DESIGN.md is explicit that the serif appears nowhere
   * else in the happy path.
   */
  headline?: string;
  /** `xl` (40/44) for the first-run headline; `lg` (28/34) for the cold-start headline. */
  headlineSize?: "xl" | "lg";
  body?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
}

/**
 * EmptyState — covers every empty/placeholder state DESIGN.md §5
 * describes. None of these is a stock illustration; the component itself
 * renders nothing decorative beyond an optional caller-supplied `icon`
 * inside `children`/`body` — the trust budget is in the copy, which is a
 * feature screen's job to write per-state.
 */
export function EmptyState({ variant = "surface", headline, headlineSize = "lg", body, actions, children, className }: EmptyStateProps) {
  if (variant === "inline") {
    return <p className={[styles.inline, className].filter(Boolean).join(" ")}>{children ?? body}</p>;
  }

  return (
    <div className={[styles.surface, className].filter(Boolean).join(" ")}>
      <div className={styles.content}>
        {headline ? <h1 className={[styles.headline, styles[headlineSize]].join(" ")}>{headline}</h1> : null}
        {body ? <div className={styles.body}>{body}</div> : null}
        {actions ? <div className={styles.actions}>{actions}</div> : null}
        {children}
      </div>
    </div>
  );
}
