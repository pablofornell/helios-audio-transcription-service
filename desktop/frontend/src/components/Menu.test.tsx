import { fireEvent, render, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { computeMenuPosition, Menu, type MenuItem } from "./Menu";

/** Mirrors LibraryScreen's real guard (LibraryScreen.tsx) — kept here as a
 * faithful stand-in rather than importing across the ingest/library
 * boundary, which is out of this component's scope. */
function isWithinOwnedWidget(el: Element | null): boolean {
  return Boolean(el?.closest('[role="listbox"], [role="menu"], [role="dialog"]'));
}

function Harness({
  items,
  onClose = vi.fn(),
  parentKeyDown,
  position,
}: {
  items: MenuItem[];
  onClose?: () => void;
  parentKeyDown?: () => void;
  /** When set, mirrors a right-click context menu (`useContextMenu`'s anchoring) rather than the `⋯` trigger's. */
  position?: { x: number; y: number };
}) {
  const anchorRef = useRef<HTMLButtonElement>(null);
  return (
    <div onKeyDown={parentKeyDown}>
      <button ref={anchorRef}>Trigger</button>
      <Menu items={items} open onClose={onClose} anchorRef={position ? undefined : anchorRef} position={position} />
    </div>
  );
}

// Reviewer finding: focus sits on the `<ul role="menu">`, but nothing ever
// told a screen reader which item `activeIndex` pointed at — arrowing to
// "Remove" and pressing Enter was silent, so a user could commit a
// destructive action without ever hearing its name.
describe("Menu — active-descendant wiring", () => {
  it("keeps aria-activedescendant pointed at a real menuitem id as arrow keys move, and Enter commits that same item", () => {
    const onSelectRemove = vi.fn();
    const items: MenuItem[] = [
      { id: "retry", label: "Retry", onSelect: vi.fn() },
      { id: "save", label: "Save a copy", onSelect: vi.fn() },
      { id: "remove", label: "Remove", onSelect: onSelectRemove, destructive: true },
    ];
    render(<Harness items={items} />);

    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowDown" });

    const activeId = menu.getAttribute("aria-activedescendant");
    expect(activeId).toBeTruthy();
    const activeItem = document.getElementById(activeId!);
    expect(activeItem).not.toBeNull();
    expect(activeItem).toHaveAttribute("role", "menuitem");
    expect(activeItem).toHaveTextContent("Remove");

    fireEvent.keyDown(menu, { key: "Enter" });
    expect(onSelectRemove).toHaveBeenCalledTimes(1);
  });

  it("stops Escape from propagating past the menu, so exactly one layer reacts per keypress (DESIGN.md §8)", () => {
    const parentKeyDown = vi.fn();
    const items: MenuItem[] = [{ id: "a", label: "Only item", onSelect: vi.fn() }];
    render(<Harness items={items} parentKeyDown={parentKeyDown} />);

    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "Escape" });

    expect(parentKeyDown).not.toHaveBeenCalled();
  });

  // The concrete scenario DESIGN.md §8's ordering exists for: a row's ⋯
  // menu open over an open detail panel. One Escape must close only the
  // menu; a second, separate press then closes the panel. `detailPanel`/
  // `libraryScreen` below stand in for DetailPanel's own `document`
  // listener and LibraryScreen's `window` listener (LibraryScreen.tsx) —
  // real `document`/`window` listeners, not React handlers, since that is
  // what those two actually are and a stopPropagation() from a React
  // onKeyDown must still defeat them.
  it("closes only the menu on the first Escape, leaving a second press to close whatever is behind it", () => {
    const detailPanelDocumentListener = vi.fn();
    const libraryScreenWindowListener = vi.fn((e: KeyboardEvent) => {
      if (e.key === "Escape" && !isWithinOwnedWidget(document.activeElement)) libraryScreenWindowListener.reacted();
    }) as ReturnType<typeof vi.fn> & { reacted: ReturnType<typeof vi.fn> };
    libraryScreenWindowListener.reacted = vi.fn();
    document.addEventListener("keydown", detailPanelDocumentListener);
    window.addEventListener("keydown", libraryScreenWindowListener);

    function StatefulHarness() {
      const [open, setOpen] = useState(true);
      const anchorRef = useRef<HTMLButtonElement>(null);
      const items: MenuItem[] = [{ id: "remove", label: "Remove", onSelect: vi.fn(), destructive: true }];
      return (
        <div>
          <button ref={anchorRef}>Trigger</button>
          {open ? <Menu items={items} open onClose={() => setOpen(false)} anchorRef={anchorRef} /> : null}
        </div>
      );
    }

    try {
      render(<StatefulHarness />);

      // First press: menu is open, so it — and only it — reacts.
      fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
      expect(detailPanelDocumentListener).not.toHaveBeenCalled();
      expect(libraryScreenWindowListener.reacted).not.toHaveBeenCalled();
      expect(screen.queryByRole("menu")).not.toBeInTheDocument();

      // Second press: the menu is gone, nothing intercepts it, so the
      // layer behind it (stood in for here) sees it normally.
      fireEvent.keyDown(document.body, { key: "Escape" });
      expect(detailPanelDocumentListener).toHaveBeenCalledTimes(1);
      expect(libraryScreenWindowListener.reacted).toHaveBeenCalledTimes(1);
    } finally {
      document.removeEventListener("keydown", detailPanelDocumentListener);
      window.removeEventListener("keydown", libraryScreenWindowListener);
    }
  });
});

// Bug report: the row `⋯` menu opens anchored to its trigger, which sits at
// the far right of every row by design — so the menu ran off the window's
// right edge for every row, with "Save…"/"Copy…"/"Delete" sliced mid-word.
// jsdom reports 0 for every layout measurement by default, so these stub
// `getBoundingClientRect` (on both the trigger and the menu itself, once
// mounted) and `window.innerWidth`/`innerHeight` to exercise the real flip/
// clamp math rather than proving nothing against all-zero geometry.
describe("Menu — viewport-aware positioning", () => {
  type Rect = { left: number; right: number; top: number; bottom: number };

  /** Stubs layout for the duration of a test; returns a restore function. */
  function stubLayout({
    anchorRect,
    menuSize,
    viewport,
  }: {
    anchorRect?: Rect;
    menuSize: { width: number; height: number };
    viewport: { width: number; height: number };
  }) {
    const rectSpy = vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this.tagName === "UL") {
        return {
          left: 0,
          right: menuSize.width,
          top: 0,
          bottom: menuSize.height,
          width: menuSize.width,
          height: menuSize.height,
          x: 0,
          y: 0,
          toJSON: () => ({}),
        } as DOMRect;
      }
      const r = anchorRect ?? { left: 0, right: 0, top: 0, bottom: 0 };
      return { ...r, width: r.right - r.left, height: r.bottom - r.top, x: r.left, y: r.top, toJSON: () => ({}) } as DOMRect;
    });
    const prevWidth = window.innerWidth;
    const prevHeight = window.innerHeight;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: viewport.width });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: viewport.height });
    return () => {
      rectSpy.mockRestore();
      Object.defineProperty(window, "innerWidth", { configurable: true, value: prevWidth });
      Object.defineProperty(window, "innerHeight", { configurable: true, value: prevHeight });
    };
  }

  const oneItem: MenuItem[] = [{ id: "a", label: "Only item", onSelect: vi.fn() }];

  it("flips to open leftward when the anchor sits too close to the right edge", () => {
    const restore = stubLayout({
      anchorRect: { left: 700, right: 730, top: 100, bottom: 120 },
      menuSize: { width: 200, height: 150 },
      viewport: { width: 800, height: 600 },
    });
    try {
      render(<Harness items={oneItem} />);
      const menu = screen.getByRole("menu");
      // Anchor's right edge (730) minus the menu's own width (200): opens
      // leftward from the trigger instead of running off the window.
      expect(menu.style.left).toBe("530px");
      expect(menu.style.top).toBe("124px"); // vertical unaffected: plenty of room below
    } finally {
      restore();
    }
  });

  it("flips to open upward when the anchor sits too close to the bottom edge", () => {
    const restore = stubLayout({
      anchorRect: { left: 100, right: 130, top: 520, bottom: 540 },
      menuSize: { width: 150, height: 200 },
      viewport: { width: 800, height: 600 },
    });
    try {
      render(<Harness items={oneItem} />);
      const menu = screen.getByRole("menu");
      expect(menu.style.left).toBe("100px"); // horizontal unaffected: plenty of room to the right
      // Anchor's top edge (520) minus the 4px gap minus the menu's own
      // height (200): opens upward from the trigger instead of running off
      // the window's bottom edge.
      expect(menu.style.top).toBe("316px");
    } finally {
      restore();
    }
  });

  it("clamps inside the viewport, never past either edge, when the window is too narrow for the menu to fit on either side", () => {
    const restore = stubLayout({
      menuSize: { width: 200, height: 100 },
      viewport: { width: 250, height: 600 },
    });
    try {
      render(<Harness items={oneItem} position={{ x: 100, y: 50 }} />);
      const menu = screen.getByRole("menu");
      const left = Number.parseFloat(menu.style.left);
      expect(left).toBeGreaterThanOrEqual(0);
      expect(left + 200).toBeLessThanOrEqual(250);
    } finally {
      restore();
    }
  });

  it("leaves a comfortably-placed anchor's position unchanged from today's behaviour", () => {
    const restore = stubLayout({
      anchorRect: { left: 300, right: 330, top: 200, bottom: 220 },
      menuSize: { width: 160, height: 120 },
      viewport: { width: 1200, height: 800 },
    });
    try {
      render(<Harness items={oneItem} />);
      const menu = screen.getByRole("menu");
      expect(menu.style.left).toBe("300px"); // anchor.left, same as the old fixed computeAnchorStyle
      expect(menu.style.top).toBe("224px"); // anchor.bottom + 4, same as before
    } finally {
      restore();
    }
  });

  // Unit coverage of the pure math underneath the component-level assertions
  // above, including the axes' boundary condition directly.
  describe("computeMenuPosition", () => {
    it("prefers the forward placement when it fits", () => {
      const anchor = { left: 10, right: 40, top: 10, bottom: 30 };
      expect(computeMenuPosition(anchor, { width: 100, height: 50 }, { width: 800, height: 600 }, 4)).toEqual({
        left: 10,
        top: 34,
      });
    });

    it("flips both axes when the forward placement doesn't fit but the flip does", () => {
      const anchor = { left: 700, right: 740, top: 500, bottom: 540 };
      expect(computeMenuPosition(anchor, { width: 200, height: 150 }, { width: 800, height: 600 }, 4)).toEqual({
        left: 540, // anchor.right - width
        top: 346, // anchor.top - gap - height
      });
    });

    it("clamps rather than letting a flip that still overflows run past the viewport", () => {
      // A point anchor (gap 0) near the left edge of a viewport too narrow
      // for the menu to sit on either side of it: neither "forward" (runs
      // off the right) nor "flipped" (runs negative off the left) fits.
      const anchor = { left: 20, right: 20, top: 20, bottom: 20 };
      const { left } = computeMenuPosition(anchor, { width: 300, height: 50 }, { width: 320, height: 600 }, 0);
      expect(left).toBe(12); // viewport(320) - margin(8) - width(300)
      expect(left).toBeGreaterThanOrEqual(0);
      expect(left + 300).toBeLessThanOrEqual(320);
    });
  });
});
