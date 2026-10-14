import { act, render, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DetailPanel } from "../../features/library/DetailPanel";
import { HeliosError } from "../errors";
// `../../lib`'s own `setServerSettings` (as opposed to this module's own,
// used everywhere else below): it is the one that also bumps
// `../serverSettingsSignal.ts` after a save, so a mounted DetailPanel
// actually notices — see the "DetailPanel's <audio> src" describe block
// below, which is the only place this distinction matters.
import { setServerSettings as saveServerSettingsAndNotify } from "../../lib";
import type { Transcription } from "../types";
import * as api from "./api";

// Every method below is exercised against a mocked global `fetch` — no
// real network call, no real Helios server. Response bodies are the exact
// wire shape internal/httpapi/dto.go sends (docs/openapi.yaml), since this
// module's whole premise is that it needs no translation layer for those
// (see `./api.ts`'s doc).

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? undefined : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeWireTranscription(overrides: Partial<Transcription> = {}): Transcription {
  return {
    id: "11111111-1111-1111-1111-111111111111",
    status: "pending",
    options: {
      model: "large-v3-turbo-q5_0",
      language: "",
      task: "transcribe",
      wordTimestamps: false,
      initialPrompt: "",
      beamSize: 5,
      temperature: 0,
    },
    audio: {
      filename: "clip.mp3",
      mediaType: "audio/mpeg",
      sizeBytes: 1024,
      durationSeconds: 0,
      sampleRateHz: 0,
      channels: 0,
      codec: "",
    },
    attempts: 0,
    maxAttempts: 3,
    createdAt: "2024-01-01T00:00:00Z",
    updatedAt: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  // Every test below except the "saved server" describe block below relies
  // on the default, same-origin behaviour (`./serverConfig.ts`'s
  // `apiBase() === ""`) — a leftover save from one test must never change
  // what URL the next one asserts against.
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

describe("submit", () => {
  it("POSTs multipart/form-data to /api/v1/transcriptions and decodes a 202", async () => {
    const wire = makeWireTranscription();
    fetchMock.mockResolvedValue(jsonResponse(202, wire));

    const file = new File(["bytes"], "clip.mp3", { type: "audio/mpeg" });
    const result = await api.submit({ kind: "file", file }, wire.options);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/transcriptions");
    expect(init.method).toBe("POST");
    expect(init.body).toBeInstanceOf(FormData);
    const form = init.body as FormData;
    expect(JSON.parse(form.get("options") as string)).toEqual(wire.options);
    expect((form.get("file") as File).name).toBe("clip.mp3");
    expect(result).toEqual(wire);
  });

  it("rejects a 'path' source without ever calling fetch — a browser tab has no local filesystem path", async () => {
    await expect(api.submit({ kind: "path", path: "/tmp/clip.mp3", name: "clip.mp3" }, makeWireTranscription().options)).rejects.toMatchObject({
      code: "invalid_input",
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects with the classified HeliosError on a non-202 response", async () => {
    fetchMock.mockResolvedValue(jsonResponse(415, { error: { code: "unsupported_media", message: "not a known audio type" } }));

    const file = new File(["bytes"], "clip.txt", { type: "text/plain" });
    await expect(api.submit({ kind: "file", file }, makeWireTranscription().options)).rejects.toMatchObject({
      code: "unsupported_media",
      message: "not a known audio type",
    });
  });
});

describe("list", () => {
  it("encodes status, limit and cursor as query params", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [], nextCursor: "abc" }));

    await api.list({ library: true, status: "running", limit: 50, cursor: "page2" });

    const [path] = fetchMock.mock.calls[0] as [string];
    const url = new URL(path, "http://localhost");
    expect(url.pathname).toBe("/api/v1/transcriptions");
    expect(url.searchParams.get("status")).toBe("running");
    expect(url.searchParams.get("limit")).toBe("50");
    expect(url.searchParams.get("cursor")).toBe("page2");
    expect(url.searchParams.get("library")).toBe("true");
  });

  it("sends no query string at all for an empty filter", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [] }));
    await api.list();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/transcriptions");
  });

  it("returns items and nextCursor decoded verbatim", async () => {
    const wire = makeWireTranscription();
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [wire], nextCursor: "next" }));
    const page = await api.list();
    expect(page.items).toEqual([wire]);
    expect(page.nextCursor).toBe("next");
  });
});

describe("get/retry/cancel/deleteTranscription", () => {
  it("GETs /api/v1/transcriptions/{id}", async () => {
    const wire = makeWireTranscription();
    fetchMock.mockResolvedValue(jsonResponse(200, wire));
    const result = await api.get(wire.id);
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/transcriptions/${wire.id}`);
    expect(result).toEqual(wire);
  });

  it("POSTs /api/v1/transcriptions/{id}/retry and decodes the 200 body", async () => {
    const wire = makeWireTranscription({ status: "pending", attempts: 1 });
    fetchMock.mockResolvedValue(jsonResponse(200, wire));
    const result = await api.retry(wire.id);
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe(`/api/v1/transcriptions/${wire.id}/retry`);
    expect(init.method).toBe("POST");
    expect(result).toEqual(wire);
  });

  it("POSTs /api/v1/transcriptions/{id}/cancel and resolves void on a 200", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, makeWireTranscription({ status: "canceled" })));
    await expect(api.cancel("id-1")).resolves.toBeUndefined();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/transcriptions/id-1/cancel");
  });

  it("DELETEs /api/v1/transcriptions/{id} and resolves void on 204", async () => {
    fetchMock.mockResolvedValue(new Response(undefined, { status: 204 }));
    await expect(api.deleteTranscription("id-1")).resolves.toBeUndefined();
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/transcriptions/id-1");
    expect(init.method).toBe("DELETE");
  });

  it("rejects with a HeliosError built from a transport failure", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(api.get("id-1")).rejects.toMatchObject({ code: "internal" });
  });
});

describe("transcript", () => {
  it("GETs /api/v1/transcriptions/{id}/transcript with the format query param and returns plain text", async () => {
    fetchMock.mockResolvedValue(new Response("1\n00:00:00,000 --> 00:00:01,000\nhi\n", { status: 200 }));
    const text = await api.transcript("id-1", "srt");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/transcriptions/id-1/transcript?format=srt");
    expect(text).toContain("hi");
  });
});

// transcriptDownloadUrl/audioDownloadUrl never call fetch at all — they are
// pure string builders `SaveAsDialog`/`DetailPanel`'s browser-tab actions
// navigate a temporary/real <a> at (see those modules' own docs). No
// fetchMock assertions below are meaningful for them; only the returned
// string is.
describe("transcriptDownloadUrl", () => {
  it("builds a same-origin relative URL with format and download=1 when nothing is saved", () => {
    expect(api.transcriptDownloadUrl("id-1", "srt")).toBe("/api/v1/transcriptions/id-1/transcript?format=srt&download=1");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("covers every TranscriptFormat", () => {
    expect(api.transcriptDownloadUrl("id-1", "txt")).toBe("/api/v1/transcriptions/id-1/transcript?format=txt&download=1");
    expect(api.transcriptDownloadUrl("id-1", "vtt")).toBe("/api/v1/transcriptions/id-1/transcript?format=vtt&download=1");
    expect(api.transcriptDownloadUrl("id-1", "json")).toBe("/api/v1/transcriptions/id-1/transcript?format=json&download=1");
  });

  it("becomes an absolute URL against a saved cross-origin server — a non-empty apiBase()", async () => {
    await api.setServerSettings("example.com", 9090);
    expect(api.transcriptDownloadUrl("id-1", "txt")).toBe("http://example.com:9090/api/v1/transcriptions/id-1/transcript?format=txt&download=1");
  });

  it("URL-encodes an id with characters that would otherwise break the path/query", () => {
    expect(api.transcriptDownloadUrl("weird id/with?stuff&more", "txt")).toBe(
      "/api/v1/transcriptions/weird%20id%2Fwith%3Fstuff%26more/transcript?format=txt&download=1",
    );
  });
});

describe("audioDownloadUrl", () => {
  it("builds a same-origin relative URL when nothing is saved", () => {
    expect(api.audioDownloadUrl("id-1")).toBe("/api/v1/transcriptions/id-1/audio");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("matches the same origin transcript()/DetailPanel's <audio src> already resolve against — becomes absolute against a saved cross-origin server", async () => {
    await api.setServerSettings("example.com", 9090);
    expect(api.audioDownloadUrl("id-1")).toBe("http://example.com:9090/api/v1/transcriptions/id-1/audio");
  });

  it("URL-encodes an id with characters that would otherwise break the path", () => {
    expect(api.audioDownloadUrl("weird id/with?stuff")).toBe("/api/v1/transcriptions/weird%20id%2Fwith%3Fstuff/audio");
  });
});

describe("health", () => {
  it("GETs /readyz (not under /api/v1) and returns the report as-is on 200", async () => {
    const report = { ok: true, checks: { database: { ok: true, detail: "" } } };
    fetchMock.mockResolvedValue(jsonResponse(200, report));
    const result = await api.health();
    expect(fetchMock.mock.calls[0][0]).toBe("/readyz");
    expect(result).toEqual(report);
  });

  it("returns the report as-is on a 503 rather than throwing", async () => {
    const report = { ok: false, checks: { database: { ok: false, detail: "down" } } };
    fetchMock.mockResolvedValue(jsonResponse(503, report));
    await expect(api.health()).resolves.toEqual(report);
  });

  it("never rejects: a transport failure becomes a false report with a 'connection' check", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    const result = await api.health();
    expect(result.ok).toBe(false);
    expect(result.checks.connection.ok).toBe(false);
    expect(result.checks.connection.detail).toContain("Failed to fetch");
  });

  it("never rejects: an unparseable body becomes a false report too", async () => {
    fetchMock.mockResolvedValue(new Response("<html>not json</html>", { status: 200 }));
    const result = await api.health();
    expect(result.ok).toBe(false);
    expect(result.checks.connection.ok).toBe(false);
  });
});

describe("server settings — default, same-origin behaviour", () => {
  it("getServerSettings resolves editable: true, pointed at this page's own origin", async () => {
    const settings = await api.getServerSettings();
    expect(settings.editable).toBe(true);
    expect(settings.baseUrl).toBe(`http://${window.location.hostname}:${window.location.port}`);
  });

  it("every REST call stays a same-origin relative path when nothing has been saved", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [] }));
    await api.list();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/transcriptions");
  });
});

describe("server settings — a saved server changes every request's URL", () => {
  it("setServerSettings persists a host/port and returns it as the new effective settings", async () => {
    const result = await api.setServerSettings("example.com", 9090);
    expect(result).toEqual({ host: "example.com", port: 9090, baseUrl: "http://example.com:9090", editable: true });
  });

  it("REST calls become absolute URLs against the saved server", async () => {
    await api.setServerSettings("example.com", 9090);
    fetchMock.mockResolvedValue(jsonResponse(200, { items: [] }));
    await api.list();
    expect(fetchMock.mock.calls[0][0]).toBe("http://example.com:9090/api/v1/transcriptions");
  });

  it("health() (GET /readyz) also becomes absolute against the saved server", async () => {
    await api.setServerSettings("example.com", 9090);
    fetchMock.mockResolvedValue(jsonResponse(200, { ok: true, checks: {} }));
    await api.health();
    expect(fetchMock.mock.calls[0][0]).toBe("http://example.com:9090/readyz");
  });

  it("getServerSettings reflects the saved server after a save", async () => {
    await api.setServerSettings("example.com", 9090);
    const settings = await api.getServerSettings();
    expect(settings).toEqual({ host: "example.com", port: 9090, baseUrl: "http://example.com:9090", editable: true });
  });

  it("rejects a malformed host with a HeliosError, without changing the effective server", async () => {
    await expect(api.setServerSettings("", 9090)).rejects.toMatchObject({ code: "invalid_input" });
    const settings = await api.getServerSettings();
    expect(settings.baseUrl).toBe(`http://${window.location.hostname}:${window.location.port}`);
  });

  it("rejects an out-of-range port with a HeliosError", async () => {
    await expect(api.setServerSettings("example.com", 0)).rejects.toBeInstanceOf(HeliosError);
  });
});

// DetailPanel's <audio src> is this adapter's one real caller of
// getServerSettings().baseUrl (see features/library/DetailPanel.tsx) — a
// bare string-concatenation assertion (what used to live here) proves
// nothing about DetailPanel itself, only that template-literal
// interpolation works, so these render it for real instead. No fake
// Wails runtime is stubbed anywhere in this suite, so `hasWailsRuntime()`
// is false and `../api.ts`'s `impl()` picks this module, exactly like a
// plain browser tab.
describe("DetailPanel's <audio> src follows the effective server", () => {
  function renderDetailPanel(transcription: Transcription) {
    return render(
      createElement(DetailPanel, {
        transcription,
        onClose: () => {},
        onRetry: () => {},
        onSaveAs: () => {},
        onDelete: () => {},
        overlay: false,
      }),
    );
  }

  it("is an absolute URL against a saved cross-origin server", async () => {
    await api.setServerSettings("example.com", 9090);
    const transcription = makeWireTranscription({ id: "detail-audio-1" });

    const { container } = renderDetailPanel(transcription);

    await waitFor(() =>
      expect(container.querySelector("audio")?.getAttribute("src")).toBe(
        "http://example.com:9090/api/v1/transcriptions/detail-audio-1/audio",
      ),
    );
  });

  it("updates once Settings -> Server saves a different server, without DetailPanel remounting", async () => {
    const transcription = makeWireTranscription({ id: "detail-audio-2" });
    const { container } = renderDetailPanel(transcription);
    const audioSrc = () => container.querySelector("audio")?.getAttribute("src");

    await waitFor(() =>
      expect(audioSrc()).toBe(`http://${window.location.hostname}:${window.location.port}/api/v1/transcriptions/detail-audio-2/audio`),
    );

    // Same call ServerSettingsSection's handleSave makes — see this
    // file's own doc on why this, not `api.setServerSettings`, is the one
    // that actually notifies a mounted DetailPanel.
    await act(async () => {
      await saveServerSettingsAndNotify("example.com", 9090);
    });

    await waitFor(() => expect(audioSrc()).toBe("http://example.com:9090/api/v1/transcriptions/detail-audio-2/audio"));
  });
});

// MediaActions (DetailPanel.tsx) has no OS to hand Open/Reveal off to from
// a browser tab — see openAudio/revealInFileManager's own docs above — so
// it renders a plain download link instead. Same "no fake Wails runtime"
// premise as the describe block above.
describe("DetailPanel's MediaActions in a browser tab", () => {
  it("launches native audio download with shared request feedback", async () => {
    const transcription = makeWireTranscription({ id: "detail-dl-1" });
    const { container } = render(
      createElement(DetailPanel, {
        transcription,
        onClose: () => {},
        onRetry: () => {},
        onSaveAs: () => {},
        onDelete: () => {},
        overlay: false,
      }),
    );

    await waitFor(() => expect(container.textContent).toContain("Download audio"));
    const button = Array.from(container.querySelectorAll("button")).find((item) => item.textContent === "Download audio")!;
    let anchor!: HTMLAnchorElement;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { anchor = this; });
    await act(async () => button.click());
    expect(anchor.getAttribute("href")).toBe(api.audioDownloadUrl("detail-dl-1"));
    expect(anchor.getAttribute("target")).toBeNull();
    expect(container.textContent).toContain("Download requested…");
    expect(container.textContent).not.toMatch(/Open|Reveal in file manager/);
  });
});

describe("testServerConnection", () => {
  it("rejects a malformed host without ever calling fetch", async () => {
    await expect(api.testServerConnection("", 9090)).rejects.toMatchObject({ code: "invalid_input" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects an out-of-range port without ever calling fetch", async () => {
    await expect(api.testServerConnection("example.com", 99999)).rejects.toBeInstanceOf(HeliosError);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("fetches /readyz on the candidate host/port, without saving anything", async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { ok: true, checks: {} }));
    const report = await api.testServerConnection("example.com", 9090);
    expect(fetchMock.mock.calls[0][0]).toBe("http://example.com:9090/readyz");
    expect(report.ok).toBe(true);
    // Nothing was persisted — the effective server is still the default.
    expect(await api.getServerSettings()).toMatchObject({ host: window.location.hostname });
  });

  it("reports ok: false, with a connection check, when the candidate refuses the connection", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    const report = await api.testServerConnection("example.com", 9090);
    expect(report.ok).toBe(false);
    expect(report.checks.connection.ok).toBe(false);
  });

  it("hints that a cross-origin target's transport failure might be CORS, not just unreachable", async () => {
    // example.com:9090 is cross-origin from this suite's default test
    // origin (http://localhost:3000) — see serverConfig.ts's isCrossOrigin.
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    const report = await api.testServerConnection("example.com", 9090);
    expect(report.checks.connection.detail).toContain("Failed to fetch");
    expect(report.checks.connection.detail).toMatch(/CORS|origin/i);
    expect(report.checks.connection.detail).toContain("HELIOS_CORS_ALLOWED_ORIGINS");
  });

  it("does not add the CORS hint for a same-origin candidate — CORS never applies there", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"));
    const report = await api.testServerConnection(window.location.hostname, Number(window.location.port));
    expect(report.checks.connection.detail).toBe("Failed to fetch");
    expect(report.checks.connection.detail).not.toContain("CORS");
  });

  it("does not add the CORS hint on top of the timeout-specific message", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(
        (_url: string, init?: RequestInit) =>
          new Promise((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(new DOMException("The operation was aborted.", "AbortError")));
          }),
      );
      const pending = api.testServerConnection("example.com", 9090);
      await vi.advanceTimersByTimeAsync(5000);
      const report = await pending;
      expect(report.checks.connection.detail).not.toContain("CORS");
    } finally {
      vi.useRealTimers();
    }
  });

  it("replaces the connection check's detail with a named timeout message after 5s of silence", async () => {
    vi.useFakeTimers();
    try {
      fetchMock.mockImplementation(
        (_url: string, init?: RequestInit) =>
          new Promise((_resolve, reject) => {
            init?.signal?.addEventListener("abort", () => reject(new DOMException("The operation was aborted.", "AbortError")));
          }),
      );

      const pending = api.testServerConnection("example.com", 9090);
      await vi.advanceTimersByTimeAsync(5000);
      const report = await pending;

      expect(report.ok).toBe(false);
      expect(report.checks.connection.detail).toContain("example.com:9090");
      expect(report.checks.connection.detail).toContain("5s");
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("desktop-only capabilities have no fake browser equivalent", () => {
  it("chooseAudioFile rejects with 'media_unavailable', without a network call", async () => {
    await expect(api.chooseAudioFile()).rejects.toMatchObject({ code: "media_unavailable" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("audioLocation always reports exists: false, without a network call", async () => {
    const result = await api.audioLocation("id-1");
    expect(result).toEqual({ exists: false });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("openAudio rejects with 'media_unavailable', without a network call", async () => {
    await expect(api.openAudio("id-1")).rejects.toMatchObject({ code: "media_unavailable" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("revealInFileManager rejects with 'media_unavailable', without a network call", async () => {
    await expect(api.revealInFileManager("id-1")).rejects.toMatchObject({ code: "media_unavailable" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("chooseSaveDirectory rejects with 'media_unavailable', without a network call", async () => {
    await expect(api.chooseSaveDirectory()).rejects.toMatchObject({ code: "media_unavailable" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("saveTranscript rejects with 'media_unavailable', without a network call", async () => {
    await expect(api.saveTranscript("id-1", "txt", "/home/user/Downloads")).rejects.toMatchObject({ code: "media_unavailable" });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("defaultSaveDirectory resolves '' rather than rejecting", async () => {
    await expect(api.defaultSaveDirectory()).resolves.toBe("");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("every rejection is a real HeliosError, not a bare string/object", async () => {
    await expect(api.openAudio("id-1")).rejects.toBeInstanceOf(HeliosError);
  });
});
