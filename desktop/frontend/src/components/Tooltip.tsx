import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useRef,
  useState,
  type FocusEvent,
  type MouseEvent,
  type ReactElement,
} from "react";
import styles from "./Tooltip.module.css";

export interface TooltipProps {
  content: string;
  /** Single focusable/interactive element; receives `aria-describedby` and the hover/focus handlers. */
  children: ReactElement;
  /** `top` by default; flip when the trigger sits near a window edge. */
  side?: "top" | "bottom" | "left" | "right";
}

// A tooltip that is already open anywhere in the app suppresses the open
// delay for every other tooltip until it closes — DESIGN.md §6.12.
let openTooltipCount = 0;

/**
 * Tooltip — DESIGN.md §6.12. 400ms open delay (0ms if another tooltip is
 * already open), no delay on keyboard focus, `role="tooltip"` +
 * `aria-describedby`. Required on every IconButton and every truncated
 * title.
 */
export function Tooltip({ content, children, side = "top" }: TooltipProps) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const viaKeyboardRef = useRef(false);

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
      if (open) openTooltipCount = Math.max(0, openTooltipCount - 1);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function show(delayMs: number) {
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => {
      openTooltipCount += 1;
      setOpen(true);
    }, delayMs);
  }

  function hide() {
    if (timerRef.current) clearTimeout(timerRef.current);
    if (open) {
      openTooltipCount = Math.max(0, openTooltipCount - 1);
      setOpen(false);
    }
  }

  if (!isValidElement(children)) return children;

  const child = children as ReactElement<Record<string, unknown>>;

  return (
    <span className={styles.wrapper}>
      {cloneElement(child, {
        "aria-describedby": open ? id : undefined,
        onMouseEnter: (e: MouseEvent) => {
          (child.props.onMouseEnter as ((e: MouseEvent) => void) | undefined)?.(e);
          viaKeyboardRef.current = false;
          show(openTooltipCount > 0 ? 0 : 400);
        },
        onMouseLeave: (e: MouseEvent) => {
          (child.props.onMouseLeave as ((e: MouseEvent) => void) | undefined)?.(e);
          hide();
        },
        onFocus: (e: FocusEvent) => {
          (child.props.onFocus as ((e: FocusEvent) => void) | undefined)?.(e);
          viaKeyboardRef.current = true;
          show(0);
        },
        onBlur: (e: FocusEvent) => {
          (child.props.onBlur as ((e: FocusEvent) => void) | undefined)?.(e);
          hide();
        },
      })}
      {open ? (
        <span role="tooltip" id={id} className={[styles.tooltip, styles[side]].join(" ")}>
          {content}
        </span>
      ) : null}
    </span>
  );
}
