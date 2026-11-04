import { describe, expect, it } from "vitest";
import { errorCopyFor } from "./errorCopy";

describe("errorCopyFor", () => {
  it("gives a manual Retry control to model_unavailable, which the server never retries by itself", () => {
    const copy = errorCopyFor("model_unavailable");
    expect(copy.retryable).toBe(false);
    expect(copy.canRetry).toBe(true);
  });

  it("does not say the user requested the model", () => {
    expect(errorCopyFor("model_unavailable").headline).not.toMatch(/requested/i);
  });

  it("keeps Retry for the codes the server retries, and off for the others", () => {
    expect(errorCopyFor("tool_failure").canRetry).toBe(true);
    expect(errorCopyFor("timeout").canRetry).toBe(true);
    for (const code of ["disk_full", "invalid_input", "internal", "no_audio_stream"] as const) {
      expect(errorCopyFor(code).canRetry).toBe(false);
    }
    expect(errorCopyFor(undefined).canRetry).toBe(false);
  });
});
