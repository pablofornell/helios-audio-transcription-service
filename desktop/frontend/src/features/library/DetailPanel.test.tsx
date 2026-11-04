import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { openAudio, revealInFileManager, transcript, type Transcription } from "../../lib";
import { bumpServerSettingsVersion, getServerSettingsVersion } from "../../lib/serverSettingsSignal";
import { ToastViewport } from "../../components";
import { DetailPanel } from "./DetailPanel";

const runtime = vi.hoisted(() => ({ desktop: false, heldVersion: undefined as number | undefined }));
vi.mock("../../lib/runtime", () => ({ hasWailsRuntime: () => runtime.desktop }));
vi.mock("../../lib", async (original) => {
  const actual = await original<typeof import("../../lib")>();
  return {
    ...actual,
    useServerSettingsVersion: () => {
      const version = actual.useServerSettingsVersion();
      return runtime.heldVersion ?? version;
    },
    openAudio: vi.fn(async () => {}),
    revealInFileManager: vi.fn(async () => {}),
    audioLocation: vi.fn(async () => ({ exists: false })),
    getServerSettings: vi.fn(async () => ({ baseUrl: "http://localhost:3000" })),
    transcript: vi.fn(async () => "Transcript text"),
  };
});
afterEach(() => { runtime.desktop = false; runtime.heldVersion = undefined; vi.clearAllMocks(); });
const row: Transcription = {
  id: "detail-source", status: "pending", attempts: 0, maxAttempts: 3,
  options: { model: "large-v3-turbo-q5_0", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
  audio: { filename: "clip.mp3", mediaType: "audio/mpeg", sizeBytes: 1024, durationSeconds: 0, sampleRateHz: 0, channels: 0, codec: "" },
  createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z",
};
const panel = (transcription: Transcription) => <DetailPanel transcription={transcription} onClose={() => {}} onRetry={() => {}} onSaveAs={() => {}} onDelete={() => {}} overlay={false} />;
it.each(["pending", "running", "failed", "canceled", "succeeded"] as const)("keeps uploaded audio available for %s jobs without an unavailable transcript", async (status) => {
  const { container } = render(panel({ ...row, status }));
  expect(container.querySelector("audio")).not.toBeNull();
  expect(await screen.findByRole("button", { name: "Download audio" })).toBeEnabled();
  expect(screen.queryByRole("button", { name: "Save as…" })).not.toBeInTheDocument();
  expect(transcript).not.toHaveBeenCalled();
});
it("hides audio preview and download when source metadata is absent", async () => {
  const { container } = render(panel({ ...row, audio: { ...row.audio, sizeBytes: 0 } }));
  await act(async () => {});
  expect(container.querySelector("audio")).toBeNull();
  expect(screen.queryByRole("button", { name: "Download audio" })).not.toBeInTheDocument();
});
it("invalidates transcript text when the saved server changes", async () => {
  const result = { text: "First", language: "en", model: "large-v3-turbo-q5_0", task: "transcribe" as const, durationSeconds: 1, segments: [] };
  vi.mocked(transcript).mockResolvedValueOnce("First server text").mockResolvedValueOnce("Second server text");
  render(panel({ ...row, status: "succeeded", result }));
  expect(await screen.findByText("First server text")).toBeInTheDocument();
  act(() => bumpServerSettingsVersion());
  await waitFor(() => expect(screen.getByText("Second server text")).toBeInTheDocument());
  expect(screen.queryByText("First server text")).not.toBeInTheDocument();
  expect(transcript).toHaveBeenCalledTimes(2);
});

function pendingOperation() {
  let resolve!: () => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const mediaActions = [
  { label: "Download and open", invoke: openAudio },
  { label: "Download and reveal", invoke: revealInFileManager },
];
it.each(mediaActions)("fences $label success and busy cleanup after switching items", async ({ label, invoke }) => {
  runtime.desktop = true;
  const old = pendingOperation();
  const next = pendingOperation();
  vi.mocked(invoke).mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
  const { rerender } = render(panel({ ...row, id: "old-item" }));
  fireEvent.click(await screen.findByRole("button", { name: label }));
  rerender(panel({ ...row, id: "new-item" }));
  const button = await screen.findByRole("button", { name: label });
  expect(button).toBeEnabled();
  fireEvent.click(button);
  await act(async () => old.resolve());
  expect(button).toBeDisabled();
  expect(button).toHaveTextContent(label);
  expect(invoke).toHaveBeenNthCalledWith(1, "old-item");
  expect(invoke).toHaveBeenNthCalledWith(2, "new-item");
  await act(async () => next.resolve());
  expect(screen.getByRole("button", { name: label === "Download and open" ? "Open" : "Reveal in file manager" })).toBeEnabled();
});
it.each(mediaActions)("fences $label errors after changing servers or unmounting", async ({ label, invoke }) => {
  runtime.desktop = true;
  const old = pendingOperation();
  const next = pendingOperation();
  vi.mocked(invoke).mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise);
  const { rerender } = render(<><ToastViewport />{panel(row)}</>);
  fireEvent.click(await screen.findByRole("button", { name: label }));
  // Hold the rendered snapshot to exercise completion before React commits the server change.
  runtime.heldVersion = getServerSettingsVersion();
  await act(async () => {
    bumpServerSettingsVersion();
    old.reject(new Error("Old server failure"));
    await old.promise.catch(() => {});
  });
  runtime.heldVersion = undefined;
  act(() => bumpServerSettingsVersion());
  await waitFor(() => expect(screen.getByRole("button", { name: label })).toBeEnabled());
  expect(screen.queryByText("Old server failure")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: label }));
  rerender(<ToastViewport />);
  await act(async () => next.reject(new Error("Unmounted operation failure")));
  expect(screen.queryByText("Unmounted operation failure")).not.toBeInTheDocument();
});
it.each(mediaActions)("fences $label success before the server render commits", async ({ label, invoke }) => {
  runtime.desktop = true;
  const old = pendingOperation();
  vi.mocked(invoke).mockReturnValueOnce(old.promise);
  render(panel(row));
  fireEvent.click(await screen.findByRole("button", { name: label }));
  runtime.heldVersion = getServerSettingsVersion();
  await act(async () => {
    bumpServerSettingsVersion();
    old.resolve();
    await old.promise;
  });
  expect(screen.getByRole("button", { name: label })).toBeDisabled();
});


it("keeps historical model names and completed transcripts accessible", async () => {
  const legacy: Transcription = {
    ...row, status: "succeeded", options: { ...row.options, model: "base" },
    result: { text: "Historical transcript", language: "en", model: "base", task: "transcribe", durationSeconds: 1, segments: [] },
  };
  vi.mocked(transcript).mockResolvedValueOnce("Historical transcript");
  render(panel(legacy));
  expect(await screen.findByText("Historical transcript")).toBeInTheDocument();
  expect(screen.getByText("base · English")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save as…" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "Download audio" })).toBeEnabled();
});

it("shows the detected and the output language, not the requested one", async () => {
  const translated: Transcription = {
    ...row, status: "succeeded", options: { ...row.options, language: "de" }, // the hint is ignored
    result: { text: "Hello", language: "fr", outputLanguage: "en", model: "large-v3-q5_0", task: "translate", durationSeconds: 1, segments: [] },
  };
  vi.mocked(transcript).mockResolvedValueOnce("Hello");
  render(panel(translated));
  expect(await screen.findByText("large-v3-q5_0 · French → English")).toBeInTheDocument();
  expect(screen.queryByText(/German/)).not.toBeInTheDocument();
});

it("offers Try again for a failed model_unavailable job and calls onRetry", async () => {
  const onRetry = vi.fn();
  const failed: Transcription = { ...row, status: "failed", error: { code: "model_unavailable", message: "Run `make models`, then retry the job." } };
  render(<DetailPanel transcription={failed} onClose={() => {}} onRetry={onRetry} onSaveAs={() => {}} onDelete={() => {}} overlay={false} />);
  fireEvent.click(await screen.findByRole("button", { name: "Try again" }));
  expect(onRetry).toHaveBeenCalledWith("detail-source");
});
it("offers no Try again for a failed disk_full job", async () => {
  const failed: Transcription = { ...row, status: "failed", error: { code: "disk_full", message: "full" } };
  render(panel(failed));
  await act(async () => {});
  expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
});
