import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
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
