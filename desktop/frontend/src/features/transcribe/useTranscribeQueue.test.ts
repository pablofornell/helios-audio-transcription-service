import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { bumpServerSettingsVersion, type Options, type Transcription } from "../../lib";
import { useTranscribeQueue } from "./useTranscribeQueue";
const mocks = vi.hoisted(() => ({ submit: vi.fn(), cancel: vi.fn(), retry: vi.fn(), refresh: vi.fn(), toast: vi.fn() }));
vi.mock("../../lib", async (original) => ({
  ...(await original<typeof import("../../lib")>()), submit: mocks.submit, cancel: mocks.cancel, retry: mocks.retry,
  useTranscriptions: () => ({ transcriptions: [], loading: false }), transcriptionStore: { refreshNow: mocks.refresh },
}));
vi.mock("../../components", () => ({ useToast: () => ({ show: mocks.toast }) }));
const options: Options = { model: "", language: "", task: "transcribe", wordTimestamps: false, initialPrompt: "", beamSize: 5, temperature: 0 };
const source = { kind: "path" as const, path: "/audio.wav", name: "audio.wav" };
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
beforeEach(() => { vi.useFakeTimers(); vi.clearAllMocks(); });
afterEach(() => vi.useRealTimers());
describe("queue lifecycle", () => {
  it("clears entrance timers on unmount", () => {
    mocks.submit.mockReturnValue(new Promise(() => {}));
    const { result, unmount } = renderHook(useTranscribeQueue);
    act(() => { void result.current.submit(source, options); });
    expect(result.current.isEntering(result.current.rows[0].key)).toBe(true);
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
  for (const end of ["unmount", "server change"] as const) {
    for (const succeeds of [true, false]) {
      it(`ignores late upload ${succeeds ? "success" : "failure"} after ${end}`, async () => {
        const request = deferred<Transcription>();
        mocks.submit.mockReturnValue(request.promise);
        const { result, unmount } = renderHook(useTranscribeQueue);
        let outcome!: ReturnType<typeof result.current.submit>;
        act(() => { outcome = result.current.submit(source, options); });
        act(() => { if (end === "unmount") unmount(); else bumpServerSettingsVersion(); });
        await act(async () => {
          if (succeeds) request.resolve({ id: "accepted" } as Transcription);
          else request.reject(new Error("old server failure"));
          await outcome;
        });
        expect((await outcome).ok).toBe(succeeds);
        expect(mocks.refresh).not.toHaveBeenCalled();
        if (end === "server change") expect(result.current.rows).toEqual([]);
      });
    }
    for (const action of ["cancel", "retry"] as const) {
      for (const succeeds of [true, false]) {
        it(`ignores late ${action} ${succeeds ? "success" : "failure"} after ${end}`, async () => {
          const request = deferred<Transcription>();
          mocks[action].mockReturnValue(request.promise);
          const { result, unmount } = renderHook(useTranscribeQueue);
          act(() => { result.current[action]("same-id"); });
          act(() => { if (end === "unmount") unmount(); else bumpServerSettingsVersion(); });
          await act(async () => {
            if (succeeds) request.resolve({ id: "same-id" } as Transcription);
            else request.reject(new Error("old server failure"));
            await vi.advanceTimersByTimeAsync(0);
          });
          expect(mocks.refresh).not.toHaveBeenCalled();
          expect(mocks.toast).not.toHaveBeenCalled();
          if (end === "server change") expect(result.current.isCanceling("same-id")).toBe(false);
        });
      }
    }
  }
  for (const action of ["cancel", "retry"] as const) {
    it(`deduplicates rapid ${action} requests`, () => {
      mocks[action].mockReturnValue(new Promise(() => {}));
      const { result } = renderHook(useTranscribeQueue);
      act(() => { result.current[action]("same-id"); result.current[action]("same-id"); });
      expect(mocks[action]).toHaveBeenCalledTimes(1);
    });
  }
  it("does not let an old retry clear a new server's in-flight guard", async () => {
    const old = deferred<Transcription>();
    const current = deferred<Transcription>();
    mocks.retry.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const { result } = renderHook(useTranscribeQueue);
    act(() => { result.current.retry("same-id"); bumpServerSettingsVersion(); });
    act(() => result.current.retry("same-id"));
    await act(async () => { old.resolve({ id: "same-id" } as Transcription); await vi.advanceTimersByTimeAsync(0); });
    act(() => result.current.retry("same-id"));
    expect(mocks.retry).toHaveBeenCalledTimes(2);
  });
});
