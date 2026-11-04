import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Transcription } from "../../lib";
import { TranscribeScreen } from "./TranscribeScreen";

const state = vi.hoisted(() => ({
  transcriptions: [] as unknown[],
  hasMore: false,
  rows: [] as unknown[],
}));
vi.mock("../../lib", async (original) => ({
  ...(await original<typeof import("../../lib")>()),
  useTranscriptions: () => ({ transcriptions: state.transcriptions, loading: false, hasMore: state.hasMore, loadingMore: false }),
  useConnection: () => ({ status: "online" }),
  transcriptionStore: { loadMore: vi.fn(), forget: vi.fn() },
}));
vi.mock("../../components", async (original) => ({
  ...(await original<typeof import("../../components")>()),
  useToast: () => ({ show: vi.fn() }),
}));
vi.mock("./Composer", () => ({ Composer: ({ variant }: { variant: string }) => <div data-testid="composer" data-variant={variant} /> }));
vi.mock("./useTranscribeQueue", async (original) => ({
  ...(await original<typeof import("./useTranscribeQueue")>()),
  useTranscribeQueue: () => ({
    rows: state.rows, loading: false, submit: vi.fn(), cancel: vi.fn(), retry: vi.fn(),
    isCanceling: () => false, isEntering: () => false, clearFinished: vi.fn(),
  }),
}));

const finished = (id: string): Transcription => ({
  id, status: "succeeded", createdAt: "2026-10-01T00:00:00Z", completedAt: "2026-10-02T00:00:00Z", updatedAt: "2026-10-02T00:00:00Z",
  options: { model: "", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
  audio: { filename: `${id}.wav`, mediaType: "audio/wav", sizeBytes: 1, durationSeconds: 1, sampleRateHz: 16000, channels: 1, codec: "pcm" },
  result: { text: "done", language: "en", durationSeconds: 1, model: "", task: "transcribe", segments: [] },
  attempts: 1, maxAttempts: 3,
} as Transcription);

beforeEach(() => {
  state.transcriptions = [];
  state.hasMore = false;
  state.rows = [];
});

describe("first-run composer", () => {
  it("fills the screen only when no row exists on the server", () => {
    render(<TranscribeScreen onOpenLibrary={vi.fn()} />);
    expect(screen.getByTestId("composer")).toHaveAttribute("data-variant", "expanded");
  });

  it("stays docked and keeps the load button after every visible row is cleared", () => {
    // The newest 100 rows are all succeeded and hidden. Older rows exist.
    state.transcriptions = [finished("a")];
    state.hasMore = true;
    state.rows = [];
    render(<TranscribeScreen onOpenLibrary={vi.fn()} />);
    expect(screen.getByTestId("composer")).toHaveAttribute("data-variant", "collapsed");
    expect(screen.getByRole("button", { name: "Load older transcriptions" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /1\+ items in Library/ })).toBeInTheDocument();
  });
});
