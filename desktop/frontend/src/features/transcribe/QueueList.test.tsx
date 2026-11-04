import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ComponentProps } from "react";
import { HeliosError, type Status, type Transcription } from "../../lib";
import { QueueList } from "./QueueList";

const mocks = vi.hoisted(() => ({ deleteTranscription: vi.fn(), forget: vi.fn(), toast: vi.fn() }));
vi.mock("../../lib", async (original) => ({
  ...(await original<typeof import("../../lib")>()),
  deleteTranscription: mocks.deleteTranscription,
  transcriptionStore: { forget: mocks.forget },
}));
vi.mock("../../components", async (original) => ({
  ...(await original<typeof import("../../components")>()),
  useToast: () => ({ show: mocks.toast }),
}));
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
const failedWith = (id: string, code: "model_unavailable" | "disk_full"): QueueRowModel => {
  const base = row(id, "failed", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z");
  if (base.kind !== "real") throw new Error("expected a real row");
  return { ...base, transcription: { ...base.transcription, error: { code, message: "needs `make models`, then retry the job" } } };
};
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

describe("queue with hidden rows", () => {
  it("keeps the list and the load button when no row is visible but older rows exist", () => {
    const onLoadMore = vi.fn();
    renderRows([], { hasMore: true, hasHistory: true, onLoadMore, libraryCount: 100, libraryCountIsLowerBound: true });
    expect(screen.getByText(/Queue is clear/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load older transcriptions" }));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
  });
  it("keeps the list when rows exist on the server but all are hidden", () => {
    renderRows([], { hasHistory: true });
    expect(screen.getByText(/Queue is clear/)).toBeInTheDocument();
  });
  it("renders nothing when no row exists anywhere", () => {
    const { container } = render(<QueueList rows={[]} loading={false} connectionStale={false} isCanceling={() => false} isEntering={() => false}
      cancel={vi.fn()} retry={vi.fn()} clearFinished={vi.fn()} libraryCount={0} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("removing a row", () => {
  async function remove() {
    renderRows([row("failed-one", "failed", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z")]);
    fireEvent.click(screen.getByRole("button", { name: "More actions" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Remove" }));
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove" }));
  }
  beforeEach(() => vi.clearAllMocks());
  it("forgets the row after a delete", async () => {
    mocks.deleteTranscription.mockResolvedValueOnce(undefined);
    await remove();
    await vi.waitFor(() => expect(mocks.forget).toHaveBeenCalledWith("failed-one"));
    expect(mocks.toast).not.toHaveBeenCalled();
  });
  it("forgets the row and shows no error when the server says not_found", async () => {
    mocks.deleteTranscription.mockRejectedValueOnce(new HeliosError("not_found", "gone"));
    await remove();
    await vi.waitFor(() => expect(mocks.forget).toHaveBeenCalledWith("failed-one"));
    expect(mocks.toast).not.toHaveBeenCalled();
  });
  it("keeps the row and shows an error for another failure", async () => {
    mocks.deleteTranscription.mockRejectedValueOnce(new HeliosError("internal", "boom"));
    await remove();
    await vi.waitFor(() => expect(mocks.toast).toHaveBeenCalledTimes(1));
    expect(mocks.forget).not.toHaveBeenCalled();
  });
});

describe("retry control", () => {
  it("offers a manual Retry for a failed model_unavailable row and calls retry with its id", () => {
    const retry = vi.fn();
    renderRows([failedWith("missing-model", "model_unavailable")], { retry });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledWith("missing-model");
  });
  it("offers no Retry for a failed disk_full row", () => {
    renderRows([failedWith("full", "disk_full")]);
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });
});
