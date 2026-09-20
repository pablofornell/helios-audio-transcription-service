import type { HTMLAttributes, ReactNode } from "react";
import styles from "./Panel.module.css";

export interface PanelProps extends HTMLAttributes<HTMLDivElement> {
  /** Removes the default padding, for panels that manage their own inner spacing (e.g. a Row list). */
  noPadding?: boolean;
}

/**
 * Panel — DESIGN.md §6.9. `--bg-surface`, 1px `--border-subtle`,
 * `--radius-md`, `--space-4` padding. No nested Panels — depth in this app
 * comes from hairlines and `--bg-sunken`, never stacked shadows.
 */
export function Panel({ noPadding = false, className, children, ...rest }: PanelProps) {
  return (
    <div className={[styles.panel, noPadding ? styles.noPadding : "", className].filter(Boolean).join(" ")} {...rest}>
      {children}
    </div>
  );
}

export interface SectionHeaderProps {
  children: ReactNode;
  action?: ReactNode;
  className?: string;
  /** Sticky section header (ACTIVE / RECENT) — DESIGN.md §4.3 rule 6. */
  sticky?: boolean;
}

/** SectionHeader — 11px uppercase `--track-wide` `--text-tertiary`, e.g. "ACTIVE · 2 downloading, 1 queued". */
export function SectionHeader({ children, action, className, sticky = false }: SectionHeaderProps) {
  return (
    <div className={[styles.sectionHeader, sticky ? styles.sticky : "", className].filter(Boolean).join(" ")}>
      <span className={styles.sectionHeaderLabel}>{children}</span>
      {action}
    </div>
  );
}
