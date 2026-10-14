import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { bumpServerSettingsVersion, HeliosError } from "../../lib";
import { Composer } from "./Composer";
import type { SubmitOutcome } from "./useTranscribeQueue";

const native = vi.hoisted(() => ({ choose: vi.fn(), available: vi.fn() }));
vi.mock("../../lib", async (original) => ({
  ...(await original<typeof import("../../lib")>()),
  chooseAudioFile: native.choose,
}));
vi.mock("../../lib/runtime", () => ({ hasWailsRuntime: native.available }));
beforeEach(() => {
  localStorage.clear();
  native.choose.mockReset();
  native.available.mockReturnValue(false);
});

function deferred() {
  let resolve!: (outcome: SubmitOutcome) => void;
  const promise = new Promise<SubmitOutcome>((yes) => { resolve = yes; });
  return { promise, resolve };
}
function upload(container: HTMLElement, filename: string) {
  fireEvent.change(container.querySelector('input[type="file"]')!, {
    target: { files: [new File(["audio"], filename, { type: "audio/wav" })] },
  });
}

describe("composer async feedback", () => {
  it("keeps a new server's upload busy and suppresses an old server's failure", async () => {
    const old = deferred();
    const current = deferred();
    const submit = vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const { container } = render(<Composer variant="collapsed" submit={submit} />);
    upload(container, "old.wav");
    expect(screen.getByText("Uploading…")).toBeInTheDocument();
    act(() => bumpServerSettingsVersion());
    expect(screen.queryByText("Uploading…")).not.toBeInTheDocument();
    upload(container, "current.wav");
    await act(async () => { old.resolve({ ok: false, error: new HeliosError("unknown", "stale failure") }); });
    expect(screen.queryByText("stale failure")).not.toBeInTheDocument();
    expect(screen.getByText("Uploading…")).toBeInTheDocument();
    await act(async () => { current.resolve({ ok: true, id: "current" }); });
    expect(screen.queryByText("Uploading…")).not.toBeInTheDocument();
  });

  it("prevents duplicate submits while an upload is in flight", () => {
    const submit = vi.fn().mockReturnValue(new Promise(() => {}));
    const { container } = render(<Composer variant="collapsed" submit={submit} />);
    upload(container, "one.wav");
    upload(container, "two.wav");
    expect(submit).toHaveBeenCalledTimes(1);
  });
});


describe("native file chooser lifecycle", () => {
  for (const end of ["unmount", "server change"] as const) {
    it(`does not submit a file selected after ${end}`, async () => {
      native.available.mockReturnValue(true);
      let resolve!: (path: string) => void;
      native.choose.mockReturnValue(new Promise<string>((yes) => { resolve = yes; }));
      const submit = vi.fn();
      const { unmount } = render(<Composer variant="collapsed" submit={submit} />);
      fireEvent.click(screen.getByRole("button", { name: /choose/i }));
      act(() => { if (end === "unmount") unmount(); else bumpServerSettingsVersion(); });
      await act(async () => { resolve("/old/audio.wav"); });
      expect(submit).not.toHaveBeenCalled();
    });
  }

  it("submits a current selection and prevents duplicate native dialogs", async () => {
    native.available.mockReturnValue(true);
    let resolve!: (path: string) => void;
    native.choose.mockReturnValue(new Promise<string>((yes) => { resolve = yes; }));
    const submit = vi.fn().mockResolvedValue({ ok: true, id: "accepted" });
    render(<Composer variant="collapsed" submit={submit} />);
    const choose = screen.getByRole("button", { name: /choose/i });
    fireEvent.click(choose);
    fireEvent.click(choose);
    expect(native.choose).toHaveBeenCalledTimes(1);
    await act(async () => { resolve("/current/audio.wav"); });
    expect(submit).toHaveBeenCalledWith({ kind: "path", path: "/current/audio.wav", name: "audio.wav" }, expect.any(Object));
  });
});


describe("supported transcription model", () => {
  it("offers only the server default and quantized turbo model", () => {
    render(<Composer variant="expanded" submit={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["Server default", "large-v3-turbo-q5_0"]);
  });

  it("submits with the default after restoring a retired saved model", () => {
    localStorage.setItem("helios.transcribe.composerOptions", JSON.stringify({ model: "base", language: "en" }));
    const submit = vi.fn().mockResolvedValue({ ok: true, id: "accepted" });
    const { container } = render(<Composer variant="collapsed" submit={submit} />);
    upload(container, "turbo.wav");
    expect(submit).toHaveBeenCalledWith(expect.any(Object), expect.objectContaining({ model: "", language: "en" }));
  });
});
