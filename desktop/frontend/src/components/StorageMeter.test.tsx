import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StorageMeter } from "./StorageMeter";

describe("storage capacity meter", () => {
  it.each([[20, "healthy"], [10, "warning"], [9, "critical"], [0, "critical"]])("uses the free capacity threshold at %s percent", (free, tone) => {
    render(<StorageMeter freeBytes={free} totalBytes={100} />);
    expect(screen.getByRole("meter")).toHaveAttribute("aria-valuenow", String(free));
    expect(screen.getByRole("region").className).toContain(tone);
  });
  it("never derives capacity from formatted text or invalid numbers", () => {
    const { rerender } = render(<StorageMeter detail="20 GB free of 100 GB" />);
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
    rerender(<StorageMeter freeBytes={NaN} totalBytes={100} />);
    expect(screen.getByText("Unavailable")).toBeInTheDocument();
    rerender(<StorageMeter freeBytes={50} />);
    expect(screen.getByText("50 B free")).toBeInTheDocument();
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
  });
  it("exposes reading and meter when collapsed", () => {
    render(<StorageMeter freeBytes={50} totalBytes={100} collapsed />);
    expect(screen.getByRole("region")).toHaveAttribute("tabindex", "0");
    expect(screen.getByRole("meter")).toHaveAttribute("aria-valuetext", "50% free — 50 B free of 100 B");
  });
});
