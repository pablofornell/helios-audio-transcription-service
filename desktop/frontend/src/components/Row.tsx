import { forwardRef, type HTMLAttributes, type ReactNode } from "react";
import type { DisplayStatus } from "../lib/types";
import styles from "./Row.module.css";

export interface RowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  variant: "queue" | "library";
  /** Drives the 3px left rail's colour, unless `selected` is set. */
  status: DisplayStatus;
  /** Selection outranks status for the rail — DESIGN.md §4.4: the selected row gets a 2px `--accent-graphic` rail that replaces the status rail. */
  selected?: boolean;
  /**
   * Plays the decaying inset flash ring once — DESIGN.md §4.2 step 3. This
   * is a one-shot CSS animation (`animation-fill-mode: forwards`, ends
   * invisible); toggle the prop back off after `--dur-flash` (900ms) if the
   * same row might need to flash again later (e.g. a dedupe hit), since a
   * prop that stays `true` will not replay on its own.
   */
  flash?: boolean;
  /** Frozen/desaturated lane treatment during a connection outage — DESIGN.md §5.7. Pass this through to the row's `lane` slot's own `mode` too. */
  stale?: boolean;
  /** The only row that ever changes height — `auth_required` rows, on explicit click (DESIGN.md §5.8). */
  expanded?: boolean;
  expandedContent?: ReactNode;
  /** Multi-select checkbox, rendered before `thumb` — omitted (as in `queue`) means no selection column at all, not an invisible one. Library is the only caller that passes this today. */
  checkbox?: ReactNode;
  thumb: ReactNode;
  title: ReactNode;
  meta: ReactNode;
  /** The StatusPill, or omitted — Library omits it for `succeeded` rows (DESIGN.md §4.4). */
  statusSlot?: ReactNode;
  metric: ReactNode;
  /** Always visible (⏹ cancel / ↻ retry). */
  primaryAction?: ReactNode;
  /** Revealed on hover/focus-within (⋯ menu). */
  secondaryActions?: ReactNode;
  /** The progress lane — DESIGN.md §4.3 rule 2: keep this in the DOM in every status, never conditionally rendered. */
  lane?: ReactNode;
}

/**
 * Row — DESIGN.md §6.8 and §4.3's anatomy table. Shared skeleton for the
 * `queue` (64px) and `library` (56px) row heights; the height difference
 * and the thumbnail size that goes with it are the only things `variant`
 * changes. Fixed height in every status is the anti-jump rule DESIGN.md
 * §4.3 states as "the specification, not a suggestion" — do not let a
 * caller's content grow this element except via `expanded`.
 */
export const Row = forwardRef<HTMLLIElement, RowProps>(function Row(
  {
    variant,
    status,
    selected = false,
    flash = false,
    stale = false,
    expanded = false,
    expandedContent,
    checkbox,
    thumb,
    title,
    meta,
    statusSlot,
    metric,
    primaryAction,
    secondaryActions,
    lane,
    className,
    children: _children,
    ...rest
  },
  ref,
) {
  return (
    <li
      ref={ref}
      className={[
        styles.row,
        styles[variant],
        selected ? styles.selected : "",
        flash ? styles.flash : "",
        stale ? styles.stale : "",
        expanded ? styles.expanded : "",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
      data-status={status}
      {...rest}
    >
      <div className={styles.main}>
        <span className={styles.rail} aria-hidden="true" />
        {checkbox ? <span className={styles.checkbox}>{checkbox}</span> : null}
        <span className={styles.thumb}>{thumb}</span>
        <div className={styles.titleBlock}>
          <div className={styles.title}>{title}</div>
          <div className={styles.meta}>{meta}</div>
        </div>
        {statusSlot ? <div className={styles.status}>{statusSlot}</div> : null}
        <div className={styles.metric}>{metric}</div>
        <div className={styles.actions}>
          {primaryAction}
          {secondaryActions ? <span className={styles.secondaryActions}>{secondaryActions}</span> : null}
        </div>
        {lane ? <div className={styles.lane}>{lane}</div> : null}
      </div>
      {expanded && expandedContent ? <div className={styles.expandedPanel}>{expandedContent}</div> : null}
    </li>
  );
});
