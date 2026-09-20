import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Spinner } from "./Spinner";
import styles from "./Button.module.css";

export type ButtonVariant = "primary" | "neutral" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "disabled"> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Leading icon/glyph slot. Replaced by a spinner while `loading`. */
  leading?: ReactNode;
  /**
   * Shows a spinner in the leading slot and locks the button's width so it
   * never resizes (DESIGN.md §6.1). Also disables the button — its action
   * is already in flight, so a second click must not start it again. A
   * caller does not need to *also* pass `disabled` for this; relying on a
   * caller to remember that is exactly what used to let a fast double-click
   * run a `loading` action twice.
   */
  loading?: boolean;
  /**
   * Disables the button. When `disabledReason` is also given this uses
   * `aria-disabled` (not the native `disabled` attribute) so a Tooltip can
   * still explain why — DESIGN.md §6.1's distinction. Without a reason it
   * falls back to native `disabled`.
   */
  disabled?: boolean;
  disabledReason?: string;
  /** Trailing keyboard-shortcut chip, e.g. `"⌘↵"`. */
  kbd?: string;
}

/**
 * Button — DESIGN.md §6.1. Four variants (`primary`/`neutral`/`ghost`/
 * `danger`), three sizes, and rest/hover/active/focus-visible/disabled/
 * loading states. `danger` is ghost at rest and only turns red on hover —
 * a destructive action should not look alarmed by default.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "neutral",
    size = "md",
    leading,
    loading = false,
    disabled = false,
    disabledReason,
    kbd,
    className,
    children,
    type = "button",
    ...rest
  },
  ref,
) {
  // `loading` always implies disabled (see its own doc above) — merged in
  // here rather than left to each call site so `disabledReason`'s
  // aria-disabled-vs-native distinction still applies consistently to
  // whichever of the two (or both) is why this button is disabled.
  const isDisabled = disabled || loading;
  const nativeDisabled = isDisabled && !disabledReason;
  const ariaDisabled = isDisabled && Boolean(disabledReason) ? true : undefined;

  return (
    <button
      ref={ref}
      type={type}
      className={[styles.button, styles[variant], styles[size], loading ? styles.loading : "", className]
        .filter(Boolean)
        .join(" ")}
      disabled={nativeDisabled}
      aria-disabled={ariaDisabled}
      title={disabled ? disabledReason : undefined}
      {...rest}
    >
      {loading ? (
        <span className={styles.leading} aria-hidden="true">
          <Spinner size={size === "lg" ? 16 : 12} />
        </span>
      ) : leading ? (
        <span className={styles.leading} aria-hidden="true">
          {leading}
        </span>
      ) : null}
      <span className={styles.label}>{children}</span>
      {kbd ? <kbd className={styles.kbd}>{kbd}</kbd> : null}
    </button>
  );
});
