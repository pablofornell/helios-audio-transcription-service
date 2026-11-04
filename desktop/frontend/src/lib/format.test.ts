import { describe, expect, it } from "vitest";
import type { Transcription } from "./types";
import { formatDuration, formatLanguages, formatTimestamp, languageName } from "./format";

/*
 * `formatTimestamp`/`formatDuration` are the two units the transcript
 * viewer's clickable `[mm:ss]` segment timestamps and the Queue/Library's
 * "how long is this audio" displays are built from — this file is
 * specifically their own boundaries. `formatBytes`/`formatRelative`/etc.
 * are carried verbatim from the sibling project and already covered by
 * its own test suite's reasoning; nothing about their behaviour changed
 * here.
 */
describe("formatTimestamp", () => {
  it("renders a genuine zero as 0:00, not a dash — it is a position, not a length", () => {
    expect(formatTimestamp(0)).toBe("0:00");
  });

  it("renders under an hour as m:ss", () => {
    expect(formatTimestamp(5)).toBe("0:05");
    expect(formatTimestamp(65)).toBe("1:05");
    expect(formatTimestamp(599)).toBe("9:59");
  });

  it("renders an hour or more as h:mm:ss", () => {
    expect(formatTimestamp(3_600)).toBe("1:00:00");
    expect(formatTimestamp(3_665)).toBe("1:01:05");
  });

  it("rounds to the nearest second and clamps negative input to 0", () => {
    expect(formatTimestamp(1.6)).toBe("0:02");
    expect(formatTimestamp(-5)).toBe("0:00");
  });
});

describe("formatDuration", () => {
  it("renders a dash, never 0:00, for zero/absent/negative — a media length of zero means 'not yet known'", () => {
    expect(formatDuration(0)).toBe("—");
    expect(formatDuration(undefined)).toBe("—");
    expect(formatDuration(null)).toBe("—");
    expect(formatDuration(-1)).toBe("—");
  });

  it("otherwise renders exactly like formatTimestamp", () => {
    expect(formatDuration(65)).toBe(formatTimestamp(65));
    expect(formatDuration(3_665)).toBe(formatTimestamp(3_665));
  });
});

describe("formatLanguages", () => {
  const result = (language: string, outputLanguage?: string, task: "transcribe" | "translate" = "transcribe"): Transcription["result"] =>
    ({ text: "", language, outputLanguage, durationSeconds: 1, model: "m", task, segments: [] });

  it("shows detected and output language for a translated job", () => {
    expect(formatLanguages({ result: result("fr", "en", "translate") })).toBe("French → English");
  });

  it("shows one language when audio and transcript match", () => {
    expect(formatLanguages({ result: result("es", "es") })).toBe("Spanish");
    expect(formatLanguages({ result: result("en", "en") })).toBe("English");
  });

  it("shows only the detected language for an old transcribe result without outputLanguage", () => {
    expect(formatLanguages({ result: result("en") })).toBe("English");
    expect(formatLanguages({ result: result("es") })).toBe("Spanish");
  });

  it("shows the English output of an old translate result without outputLanguage", () => {
    expect(formatLanguages({ result: result("fr", undefined, "translate") })).toBe("French → English");
  });

  it("shows no arrow for an old translate result whose audio was English", () => {
    expect(formatLanguages({ result: result("en", undefined, "translate") })).toBe("English");
  });

  it("shows auto-detect while there is no result", () => {
    expect(formatLanguages({ result: undefined })).toBe("auto-detect");
  });
});

describe("languageName", () => {
  it("falls back to the code for an unknown one", () => {
    expect(languageName("zzzz-invalid-code!")).toBe("zzzz-invalid-code!");
  });
});
