import { useRef } from "react";
import { Tooltip } from "./Tooltip";
import styles from "./Segmented.module.css";

export interface SegmentedOption {
  value: string;
  label: string;
}

export interface SegmentedProps {
  options: SegmentedOption[];
  value: string;
  onChange: (value: string) => void;
  label: string;
  /** Dims the control to 0.45 opacity and marks it `aria-disabled`, with `disabledReason` in a Tooltip — DESIGN.md §4.2's "Quality applies to video downloads" case. */
  disabled?: boolean;
  disabledReason?: string;
  className?: string;
}

/**
 * Segmented — DESIGN.md §6.4. Used for Quality: a `role="radiogroup"` track
 * where every option is always visible (never hidden behind a dropdown
 * click), arrow keys move the selection, and the indicator slides between
 * options over `--dur-fast`.
 */
export function Segmented({ options, value, onChange, label, disabled = false, disabledReason, className }: SegmentedProps) {
  const rootRef = useRef<HTMLDivElement>(null);

  function move(delta: number) {
    const index = options.findIndex((o) => o.value === value);
    const next = options[(index + delta + options.length) % options.length];
    if (next) onChange(next.value);
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (disabled) return;
    if (e.key === "ArrowRight" || e.key === "ArrowDown") {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Home") {
      e.preventDefault();
      const first = options[0];
      if (first) onChange(first.value);
    } else if (e.key === "End") {
      e.preventDefault();
      const last = options[options.length - 1];
      if (last) onChange(last.value);
    }
  }

  const track = (
    <div
      ref={rootRef}
      role="radiogroup"
      aria-label={label}
      aria-disabled={disabled || undefined}
      className={[styles.track, disabled ? styles.disabled : "", className].filter(Boolean).join(" ")}
      onKeyDown={onKeyDown}
    >
      {options.map((option) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            tabIndex={disabled ? -1 : selected ? 0 : -1}
            className={[styles.option, selected ? styles.selected : ""].filter(Boolean).join(" ")}
            onClick={() => !disabled && onChange(option.value)}
            disabled={disabled}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );

  if (disabled && disabledReason) {
    return <Tooltip content={disabledReason}>{track}</Tooltip>;
  }
  return track;
}
