import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import styles from "./Dialog.module.css";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** 480px (Delete confirm) by default; 560px for Settings. */
  size?: "sm" | "lg";
  /**
   * Ref to the control that should receive focus when the dialog opens.
   * DESIGN.md §6.11: initial focus goes to the SAFE button, never the
   * destructive one.
   */
  initialFocusRef?: React.RefObject<HTMLElement | null>;
}

/**
 * Dialog — DESIGN.md §6.11. There are exactly two uses of this in the app
 * (Delete confirm, Settings). Scrim fades in over `--dur-fast`, the panel
 * scales 0.98 -> 1 with an 8px rise over `--dur-base`, focus is trapped,
 * Esc closes, and focus returns to the trigger on close (the caller owns
 * the trigger ref and should refocus it in its own `onClose`, since Dialog
 * itself does not know which element opened it).
 *
 * Escape and Tab are handled on the panel's own `onKeyDown` (bubble phase),
 * not a `document` listener: focus is trapped inside the panel, so every
 * keydown while open bubbles up through it regardless, and handling it here
 * lets Escape call `stopPropagation()` and actually stop it — DESIGN.md §8's
 * "close detail → close menu → clear search → blur, in that order" only
 * holds if a dialog on top of a detail panel `stopPropagation()`s in a way
 * that keeps the event from ever reaching whatever document/window listener
 * the layer behind it owns, which a `document`-level listener of Dialog's
 * own cannot do (it fires as a same-node sibling, not a bubble-order
 * ancestor, so stopping it there would not stop the others).
 */
export function Dialog({ open, onClose, title, children, footer, size = "sm", initialFocusRef }: DialogProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  const titleId = useRef(`dialog-title-${Math.random().toString(36).slice(2)}`).current;

  useEffect(() => {
    if (!open) return;
    const toFocus = initialFocusRef?.current ?? panelRef.current?.querySelector<HTMLElement>("[data-dialog-safe-action]");
    (toFocus ?? panelRef.current)?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  if (!open) return null;

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      // Any component that handles Escape stops it propagating, so exactly
      // one layer reacts per press (DESIGN.md §8) — otherwise this closing
      // also dismisses whatever owns Escape behind it (a detail panel, the
      // search field).
      e.preventDefault();
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key === "Tab") trapFocus(e, panelRef.current);
  }

  return (
    <div className={styles.scrim} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={[styles.panel, styles[size]].join(" ")}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId} className={styles.title}>
          {title}
        </h2>
        <div className={styles.body}>{children}</div>
        {footer ? <div className={styles.footer}>{footer}</div> : null}
      </div>
    </div>
  );
}

function trapFocus(e: KeyboardEvent<HTMLDivElement>, container: HTMLElement | null) {
  if (!container) return;
  const focusable = Array.from(
    container.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((el) => el.offsetParent !== null);
  if (focusable.length === 0) return;
  const first = focusable[0]!;
  const last = focusable[focusable.length - 1]!;
  const active = document.activeElement;

  if (e.shiftKey && active === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}
