import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ComponentProps } from "react";
import { type Status, type Transcription } from "../../lib";
import { QueueList } from "./QueueList";
import { type QueueRowModel } from "./useTranscribeQueue";
const row = (id: string, status: Status, createdAt: string, completedAt?: string): QueueRowModel => ({
  kind: "real", key: id, transcription: {
    id, status, createdAt, completedAt, updatedAt: createdAt,
    options: { model: "large-v3-turbo-q5_0", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
    audio: { filename: id, mediaType: "audio/wav", sizeBytes: 1, durationSeconds: 1, sampleRateHz: 16000, channels: 1, codec: "pcm" },
    attempts: 0, maxAttempts: 3,
  } satisfies Transcription,
});
function renderRows(rows: QueueRowModel[], extra: Partial<ComponentProps<typeof QueueList>> = {}) {
  render(<QueueList rows={rows} loading={false} connectionStale={false} isCanceling={() => false}
    isEntering={() => false} cancel={vi.fn()} retry={vi.fn()} clearFinished={vi.fn()} libraryCount={0} {...extra} />);
}
const names = (label: string) => within(screen.getByRole("list", { name: label })).getAllByRole("listitem")
  .map((item) => item.querySelector("[id$='-title']")?.textContent);
describe("queue rendered order", () => {
  it("shows running first, FIFO pending with microseconds and ID ties, then FIFO uploads", () => {
    renderRows([
      { kind: "uploading", key: "u2", name: "upload-new" }, { kind: "uploading", key: "u1", name: "upload-old" },
      row("new", "pending", "2026-10-01T00:00:00.000002Z"),
      row("b", "pending", "2026-10-01T00:00:00.000001Z"), row("a", "pending", "2026-10-01T00:00:00.000001Z"),
      row("running", "running", "2026-10-02T00:00:00Z"),
    ]);
    expect(names("Active transcriptions")).toEqual(["running", "a", "b", "new", "upload-old", "upload-new"]);
  });
  it("orders recent by completion with creation fallback and deterministic ties", () => {
    renderRows([
      row("older", "succeeded", "2026-10-03T00:00:00Z", "2026-10-04T00:00:00Z"),
      row("b", "failed", "2026-10-01T00:00:00Z", "2026-10-05T00:00:00Z"),
      row("fallback", "canceled", "2026-10-06T00:00:00Z"),
      row("a", "succeeded", "2026-10-02T00:00:00Z", "2026-10-05T00:00:00Z"),
    ]);
    expect(names("Recent transcriptions")).toEqual(["fallback", "a", "b", "older"]);
  });
});

describe("queue reach and actions", () => {
  it("shows a load button when older rows exist, and calls the loader", () => {
    const onLoadMore = vi.fn();
    renderRows([row("failed", "failed", "2026-10-01T00:00:00Z")], { hasMore: true, onLoadMore });
    fireEvent.click(screen.getByRole("button", { name: "Load older transcriptions" }));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });
  it("hides the load button when every row is loaded", () => {
    renderRows([row("failed", "failed", "2026-10-01T00:00:00Z")]);
    expect(screen.queryByRole("button", { name: "Load older transcriptions" })).not.toBeInTheDocument();
  });
  it("adds a plus to the library count only when it is a lower bound", () => {
    const rows = [row("done", "succeeded", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z")];
    const view = render(<QueueList rows={rows} loading={false} connectionStale={false} isCanceling={() => false} isEntering={() => false}
      cancel={vi.fn()} retry={vi.fn()} clearFinished={vi.fn()} libraryCount={100} libraryCountIsLowerBound onOpenLibrary={vi.fn()} />);
    expect(screen.getByRole("button", { name: /100\+ items in Library/ })).toBeInTheDocument();
    view.rerender(<QueueList rows={rows} loading={false} connectionStale={false} isCanceling={() => false} isEntering={() => false}
      cancel={vi.fn()} retry={vi.fn()} clearFinished={vi.fn()} libraryCount={100} onOpenLibrary={vi.fn()} />);
    expect(screen.getByRole("button", { name: /^100 items in Library/ })).toBeInTheDocument();
  });
  it("offers to clear completed rows only when a succeeded row exists", () => {
    renderRows([row("failed", "failed", "2026-10-01T00:00:00Z"), row("canceled", "canceled", "2026-10-01T00:00:00Z")]);
    expect(screen.queryByRole("button", { name: "Clear completed" })).not.toBeInTheDocument();
  });
  it("calls clearFinished from the clear completed button", () => {
    const clearFinished = vi.fn();
    renderRows([row("done", "succeeded", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z")], { clearFinished });
    fireEvent.click(screen.getByRole("button", { name: "Clear completed" }));
    expect(clearFinished).toHaveBeenCalledTimes(1);
  });
});
