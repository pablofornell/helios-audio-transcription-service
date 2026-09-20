import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type RefObject,
} from "react";
import styles from "./Menu.module.css";

export interface MenuItem {
  id: string;
  label: string;
  onSelect: () => void;
  /** Rendered in `--st-failed`, at the bottom behind a separator (DESIGN.md §6.12). */
  destructive?: boolean;
  disabled?: boolean;
}

export interface MenuProps {
  items: MenuItem[];
  open: boolean;
  onClose: () => void;
  /** Anchors below this element (the ⋯ trigger) — mutually exclusive with `position`. */
  anchorRef?: RefObject<HTMLElement | null>;
  /** Anchors at a fixed viewport point (a right-click context menu). */
  position?: { x: number; y: number };
  /** Element to restore focus to on close — usually the same as `anchorRef.current` or the row that was right-clicked. */
  returnFocusRef?: RefObject<HTMLElement | null>;
}

const TYPEAHEAD_RESET_MS = 600;
/** Below the trigger, matching the old fixed `+ 4` offset — only used for the anchor-below-trigger case, not the context-menu-at-a-point case. */
const ANCHOR_GAP = 4;
/** Minimum breathing room from the viewport edge once flipping alone can't make the menu fit (`--space-2`). */
const VIEWPORT_MARGIN = 8;
/** Not yet measured: keep it out of the way of a flash of the eventual real position (DESIGN.md §6.12). */
const UNMEASURED_STYLE: CSSProperties = { position: "fixed", top: 0, left: 0, visibility: "hidden" };

interface Rect {
  left: number;
  right: number;
  top: number;
  bottom: number;
}

interface Size {
  width: number;
  height: number;
}

/**
 * Where the menu should sit given its own (already-measured) size, the rect
 * it's anchored to — a real element's rect for the `⋯` trigger, or a
 * zero-size rect at the pointer for a right-click — and the viewport.
 *
 * Horizontally, the menu's left edge prefers the anchor's left edge (it
 * opens rightward, same column as the trigger); vertically its top edge
 * prefers `anchor.bottom + gap` (it opens downward, below the trigger).
 * Either axis flips — right edge to `anchor.right`, or bottom edge to
 * `anchor.top - gap` — when the preferred side doesn't fit. If neither side
 * fits (a very narrow or short window), clamps inside the viewport with
 * `VIEWPORT_MARGIN` rather than letting either edge run past it.
 */
export function computeMenuPosition(
  anchor: Rect,
  menuSize: Size,
  viewport: Size,
  gap: number,
  margin = VIEWPORT_MARGIN,
): { left: number; top: number } {
  const left = resolveAxis(anchor.left, anchor.right, menuSize.width, viewport.width, margin);
  const top = resolveAxis(anchor.bottom + gap, anchor.top - gap, menuSize.height, viewport.height, margin);
  return { left, top };
}

/**
 * One axis of `computeMenuPosition`. `preferredStart` is where this edge
 * sits if it fits (e.g. `anchor.left`, or `anchor.bottom + gap`);
 * `flippedEnd` is the anchor edge the *opposite* edge of the menu aligns to
 * once flipped (e.g. `anchor.right`, or `anchor.top - gap`) — the flipped
 * start is then `flippedEnd - menuExtent`.
 */
function resolveAxis(preferredStart: number, flippedEnd: number, menuExtent: number, viewportExtent: number, margin: number): number {
  const fitsPreferred = preferredStart + menuExtent <= viewportExtent - margin;
  let position: number;
  if (fitsPreferred) {
    position = preferredStart;
  } else {
    const flipped = flippedEnd - menuExtent;
    position = flipped >= margin ? flipped : preferredStart;
  }
  // Clamp: covers both "the flip itself still overflows" above and a window
  // too narrow/short for the menu to fit either way at all.
  const maxPosition = viewportExtent - margin - menuExtent;
  if (maxPosition < margin) return margin;
  return Math.min(Math.max(position, margin), maxPosition);
}

/**
 * Menu — DESIGN.md §6.12. Used for both the row `⋯` dropdown and the
 * right-click context menu (same component, different anchor). Arrow keys
 * move, typeahead jumps, Esc restores focus to the trigger/`returnFocusRef`.
 * Destructive items render at the bottom, behind a separator.
 *
 * Same active-descendant pattern as `Select` (brought to parity after a
 * review found this component's DOM focus never moved off the `<ul>`, so a
 * screen reader announced "menu" once and then nothing else while arrowing):
 * DOM focus stays on the `role="menu"` list, which owns `aria-activedescendant`
 * pointing at the current `role="menuitem"`'s real `id`. Escape and Tab both
 * `preventDefault` and return focus to the trigger themselves — Escape
 * additionally `stopPropagation`s so a menu open over a dialog/detail panel
 * only ever dismisses itself (DESIGN.md §8).
 */
export function Menu({ items, open, onClose, anchorRef, position, returnFocusRef }: MenuProps) {
  const menuRef = useRef<HTMLUListElement>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const [menuStyle, setMenuStyle] = useState<CSSProperties>();
  const typeaheadRef = useRef("");
  const typeaheadTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const baseId = useId();
  const itemId = (index: number) => `${baseId}-item-${index}`;

  const enabledIndices = items.reduce<number[]>((acc, item, i) => {
    if (!item.disabled) acc.push(i);
    return acc;
  }, []);

  useEffect(() => {
    if (!open) return;
    setActiveIndex(enabledIndices[0] ?? 0);
    const id = requestAnimationFrame(() => menuRef.current?.focus());
    return () => cancelAnimationFrame(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(e: MouseEvent) {
      if (menuRef.current?.contains(e.target as Node)) return;
      close();
    }
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // Positioning happens after mount, once the menu's real size is known —
  // guessing from item count broke as soon as the label list changed. Runs
  // as a layout effect (not `useEffect`) so the corrected position commits
  // before the browser paints: the very first render below is `visibility:
  // hidden`, so there's nothing to flash even on the frame this measures.
  useLayoutEffect(() => {
    if (!open) {
      setMenuStyle(undefined);
      return;
    }
    const menuEl = menuRef.current;
    if (!menuEl || (!position && !anchorRef?.current)) return;
    const anchor: Rect = position
      ? { left: position.x, right: position.x, top: position.y, bottom: position.y }
      : anchorRef!.current!.getBoundingClientRect();
    const gap = position ? 0 : ANCHOR_GAP;
    const { width, height } = menuEl.getBoundingClientRect();
    const viewport = { width: window.innerWidth, height: window.innerHeight };
    setMenuStyle({ position: "fixed", ...computeMenuPosition(anchor, { width, height }, viewport, gap) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, position?.x, position?.y]);

  function close() {
    onClose();
    (returnFocusRef?.current ?? anchorRef?.current)?.focus();
  }

  function commit(index: number) {
    const item = items[index];
    if (!item || item.disabled) return;
    item.onSelect();
    close();
  }

  function moveTo(direction: 1 | -1) {
    if (enabledIndices.length === 0) return;
    const pos = enabledIndices.indexOf(activeIndex);
    const nextPos = (pos + direction + enabledIndices.length) % enabledIndices.length;
    setActiveIndex(enabledIndices[nextPos]!);
  }

  function typeahead(char: string) {
    if (typeaheadTimerRef.current) clearTimeout(typeaheadTimerRef.current);
    typeaheadRef.current += char.toLowerCase();
    typeaheadTimerRef.current = setTimeout(() => {
      typeaheadRef.current = "";
    }, TYPEAHEAD_RESET_MS);
    const match = items.findIndex((it) => !it.disabled && it.label.toLowerCase().startsWith(typeaheadRef.current));
    if (match >= 0) setActiveIndex(match);
  }

  function onKeyDown(e: KeyboardEvent<HTMLUListElement>) {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        moveTo(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        moveTo(-1);
        break;
      case "Home":
        e.preventDefault();
        if (enabledIndices[0] !== undefined) setActiveIndex(enabledIndices[0]);
        break;
      case "End":
        e.preventDefault();
        if (enabledIndices.length) setActiveIndex(enabledIndices[enabledIndices.length - 1]!);
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
        close();
        break;
      case "Tab":
        // preventDefault, not just close(): close() already moves focus to
        // the trigger, but without this the browser's own Tab traversal
        // still runs right after, carrying focus straight past the trigger
        // to whatever follows it in the DOM.
        e.preventDefault();
        close();
        break;
      default:
        if (e.key.length === 1) typeahead(e.key);
    }
  }

  if (!open) return null;

  return (
    <ul
      ref={menuRef}
      role="menu"
      tabIndex={-1}
      aria-activedescendant={items.length ? itemId(activeIndex) : undefined}
      className={styles.menu}
      style={menuStyle ?? UNMEASURED_STYLE}
      onKeyDown={onKeyDown}
    >
      {items.map((item, index) => (
        <li key={item.id} role="none">
          <div
            id={itemId(index)}
            role="menuitem"
            aria-disabled={item.disabled || undefined}
            className={[
              styles.item,
              index === activeIndex ? styles.active : "",
              item.destructive ? styles.destructive : "",
              item.disabled ? styles.disabled : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onMouseEnter={() => !item.disabled && setActiveIndex(index)}
            onClick={() => commit(index)}
          >
            {item.label}
          </div>
        </li>
      ))}
    </ul>
  );
}

/**
 * Convenience hook for a right-click context menu: attach `onContextMenu`
 * to any element, then render `<Menu {...menuProps} items={...} />`.
 */
export function useContextMenu(): {
  open: boolean;
  position: { x: number; y: number } | undefined;
  onContextMenu: (e: ReactMouseEvent) => void;
  close: () => void;
} {
  const [state, setState] = useState<{ open: boolean; position?: { x: number; y: number } }>({ open: false });

  return {
    open: state.open,
    position: state.position,
    onContextMenu: (e: ReactMouseEvent) => {
      e.preventDefault();
      setState({ open: true, position: { x: e.clientX, y: e.clientY } });
    },
    close: () => setState((s) => ({ ...s, open: false })),
  };
}
