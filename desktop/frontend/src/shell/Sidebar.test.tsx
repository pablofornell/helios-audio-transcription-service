import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

/*
 * Sidebar's list and storage requests are mocked at the adapter boundary.
 */
const freeStorageBytes = vi.hoisted(() => vi.fn());
vi.mock("../lib", async () => {
  const actual = await vi.importActual<typeof import("../lib")>("../lib");
  return {
    ...actual,
    useTranscriptions: () => ({ transcriptions: [], loading: false }),
    freeStorageBytes,
  };
});

it("shows server free space and an unavailable state after a read failure", async () => {
  freeStorageBytes.mockResolvedValueOnce(10 * 1024 ** 3).mockRejectedValueOnce(new Error("offline"));
  const health = { ok: true, checks: { storage: { ok: true, detail: "ok" } } };
  const view = render(<Sidebar screen="transcribe" onNavigate={vi.fn()} collapsed={false} health={health} />);
  await waitFor(() => expect(screen.getByText("10.7 GB free")).toBeInTheDocument());
  view.rerender(<Sidebar screen="transcribe" onNavigate={vi.fn()} collapsed={false} health={{ ...health }} />);
  await waitFor(() => expect(screen.getByText("Unavailable")).toBeInTheDocument());
});

it("lets keyboard users inspect server space in the collapsed sidebar", async () => {
  freeStorageBytes.mockResolvedValue(5 * 1024 ** 3);
  render(<Sidebar screen="transcribe" onNavigate={vi.fn()} collapsed health={{ ok: true, checks: {} }} />);
  const storage = await screen.findByRole("region", { name: "Server storage: 5.4 GB free" });
  const user = userEvent.setup();
  await user.tab();
  await user.tab();
  await user.tab();
  expect(storage).toHaveFocus();
  await waitFor(() => expect(screen.getByRole("tooltip")).toHaveTextContent("Server storage: 5.4 GB free"));
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
