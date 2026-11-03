import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Select } from "./Select";

const OPTIONS = [
  { value: "a", label: "Alpha" },
  { value: "b", label: "Beta" },
];

// Coordinator ruling: a custom listbox ships only with full conformance.
// These three gaps were left over after the rest of Select got the full
// treatment.
describe("Select — remaining conformance gaps", () => {
  it("stops Escape from propagating past the listbox, so exactly one layer reacts per keypress (DESIGN.md §8)", () => {
    const parentKeyDown = vi.fn();
    render(
      <div onKeyDown={parentKeyDown}>
        <Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />
      </div>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Sort" }));
    fireEvent.keyDown(screen.getByRole("listbox"), { key: "Escape" });

    expect(parentKeyDown).not.toHaveBeenCalled();
  });

  it("returns focus to the trigger on Tab instead of letting it fall through to whatever follows in the DOM", () => {
    render(
      <div>
        <Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />
        <button>After</button>
      </div>,
    );

    const trigger = screen.getByRole("button", { name: "Sort" });
    fireEvent.click(trigger);
    fireEvent.keyDown(screen.getByRole("listbox"), { key: "Tab" });

    expect(document.activeElement).toBe(trigger);
  });

  it("does not point aria-activedescendant at a non-existent id when there are no options", () => {
    render(<Select options={[]} value="" onChange={vi.fn()} label="Sort" />);

    fireEvent.click(screen.getByRole("button", { name: "Sort" }));

    expect(screen.getByRole("listbox")).not.toHaveAttribute("aria-activedescendant");
  });
});

describe("Select — listbox placement", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  /** jsdom has no layout: fake the trigger and listbox rects, in a 1000x600 window. */
  function mockRects(trigger: Partial<DOMRect>, listbox: Partial<DOMRect>) {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1000);
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(600);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      return (this.getAttribute("role") === "listbox" ? listbox : trigger) as DOMRect;
    });
  }

  function open() {
    fireEvent.click(screen.getByRole("button", { name: "Sort" }));
    return screen.getByRole("listbox");
  }

  it("opens downward and left-aligned when it fits", () => {
    mockRects({ top: 100, bottom: 132, left: 100, right: 220 }, { width: 200, height: 240 });
    render(<Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />);

    const listbox = open();

    expect(listbox.className).not.toMatch(/above|alignEnd/);
    expect(listbox.style.maxHeight).toBe("");
  });

  it("flips above the trigger when there is more room there and it doesn't fit below", () => {
    mockRects({ top: 450, bottom: 482, left: 100, right: 220 }, { width: 200, height: 240 });
    render(<Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />);

    expect(open().className).toMatch(/above/);
  });

  it("aligns to the trigger's right edge when it would run past the window", () => {
    mockRects({ top: 100, bottom: 132, left: 850, right: 970 }, { width: 200, height: 100 });
    render(<Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />);

    expect(open().className).toMatch(/alignEnd/);
  });

  it("caps its height to the available room when it fits on neither side", () => {
    mockRects({ top: 280, bottom: 312, left: 100, right: 220 }, { width: 200, height: 400 });
    render(<Select options={OPTIONS} value="a" onChange={vi.fn()} label="Sort" />);

    const listbox = open();

    expect(listbox.className).not.toMatch(/above/);
    expect(listbox.style.maxHeight).toBe("276px");
  });
});
