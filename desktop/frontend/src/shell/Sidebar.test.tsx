import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

/*
 * Only `useTranscriptions` needs a double — Sidebar's active/queued counts
 * read from it, but this file's one guard doesn't care about their values.
 */
vi.mock("../lib", async () => {
  const actual = await vi.importActual<typeof import("../lib")>("../lib");
  return {
    ...actual,
    useTranscriptions: () => ({ transcriptions: [], loading: false }),
  };
});

import { Sidebar } from "./Sidebar";

// Naming guard: the sidebar wordmark must read the full product name,
// "HELIOS-ATS" — not the old "Helios" — so a future rename can't silently
// revert it. Collapsed state renders no wordmark at all (see Sidebar.tsx),
// so there is nothing narrower to guard here.
describe("Sidebar — wordmark names the product as HELIOS-ATS", () => {
  it("renders the full name when expanded", () => {
    render(<Sidebar screen="transcribe" onNavigate={vi.fn()} collapsed={false} />);

    expect(screen.getByText("HELIOS-ATS")).toBeInTheDocument();
  });

  it("renders no wordmark at all when collapsed", () => {
    render(<Sidebar screen="transcribe" onNavigate={vi.fn()} collapsed={true} />);

    expect(screen.queryByText("HELIOS-ATS")).not.toBeInTheDocument();
  });
});
