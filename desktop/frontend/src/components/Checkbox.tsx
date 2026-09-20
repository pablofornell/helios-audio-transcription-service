import { forwardRef, type InputHTMLAttributes } from "react";
import styles from "./Checkbox.module.css";

export interface CheckboxProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "size"> {
  label: string;
  /**
   * Keeps `label` as the input's real accessible name (via the wrapping
   * `<label>`) but hides its text visually — for compact contexts where the
   * label would only repeat text already shown next to it, e.g. a Library
   * row's per-item "Select {title}" checkbox sitting beside that same
   * title.
   */
  hideLabel?: boolean;
}

/**
 * Checkbox — DESIGN.md §6.5. 16px, used for submit-time options (Audio
 * only, Include description) — a form input applied at submit, not a
 * setting that takes effect immediately (that distinction is `Switch`'s
 * job). The label is itself a click target.
 */
export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  { label, hideLabel = false, className, id, ...rest },
  ref,
) {
  return (
    <label className={[styles.row, className].filter(Boolean).join(" ")} htmlFor={id}>
      <span className={styles.box}>
        <input ref={ref} type="checkbox" id={id} className={styles.input} {...rest} />
        <CheckGlyph className={styles.glyph} />
      </span>
      <span className={[styles.label, hideLabel ? styles.srOnly : ""].filter(Boolean).join(" ")}>{label}</span>
    </label>
  );
});

function CheckGlyph({ className }: { className?: string }) {
  return (
    <svg className={className} width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
      <path d="M2.5 6.2l2.4 2.4 4.6-5.2" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
