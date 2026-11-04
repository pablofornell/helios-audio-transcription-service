import { useCallback, useEffect, useState } from "react";
import type { Options } from "../../lib";

/*
 * Submit-time composer options (Model / Word timestamps) — persisted in
 * localStorage and restored per session. The service detects the audio
 * language itself and ignores any language hint, so the composer has no
 * language control: `language` is always sent empty, and a language saved
 * by an older version is dropped on load. `task` has no UI either; it is
 * always sent as "transcribe" for compatibility and the server ignores it.
 * `initialPrompt`, `beamSize` and `temperature` have no UI here; they are
 * carried at their domain.DefaultOptions() zero values and the server's own
 * Options.Normalize fills in its actual defaults (beam size 5, transcribe)
 * from an empty/zero field, so submitting them untouched is always
 * well-formed.
 */

const STORAGE_KEY = "helios.transcribe.composerOptions";

export const SUPPORTED_MODEL = "large-v3-turbo-q5_0";

export const DEFAULT_OPTIONS: Options = {
  model: "",
  language: "",
  task: "transcribe",
  wordTimestamps: false,
  initialPrompt: "",
  beamSize: 0,
  temperature: 0,
};

function loadOptions(): Options {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_OPTIONS;
    const parsed = JSON.parse(raw) as Partial<Options>;
    return {
      ...DEFAULT_OPTIONS,
      // Retired or foreign saved models fall back to the supported server default.
      model: parsed.model === SUPPORTED_MODEL ? parsed.model : DEFAULT_OPTIONS.model,
      wordTimestamps: Boolean(parsed.wordTimestamps),
    };
  } catch {
    // Corrupt or foreign localStorage content is not worth surfacing —
    // fall back to the documented defaults silently.
    return DEFAULT_OPTIONS;
  }
}

function isDefault(options: Options): boolean {
  return (
    options.model === DEFAULT_OPTIONS.model &&
    !options.wordTimestamps
  );
}

export interface UseComposerOptions {
  options: Options;
  setOptions: (next: Options) => void;
  /** True only when `options` matches the documented defaults exactly — drives the "Reset to defaults" ghost link's visibility. */
  isDefault: boolean;
  reset: () => void;
}

export function useComposerOptions(): UseComposerOptions {
  const [options, setOptions] = useState<Options>(loadOptions);

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(options));
  }, [options]);

  const reset = useCallback(() => setOptions(DEFAULT_OPTIONS), []);

  return { options, setOptions, isDefault: isDefault(options), reset };
}
