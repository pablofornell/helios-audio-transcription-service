import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Tooltip } from "./Tooltip";
import styles from "./IconButton.module.css";

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  size?: 24 | 28 | 32;
  icon: ReactNode;
  /** Required — every IconButton carries a tooltip and an `aria-label` (DESIGN.md §6.2). */
  label: string;
}

/**
 * IconButton — DESIGN.md §6.2. 24/28/32px squares, always ghost, always
 * with a Tooltip and a real `aria-label` (never title-attribute-only).
 * Row actions use 28px with an 8px hit-area bleed via a pseudo-element,
 * since a 64px row leaves no room for a larger visible target but a bare
 * 28px click target is unkind.
 */
export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { size = 28, icon, label, className, type = "button", ...rest },
  ref,
) {
  return (
    <Tooltip content={label}>
      <button
        ref={ref}
        type={type}
        aria-label={label}
        className={[styles.button, styles[`s${size}` as "s24" | "s28" | "s32"], className].filter(Boolean).join(" ")}
        {...rest}
      >
        <span aria-hidden="true" className={styles.icon}>
          {icon}
        </span>
      </button>
    </Tooltip>
  );
});
