import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Dialog } from "./Dialog";

// Reviewer finding: pressing Escape to cancel a delete confirmation also
// closed the detail panel behind it, because neither component stopped the
// key event propagating. DESIGN.md §8 fixes the order ("close detail ->
// close menu -> clear search -> blur, in that order") — that only holds if
// exactly one layer reacts per press. This stands in for the detail panel
// (or any other feature-owned layer) that owns its own `document`-level
// Escape listener behind the dialog.
describe("Dialog — Escape propagation", () => {
  it("stops Escape from reaching a document-level Escape listener owned by whatever is open behind it", () => {
    const behindLayerEscape = vi.fn();
    document.addEventListener("keydown", behindLayerEscape);
    try {
      render(
        <div>
          <div>stand-in for an open detail panel</div>
          <Dialog open onClose={vi.fn()} title="Delete this item?">
            <p>Delete "video.mp4" and its file? This can't be undone.</p>
          </Dialog>
        </div>,
      );

      fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });

      expect(behindLayerEscape).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("keydown", behindLayerEscape);
    }
  });

  it("still closes itself on Escape", () => {
    const onClose = vi.fn();
    render(
      <Dialog open onClose={onClose} title="Delete this item?">
        <p>Body</p>
      </Dialog>,
    );

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });

    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
