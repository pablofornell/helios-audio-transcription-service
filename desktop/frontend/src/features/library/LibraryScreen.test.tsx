import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { bumpServerSettingsVersion } from "../../lib";
import { LibraryScreen } from "./LibraryScreen";

// LibraryScreen's own backend call (the "View in Library" hand-off's
// get()) is mocked directly, the same pattern ServerSettingsSection.test.tsx
// uses for its module boundary. bumpServerSettingsVersion is left real
// (spread from actual) so this suite can trigger the same signal
// setServerSettings bumps after a save, without going through a whole
// ServerSettingsSection render.
const { get, list } = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn() }));
vi.mock("../../lib", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib")>()),
  get: (...args: unknown[]) => get(...args),
  list: (...args: unknown[]) => list(...args),
}));

// DetailPanel pulls in audio playback, transcript fetching and more --
// none of that is what this suite is about. A stub standing in for it
// only needs to prove which transcription it currently holds, and that
// LibraryScreen's onClose reaches it.
vi.mock("./DetailPanel", () => ({
  DetailPanel: ({ transcription, onClose }: { transcription: { id: string; status: string }; onClose: () => void }) => (
    <div data-testid="detail-panel">
      <span data-testid="detail-id">{transcription.id}</span>
      <span data-testid="detail-status">{transcription.status}</span>
      <button onClick={onClose}>close</button>
    </div>
  ),
}));

const row = (id: string) => ({
  id,
  status: "succeeded",
  options: { model: "base", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
  audio: { filename: `${id}.wav`, mediaType: "audio/wav", sizeBytes: 1, durationSeconds: 1, sampleRateHz: 16000, channels: 1, codec: "pcm_s16le" },
  attempts: 1,
  maxAttempts: 3,
  createdAt: "2026-09-20T20:00:00Z",
  updatedAt: "2026-09-20T20:00:00Z",
});

beforeEach(() => {
  get.mockReset();
  list.mockReset();
  list.mockResolvedValue({ items: [] });
});

afterEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("LibraryScreen — server settings changes", () => {
  // Settings -> Server saving a new server: the open detail panel still
  // shows the old server's transcript while its own audio element switches
  // to the new server underneath it -- an id that no longer exists there.
  // Closing the panel is the simplest correct behavior.
  it("closes the open detail panel when the server settings change", async () => {
    get.mockResolvedValue(row("t-1"));

    render(<LibraryScreen openId="t-1" />);

    await waitFor(() => expect(screen.getByTestId("detail-id")).toHaveTextContent("t-1"));

    act(() => {
      bumpServerSettingsVersion();
    });

    await waitFor(() => expect(screen.queryByTestId("detail-panel")).not.toBeInTheDocument());
  });
});


describe("LibraryScreen detail refresh and request fencing", () => {
  it("updates the open detail when a polled row completes", async () => {
    vi.useFakeTimers();
    const active = { ...row("active"), status: "running" };
    get.mockResolvedValue(active);
    list.mockResolvedValueOnce({ items: [active] });
    render(<LibraryScreen openId="active" />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByTestId("detail-status")).toHaveTextContent("running");
    list.mockResolvedValue({ items: [{ ...row("active"), result: { text: "finished" } }] });
    await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
    expect(screen.getByTestId("detail-status")).toHaveTextContent("succeeded");
  });

  it("discards a detail response from the server before settings changed", async () => {
    let finish: (value: ReturnType<typeof row>) => void = () => {};
    get.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    list.mockResolvedValue({ items: [] });
    render(<LibraryScreen openId="old" />);
    await act(async () => bumpServerSettingsVersion());
    await act(async () => finish(row("old")));
    expect(screen.queryByTestId("detail-panel")).not.toBeInTheDocument();
  });

  it("discards an older open request after another id is opened", async () => {
    let finish: (value: ReturnType<typeof row>) => void = () => {};
    get.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    list.mockResolvedValue({ items: [] });
    const view = render(<LibraryScreen openId="old" />);
    get.mockResolvedValueOnce(row("new"));
    view.rerender(<LibraryScreen openId="new" />);
    await waitFor(() => expect(screen.getByTestId("detail-id")).toHaveTextContent("new"));
    await act(async () => finish(row("old")));
    expect(screen.getByTestId("detail-id")).toHaveTextContent("new");
  });
});


it("refreshes selected detail even when filename search hides its loaded row", async () => {
  vi.useFakeTimers();
  const active = { ...row("active"), status: "running" };
  get.mockResolvedValue(active);
  list.mockResolvedValueOnce({ items: [active] });
  render(<LibraryScreen openId="active" />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  fireEvent.change(screen.getByRole("textbox", { name: "Search transcriptions" }), { target: { value: "other-file" } });
  list.mockResolvedValue({ items: [{ ...row("active"), result: { text: "finished" } }] });
  await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
  expect(screen.getByTestId("detail-status")).toHaveTextContent("succeeded");
});

it("polls open detail independently when its status is excluded from loaded rows", async () => {
  vi.useFakeTimers();
  const active = { ...row("active"), status: "running" };
  get.mockResolvedValueOnce(active);
  list.mockResolvedValue({ items: [] });
  render(<LibraryScreen openId="active" />);
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
  expect(screen.getByTestId("detail-status")).toHaveTextContent("running");
  get.mockResolvedValue({ ...row("active"), result: { text: "finished" } });
  await act(async () => { await vi.advanceTimersByTimeAsync(1600); });
  expect(screen.getByTestId("detail-status")).toHaveTextContent("succeeded");
});


it("opens the handed-off detail after StrictMode replays mount effects", async () => {
  get.mockResolvedValue(row("active"));
  render(<StrictMode><LibraryScreen openId="active" /></StrictMode>);
  await waitFor(() => expect(screen.getByTestId("detail-id")).toHaveTextContent("active"));
});
