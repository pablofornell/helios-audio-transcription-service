import { forwardRef, useId, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { IconButton } from "./IconButton";
import styles from "./TextField.module.css";

export type TextFieldVariant = "url" | "search" | "plain";

export interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "size"> {
  variant?: TextFieldVariant;
  /** Renders a red border and the message below — DESIGN.md §6.3: invalid never shows colour alone. */
  invalid?: boolean;
  errorMessage?: string;
  /** Leading slot: platform glyph for `url`, search icon for `search`. */
  leading?: ReactNode;
  /** Trailing slot, e.g. a `⌘F` kbd chip. Ignored when `copyable` supplies its own trailing button. */
  trailing?: ReactNode;
  /** Read-only field with a copy-to-clipboard button (paths, hashes) — DESIGN.md §6.3. */
  copyable?: boolean;
  label?: string;
}

/**
 * TextField — DESIGN.md §6.3. `url` (44px, mono, platform-glyph gutter),
 * `search` (32px, leading icon), `plain` (32px, Settings). Validation
 * styling is opt-in via `invalid`/`errorMessage`; callers decide *when* to
 * set it (DESIGN.md's rule that a URL field must not turn red while the
 * user is still typing is a call-site concern, not this component's).
 */
export const TextField = forwardRef<HTMLInputElement, TextFieldProps>(function TextField(
  {
    variant = "plain",
    invalid = false,
    errorMessage,
    leading,
    trailing,
    copyable = false,
    label,
    className,
    id,
    value,
    readOnly,
    ...rest
  },
  ref,
) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const errorId = `${inputId}-error`;
  const [copied, setCopied] = useState(false);

  async function copy() {
    const text = typeof value === "string" ? value : String(value ?? "");
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard access can be denied in a locked-down WebView; failing
      // silently beats throwing out of an event handler for a non-critical
      // convenience action.
    }
  }

  return (
    <div className={styles.field}>
      {label ? (
        <label className={styles.label} htmlFor={inputId}>
          {label}
        </label>
      ) : null}
      <div
        className={[
          styles.wrapper,
          styles[variant],
          invalid ? styles.invalid : "",
          readOnly ? styles.readOnly : "",
          className,
        ]
          .filter(Boolean)
          .join(" ")}
      >
        {leading ? (
          <span className={styles.leading} aria-hidden="true">
            {leading}
          </span>
        ) : null}
        <input
          ref={ref}
          id={inputId}
          className={[styles.input, "selectable"].join(" ")}
          value={value}
          readOnly={readOnly}
          aria-invalid={invalid || undefined}
          aria-describedby={invalid && errorMessage ? errorId : undefined}
          {...rest}
        />
        {copyable ? (
          <IconButton
            size={24}
            className={styles.trailing}
            icon={copied ? <CheckGlyph /> : <CopyGlyph />}
            label={copied ? "Copied" : "Copy"}
            onClick={copy}
          />
        ) : trailing ? (
          <span className={styles.trailing}>{trailing}</span>
        ) : null}
      </div>
      {invalid && errorMessage ? (
        <p id={errorId} className={styles.error}>
          {errorMessage}
        </p>
      ) : null}
    </div>
  );
});

function CopyGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
      <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
      <path d="M3.5 10.5v-6a1 1 0 0 1 1-1h6" stroke="currentColor" strokeWidth="1.3" />
    </svg>
  );
}

function CheckGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
      <path d="M3.5 8.5l3 3 6-6.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
