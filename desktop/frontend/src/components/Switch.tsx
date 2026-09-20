import { forwardRef, type InputHTMLAttributes } from "react";
import styles from "./Switch.module.css";

export interface SwitchProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "type" | "size"> {
  label: string;
}

/**
 * Switch — DESIGN.md §6.5. 32×18, used ONLY in Settings, where flipping it
 * takes effect immediately. Do not reach for this for submit-time options
 * (`Checkbox` is for those) — the distinction is meaningful: a switch
 * implies an immediate side effect, a checkbox implies "applied when you
 * submit this form".
 */
export const Switch = forwardRef<HTMLInputElement, SwitchProps>(function Switch(
  { label, className, id, ...rest },
  ref,
) {
  return (
    <label className={[styles.row, className].filter(Boolean).join(" ")} htmlFor={id}>
      <span className={styles.label}>{label}</span>
      <span className={styles.track}>
        <input ref={ref} type="checkbox" role="switch" id={id} className={styles.input} {...rest} />
        <span className={styles.thumb} />
      </span>
    </label>
  );
});
