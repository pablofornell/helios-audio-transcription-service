import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DEFAULT_OPTIONS, useComposerOptions } from "./useComposerOptions";

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
      result.current.setOptions({ ...result.current.options, model: "base", language: "en", wordTimestamps: true });
    });
    expect(result.current.isDefault).toBe(false);
    unmount();

    const remounted = renderHook(() => useComposerOptions());
    expect(remounted.result.current.options.model).toBe("base");
    expect(remounted.result.current.options.language).toBe("en");
    expect(remounted.result.current.options.wordTimestamps).toBe(true);
    expect(remounted.result.current.isDefault).toBe(false);
  });

  it("reset() returns to the documented defaults and clears isDefault's false state", () => {
    const { result } = renderHook(() => useComposerOptions());
    act(() => {
      result.current.setOptions({ ...result.current.options, task: "translate" });
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

  it("ignores an invalid persisted task rather than trusting it verbatim", () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ task: "not-a-real-task", model: "small" }));
    const { result } = renderHook(() => useComposerOptions());
    expect(result.current.options.task).toBe("transcribe");
    expect(result.current.options.model).toBe("small");
  });
});
