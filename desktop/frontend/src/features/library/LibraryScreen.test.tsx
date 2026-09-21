import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LibraryScreen } from "./LibraryScreen";

// LibraryScreen's own backend call (the "View in Library" hand-off's
// get()) is mocked directly, the same pattern ServerSettingsSection.test.tsx
// uses for its module boundary. bumpServerSettingsVersion is left real
// (spread from actual) so this suite can trigger the same signal
// setServerSettings bumps after a save, without going through a whole
// ServerSettingsSection render.
const get = vi.hoisted(() => vi.fn());
vi.mock("../../lib", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib")>()),
  get: (...args: unknown[]) => get(...args),
}));

// DetailPanel pulls in audio playback, transcript fetching and more --
// none of that is what this suite is about. A stub standing in for it
// only needs to prove which transcription it currently holds, and that
// LibraryScreen's onClose reaches it.
vi.mock("./DetailPanel", () => ({
  DetailPanel: ({ transcription, onClose }: { transcription: { id: string }; onClose: () => void }) => (
    <div data-testid="detail-panel">
      <span data-testid="detail-id">{transcription.id}</span>
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

afterEach(() => {
  vi.clearAllMocks();
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

    const { bumpServerSettingsVersion } = await import("../../lib");
    act(() => {
      bumpServerSettingsVersion();
    });

    await waitFor(() => expect(screen.queryByTestId("detail-panel")).not.toBeInTheDocument());
  });
});
