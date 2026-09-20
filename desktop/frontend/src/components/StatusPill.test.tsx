import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusPill } from "./StatusPill";

// Reviewer finding: role="status" on every pill created an announcement
// storm — five rows moving pending -> running -> succeeded fired up to
// fifteen uncoordinated polite announcements, on top of the one coalesced
// sentence DESIGN.md §8 asks for, and each `succeeded` was announced twice
// since the pill is also its row's aria-describedby target. DESIGN.md §8
// puts role="status" on exactly one region, at the screen root
// (ConnectionBar already has it correctly).
describe("StatusPill", () => {
  it("does not carry its own role=status live region", () => {
    render(<StatusPill status="succeeded" />);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("still renders the always-present text label, since aria-describedby (wired by the row, not this component) needs real text to read", () => {
    render(<StatusPill status="failed" />);
    expect(screen.getByText("Failed")).toBeInTheDocument();
  });
});
