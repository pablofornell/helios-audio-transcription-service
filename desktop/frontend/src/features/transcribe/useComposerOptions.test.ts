import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DEFAULT_OPTIONS, SUPPORTED_MODEL, useComposerOptions } from "./useComposerOptions";

const STORAGE_KEY = "helios.transcribe.composerOptions";

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  localStorage.clear();
});

describe("useComposerOptions", () => {
  it("starts at the documented defaults when nothing is persisted", () => {
    const { result } = renderHook(() => useComposerOptions());
    expect(result.current.options).toEqual(DEFAULT_OPTIONS);
    expect(result.current.isDefault).toBe(true);
  });

  it("persists a change to localStorage and restores it on the next mount", () => {
    const { result, unmount } = renderHook(() => useComposerOptions());
    act(() => {
      result.current.setOptions({ ...result.current.options, model: SUPPORTED_MODEL, language: "en", wordTimestamps: true });
    });
    expect(result.current.isDefault).toBe(false);
    unmount();

    const remounted = renderHook(() => useComposerOptions());
    expect(remounted.result.current.options.model).toBe(SUPPORTED_MODEL);
    expect(remounted.result.current.options.language).toBe("en");
    expect(remounted.result.current.options.wordTimestamps).toBe(true);
    expect(remounted.result.current.isDefault).toBe(false);
  });

  it.each(["tiny", "base", "small", "medium", "large-v3", "large-v3-turbo", "unknown"])(
    "migrates retired persisted model %s to the server default without losing other options",
    (model) => {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ model, language: "es", wordTimestamps: true }));
      const { result } = renderHook(() => useComposerOptions());
      expect(result.current.options).toEqual({ ...DEFAULT_OPTIONS, language: "es", wordTimestamps: true });
      expect(JSON.parse(localStorage.getItem(STORAGE_KEY)!).model).toBe(DEFAULT_OPTIONS.model);
    },
  );

  it("reset() returns to the documented defaults and clears isDefault's false state", () => {
    const { result } = renderHook(() => useComposerOptions());
    act(() => {
      result.current.setOptions({ ...result.current.options, language: "fr" });
    });
    expect(result.current.isDefault).toBe(false);

    act(() => {
      result.current.reset();
    });
    expect(result.current.options).toEqual(DEFAULT_OPTIONS);
    expect(result.current.isDefault).toBe(true);
  });

  it("falls back to the defaults for a corrupt/foreign localStorage value rather than throwing", () => {
    localStorage.setItem(STORAGE_KEY, "not json");
    const { result } = renderHook(() => useComposerOptions());
    expect(result.current.options).toEqual(DEFAULT_OPTIONS);
  });

  it("drops a saved task: the server ignores it and the composer always sends transcribe", () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ task: "translate", language: "es", model: SUPPORTED_MODEL }));
    const { result } = renderHook(() => useComposerOptions());
    expect(result.current.options.task).toBe("transcribe");
    expect(result.current.options.language).toBe("es");
    expect(result.current.options.model).toBe(SUPPORTED_MODEL);
  });
});
