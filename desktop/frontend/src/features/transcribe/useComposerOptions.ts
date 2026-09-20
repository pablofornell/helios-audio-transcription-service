import { useCallback, useEffect, useState } from "react";
import type { Options, Task } from "../../lib";

/*
 * Submit-time composer options (Model / Language / Task / Word timestamps)
 * — persisted in localStorage and restored per session. `initialPrompt`,
 * `beamSize` and `temperature` have no UI here (DESKTOP.md's brief lists
 * only these four); they are carried at their domain.DefaultOptions()
 * zero values and the server's own Options.Normalize fills in its actual
 * defaults (beam size 5, transcribe, auto-detect) from an empty/zero
 * field, so submitting them untouched is always well-formed.
 */

const STORAGE_KEY = "helios.transcribe.composerOptions";

export const DEFAULT_OPTIONS: Options = {
  model: "",
  language: "",
  task: "transcribe",
  wordTimestamps: false,
  initialPrompt: "",
  beamSize: 0,
  temperature: 0,
};

const VALID_TASKS: Task[] = ["transcribe", "translate"];

function loadOptions(): Options {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_OPTIONS;
    const parsed = JSON.parse(raw) as Partial<Options>;
    const task = VALID_TASKS.includes(parsed.task as Task) ? (parsed.task as Task) : DEFAULT_OPTIONS.task;
    return {
      ...DEFAULT_OPTIONS,
      model: typeof parsed.model === "string" ? parsed.model : DEFAULT_OPTIONS.model,
      language: typeof parsed.language === "string" ? parsed.language : DEFAULT_OPTIONS.language,
      task,
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
    options.language === DEFAULT_OPTIONS.language &&
    options.task === DEFAULT_OPTIONS.task &&
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
