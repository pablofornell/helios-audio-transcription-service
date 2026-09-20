import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import styles from "./Select.module.css";

export interface SelectOption {
  value: string;
  label: string;
}

export interface SelectProps {
  options: SelectOption[];
  value: string;
  onChange: (value: string) => void;
  /** Accessible name for the trigger, e.g. "Sort". */
  label: string;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
}

const TYPEAHEAD_RESET_MS = 600;

/**
 * Select — DESIGN.md §6.4 and Coordinator ruling §4.
 *
 * A hand-rolled listbox, not `<select>`: WebKitGTK's native popup ignores
 * the app's theme on Linux. Per the ruling this ships ONLY with full
 * `role="listbox"`/`role="option"` semantics, `aria-activedescendant`,
 * Up/Down/Home/End/Escape/type-ahead, focus returned to the trigger on
 * close, and `aria-expanded` on the trigger — every one of those is
 * implemented below. If you need a select elsewhere and cannot preserve
 * all of this, use a native `<select>` instead of copying a weakened
 * version of this component.
 *
 * Pattern: the trigger is a button; opening moves DOM focus onto the
 * (tabindex=-1) listbox itself, which owns `aria-activedescendant`
 * pointing at the current option's id. Arrow keys move that pointer,
 * Enter/Space commits and closes, Escape closes without committing — both
 * return focus to the trigger.
 */
export function Select({ options, value, onChange, label, placeholder = "Select…", disabled = false, className }: SelectProps) {
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const baseId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listboxRef = useRef<HTMLUListElement>(null);
  const typeaheadRef = useRef("");
  const typeaheadTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const selectedIndex = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );
  const selected = options.find((o) => o.value === value);
  const listboxId = `${baseId}-listbox`;
  const optionId = (index: number) => `${baseId}-opt-${index}`;

  useEffect(() => {
    if (!open) return;
    setActiveIndex(selectedIndex);
    // Defer focus until the listbox has mounted.
    const id = requestAnimationFrame(() => listboxRef.current?.focus());
    return () => cancelAnimationFrame(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: MouseEvent) {
      const target = e.target as Node;
      if (listboxRef.current?.contains(target) || triggerRef.current?.contains(target)) return;
      close(false);
    }
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!open) return;
    document.getElementById(optionId(activeIndex))?.scrollIntoView({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeIndex]);

  function close(returnFocus: boolean) {
    setOpen(false);
    if (returnFocus) triggerRef.current?.focus();
  }

  function commit(index: number) {
    const option = options[index];
    if (option) onChange(option.value);
    close(true);
  }

  function typeahead(char: string) {
    if (typeaheadTimerRef.current) clearTimeout(typeaheadTimerRef.current);
    typeaheadRef.current += char.toLowerCase();
    typeaheadTimerRef.current = setTimeout(() => {
      typeaheadRef.current = "";
    }, TYPEAHEAD_RESET_MS);
    const match = options.findIndex((o) => o.label.toLowerCase().startsWith(typeaheadRef.current));
    if (match >= 0) setActiveIndex(match);
  }

  function onListboxKeyDown(e: KeyboardEvent<HTMLUListElement>) {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setActiveIndex((i) => Math.min(options.length - 1, i + 1));
        break;
      case "ArrowUp":
        e.preventDefault();
        setActiveIndex((i) => Math.max(0, i - 1));
        break;
      case "Home":
        e.preventDefault();
        setActiveIndex(0);
        break;
      case "End":
        e.preventDefault();
        setActiveIndex(options.length - 1);
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        commit(activeIndex);
        break;
      case "Escape":
        // Any component that handles Escape stops it propagating, so
        // exactly one layer reacts per press (DESIGN.md §8) — otherwise this
        // closing also dismisses whatever owns Escape above it (a dialog, a
        // detail panel, the search field).
        e.preventDefault();
        e.stopPropagation();
        close(true);
        break;
      case "Tab":
        // Return focus to the trigger AND consume the key: closing without
        // preventDefault lets the browser's own Tab traversal continue
        // immediately afterward from wherever focus just landed, carrying
        // focus straight past the trigger to whatever follows it in the DOM.
        e.preventDefault();
        close(true);
        break;
      default:
        if (e.key.length === 1) typeahead(e.key);
    }
  }

  function onTriggerKeyDown(e: KeyboardEvent<HTMLButtonElement>) {
    if (disabled) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp" || e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      setOpen(true);
    }
  }

  return (
    <div className={[styles.root, className].filter(Boolean).join(" ")}>
      <button
        ref={triggerRef}
        type="button"
        className={styles.trigger}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listboxId}
        aria-label={label}
        disabled={disabled}
        onClick={() => !disabled && setOpen((o) => !o)}
        onKeyDown={onTriggerKeyDown}
      >
        <span className={styles.value}>{selected ? selected.label : placeholder}</span>
        <ChevronGlyph className={styles.chevron} />
      </button>
      {open ? (
        <ul
          ref={listboxRef}
          id={listboxId}
          role="listbox"
          tabIndex={-1}
          aria-label={label}
          aria-activedescendant={options.length ? optionId(activeIndex) : undefined}
          className={styles.listbox}
          onKeyDown={onListboxKeyDown}
        >
          {options.map((option, index) => (
            <li
              key={option.value}
              id={optionId(index)}
              role="option"
              aria-selected={option.value === value}
              className={[styles.option, index === activeIndex ? styles.active : "", option.value === value ? styles.selected : ""]
                .filter(Boolean)
                .join(" ")}
              onMouseEnter={() => setActiveIndex(index)}
              onClick={() => commit(index)}
            >
              {option.label}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function ChevronGlyph({ className }: { className?: string }) {
  return (
    <svg className={className} width="10" height="10" viewBox="0 0 10 10" fill="none" aria-hidden="true">
      <path d="M2 3.5l3 3 3-3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
