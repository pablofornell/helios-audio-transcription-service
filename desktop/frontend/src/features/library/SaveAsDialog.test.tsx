import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToastViewport } from "../../components";
import { chooseSaveDirectory, defaultSaveDirectory, saveTranscript } from "../../lib";
import { bumpServerSettingsVersion, getServerSettingsVersion } from "../../lib/serverSettingsSignal";
import { SaveAsDialog } from "./SaveAsDialog";

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
    defaultSaveDirectory: vi.fn(async () => "/downloads"),
    chooseSaveDirectory: vi.fn(async () => "/chosen"),
    saveTranscript: vi.fn(async () => "/saved.txt"),
  };
});

const row = (overrides: Partial<ReturnType<typeof baseRow>> = {}) => ({ ...baseRow(), ...overrides });

function baseRow() {
  return {
    id: "save-as-1",
    status: "succeeded" as const,
    options: { model: "large-v3-turbo-q5_0", language: "", task: "transcribe" as const, wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
    audio: { filename: "clip.mp3", mediaType: "audio/mpeg", sizeBytes: 1024, durationSeconds: 12, sampleRateHz: 16000, channels: 1, codec: "mp3" },
    result: { text: "Hello", segments: [], language: "en", durationSeconds: 12, model: "large-v3-turbo-q5_0", task: "transcribe" as const },
    attempts: 1,
    maxAttempts: 3,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
}

afterEach(() => {
  runtime.desktop = false; runtime.heldVersion = undefined;
  vi.clearAllMocks();
  vi.restoreAllMocks();
  window.localStorage.clear();
});

describe("SaveAsDialog in a browser tab", () => {
  it("offers only a format picker — no destination folder field, no folder picker button", () => {
    render(<SaveAsDialog transcription={row()} initialFormat="txt" onClose={() => {}} />);

    expect(screen.getByRole("radiogroup", { name: "Format" })).toBeInTheDocument();
    expect(screen.queryByLabelText(/destination folder/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /choose folder/i })).not.toBeInTheDocument();
    // The desktop "Save" label never shows here — this is a download, not a
    // filesystem write.
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toBeInTheDocument();
  });

  it("Download launches a same-origin native URL for the selected format", () => {
    let capturedHref: string | null = null;
    let capturedDownload: string | null = null;
    let capturedTarget: string | null = null;
    let capturedRel: string | null = null;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      capturedHref = this.getAttribute("href");
      capturedDownload = this.getAttribute("download");
      capturedTarget = this.getAttribute("target");
      capturedRel = this.getAttribute("rel");
    });

    render(<SaveAsDialog transcription={row({ id: "dl-1" })} initialFormat="srt" onClose={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(capturedHref).toBe("/api/v1/transcriptions/dl-1/transcript?format=srt&download=1");
    // Cross-origin `download` filename hints are ignored by browsers
    // anyway — the attribute's presence is what matters here, not its
    // value; the server's Content-Disposition names the file.
    expect(capturedDownload).toBe("");
    expect(capturedTarget).toBeNull();
    expect(capturedRel).toBeNull();
  });

  it("closes the dialog once the download has been triggered", () => {
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const onClose = vi.fn();

    render(<SaveAsDialog transcription={row({ id: "close-1" })} initialFormat="txt" onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("toasts that the download was requested, not that it definitely succeeded — this component cannot observe native download completion", () => {
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    render(
      <>
        <ToastViewport />
        <SaveAsDialog transcription={row()} initialFormat="vtt" onClose={() => {}} />
      </>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(screen.getByText("VTT download requested")).toBeInTheDocument();
  });
});

  it("does not offer a download for a failed row or a missing result", () => {
    const { rerender } = render(<SaveAsDialog transcription={row({ status: "failed" as never })} initialFormat="txt" onClose={() => {}} />);
    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
    rerender(<SaveAsDialog transcription={row({ result: undefined as never })} initialFormat="txt" onClose={() => {}} />);
    expect(screen.getByRole("button", { name: "Download" })).toBeDisabled();
  });

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
it("guards desktop saves before state flush and fences completion after switching items", async () => {
  runtime.desktop = true;
  const pending = deferred<string>();
  vi.mocked(saveTranscript).mockReturnValueOnce(pending.promise);
  const close = vi.fn();
  const { rerender } = render(<SaveAsDialog transcription={row({ id: "old" })} initialFormat="txt" onClose={close} />);
  const field = screen.getByLabelText("Destination folder");
  await waitFor(() => expect(field).toHaveValue("/downloads"));
  act(() => {
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  expect(saveTranscript).toHaveBeenCalledTimes(1);
  rerender(<SaveAsDialog transcription={row({ id: "new" })} initialFormat="srt" onClose={close} />);
  await act(async () => pending.resolve("/old.txt"));
  expect(close).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
});
it("fences an old folder picker and prefill after the server changes", async () => {
  runtime.desktop = true;
  const prefill = deferred<string>();
  const picker = deferred<string>();
  vi.mocked(defaultSaveDirectory).mockReturnValueOnce(prefill.promise);
  vi.mocked(chooseSaveDirectory).mockReturnValueOnce(picker.promise);
  render(<SaveAsDialog transcription={row()} initialFormat="txt" onClose={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: /choose folder/i }));
  act(() => bumpServerSettingsVersion());
  await waitFor(() => expect(screen.getByLabelText("Destination folder")).toHaveValue("/downloads"));
  await act(async () => { prefill.resolve("/old-default"); picker.resolve("/old-folder"); });
  expect(screen.getByLabelText("Destination folder")).toHaveValue("/downloads");
});
it("does not close a desktop save after unmount", async () => {
  runtime.desktop = true;
  const pending = deferred<string>();
  vi.mocked(saveTranscript).mockReturnValueOnce(pending.promise);
  const close = vi.fn();
  const { unmount } = render(<SaveAsDialog transcription={row()} initialFormat="txt" onClose={close} />);
  await waitFor(() => expect(screen.getByLabelText("Destination folder")).toHaveValue("/downloads"));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  unmount();
  await act(async () => pending.resolve("/old.txt"));
  expect(close).not.toHaveBeenCalled();
});

it("ignores a pending desktop save error after changing servers", async () => {
  runtime.desktop = true;
  const pending = deferred<string>();
  vi.mocked(saveTranscript).mockReturnValueOnce(pending.promise);
  const close = vi.fn();
  render(<><ToastViewport /><SaveAsDialog transcription={row()} initialFormat="txt" onClose={close} /></>);
  await waitFor(() => expect(screen.getByLabelText("Destination folder")).toHaveValue("/downloads"));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  // Hold the rendered snapshot to exercise completion before React commits the server change.
  runtime.heldVersion = getServerSettingsVersion();
  await act(async () => {
    bumpServerSettingsVersion();
    pending.reject(new Error("Old server failure"));
    await pending.promise.catch(() => {});
  });
  expect(close).not.toHaveBeenCalled();
  expect(screen.queryByText(/Old server failure/)).not.toBeInTheDocument();
  runtime.heldVersion = undefined;
  act(() => bumpServerSettingsVersion());
  await waitFor(() => expect(screen.getByRole("button", { name: "Save" })).toBeEnabled());
});

it("fences a desktop save resolved in the same turn as the server switch", async () => {
  runtime.desktop = true;
  const pending = deferred<string>();
  vi.mocked(saveTranscript).mockReturnValueOnce(pending.promise);
  const close = vi.fn();
  render(<><ToastViewport /><SaveAsDialog transcription={row()} initialFormat="txt" onClose={close} /></>);
  await waitFor(() => expect(screen.getByLabelText("Destination folder")).toHaveValue("/downloads"));
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  // Hold the rendered snapshot to exercise completion before React commits the server change.
  runtime.heldVersion = getServerSettingsVersion();
  await act(async () => {
    bumpServerSettingsVersion();
    pending.resolve("/old-server.txt");
    await pending.promise;
  });
  expect(close).not.toHaveBeenCalled();
  expect(screen.queryByText("Saved to /old-server.txt")).not.toBeInTheDocument();
});
it("fences folder and prefill responses before the server render commits", async () => {
  runtime.desktop = true;
  const prefill = deferred<string>();
  const picker = deferred<string>();
  vi.mocked(defaultSaveDirectory).mockReturnValueOnce(prefill.promise);
  vi.mocked(chooseSaveDirectory).mockReturnValueOnce(picker.promise);
  render(<SaveAsDialog transcription={row()} initialFormat="txt" onClose={() => {}} />);
  fireEvent.click(screen.getByRole("button", { name: /choose folder/i }));
  runtime.heldVersion = getServerSettingsVersion();
  await act(async () => {
    bumpServerSettingsVersion();
    prefill.resolve("/old-default");
    picker.resolve("/old-folder");
    await Promise.all([prefill.promise, picker.promise]);
  });
  expect(screen.getByLabelText("Destination folder")).toHaveValue("");
});
