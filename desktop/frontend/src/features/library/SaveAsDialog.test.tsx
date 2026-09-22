import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToastViewport } from "../../components";
import { SaveAsDialog } from "./SaveAsDialog";

// No Wails runtime is ever stubbed in this suite (test/setup.ts never sets
// window.go — see its own doc), so `hasWailsRuntime()` is false throughout,
// exactly the state a plain browser tab is in — the same premise
// `../../lib/browser/api.test.ts`'s DetailPanel describe block relies on.

const row = (overrides: Partial<ReturnType<typeof baseRow>> = {}) => ({ ...baseRow(), ...overrides });

function baseRow() {
  return {
    id: "save-as-1",
    status: "succeeded" as const,
    options: { model: "base", language: "", task: "transcribe" as const, wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 },
    audio: { filename: "clip.mp3", mediaType: "audio/mpeg", sizeBytes: 1024, durationSeconds: 12, sampleRateHz: 16000, channels: 1, codec: "mp3" },
    attempts: 1,
    maxAttempts: 3,
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
  };
}

afterEach(() => {
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

  it("Download navigates a temporary <a download target=_blank rel=noopener> at transcriptDownloadUrl for the selected format, not saveTranscript's folder flow", () => {
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
    // A saved, cross-origin server ignores `download` entirely, so an
    // error response (404, unreachable server) would otherwise replace
    // this whole SPA tab — `target="_blank"` sends that failure to a
    // throwaway tab instead (see `triggerBrowserDownload`'s own doc).
    expect(capturedTarget).toBe("_blank");
    expect(capturedRel).toBe("noopener");
  });

  it("closes the dialog once the download has been triggered", () => {
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const onClose = vi.fn();

    render(<SaveAsDialog transcription={row()} initialFormat="txt" onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: "Download" }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("toasts that the download was requested, not that it definitely succeeded — this component cannot observe a target=_blank tab's outcome at all", () => {
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
