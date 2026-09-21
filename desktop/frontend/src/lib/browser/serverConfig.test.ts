import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HeliosError } from "../errors";
import { apiBase, currentSettings, setServer, validateCandidate } from "./serverConfig";

// jsdom's default test URL (vitest's jsdom environment default, unless
// overridden below) is http://localhost:3000/ — every "default" assertion
// in this file is built on exactly that, and `withLocation` is how a test
// overrides it for the mixed-content cases, which need an https page.
const DEFAULT_ORIGIN = { host: "localhost", port: 3000, scheme: "http" as const };

function withLocation(url: string, run: () => void): void {
  const original = window.location;
  Object.defineProperty(window, "location", { configurable: true, value: new URL(url) });
  try {
    run();
  } finally {
    Object.defineProperty(window, "location", { configurable: true, value: original });
  }
}

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  window.localStorage.clear();
});

describe("apiBase", () => {
  it("is empty (same-origin, relative) when nothing has ever been saved", () => {
    expect(apiBase()).toBe("");
  });

  it("is the saved server's absolute origin once one has been saved", () => {
    setServer("example.com", 9090);
    expect(apiBase()).toBe("http://example.com:9090");
  });

  it("goes back to empty once localStorage is cleared directly", () => {
    setServer("example.com", 9090);
    window.localStorage.clear();
    expect(apiBase()).toBe("");
  });

  it("ignores a corrupted localStorage value, falling back to the default", () => {
    window.localStorage.setItem("helios:server", "not json at all");
    expect(apiBase()).toBe("");
  });

  it("ignores a localStorage value missing a required field", () => {
    window.localStorage.setItem("helios:server", JSON.stringify({ host: "example.com" }));
    expect(apiBase()).toBe("");
  });
});

describe("currentSettings", () => {
  it("reports this page's own origin, split into host/port/baseUrl, by default", () => {
    expect(currentSettings()).toEqual({ host: DEFAULT_ORIGIN.host, port: DEFAULT_ORIGIN.port, baseUrl: "http://localhost:3000", editable: true });
  });

  it("is always editable", () => {
    expect(currentSettings().editable).toBe(true);
  });

  it("reports the saved server once one is saved", () => {
    setServer("example.com", 9090);
    expect(currentSettings()).toEqual({ host: "example.com", port: 9090, baseUrl: "http://example.com:9090", editable: true });
  });

  it("defaults an https page's port to 443 and an http page's to 80 when the URL carries none", () => {
    withLocation("https://ats.example.com/", () => {
      expect(currentSettings()).toEqual({ host: "ats.example.com", port: 443, baseUrl: "https://ats.example.com:443", editable: true });
    });
  });

  it("never carries a warning — there is no on-disk file to have gone invalid at startup", () => {
    expect(currentSettings().warning).toBeUndefined();
  });
});

describe("setServer", () => {
  it("persists host/port and reports them back as the new effective settings", () => {
    const result = setServer("example.com", 9090);
    expect(result).toEqual({ host: "example.com", port: 9090, baseUrl: "http://example.com:9090", editable: true });
    expect(currentSettings().baseUrl).toBe("http://example.com:9090");
  });

  it("trims whitespace around the host", () => {
    const result = setServer("  example.com  ", 9090);
    expect(result.host).toBe("example.com");
  });

  it("honours an explicit https:// prefix on Host, overriding the fallback scheme", () => {
    const result = setServer("https://example.com", 9090);
    expect(result).toEqual({ host: "example.com", port: 9090, baseUrl: "https://example.com:9090", editable: true });
  });

  it("preserves the previously effective scheme when Host carries no explicit prefix", () => {
    setServer("https://example.com", 9090);
    const result = setServer("other-host", 9090);
    expect(result.baseUrl).toBe("https://other-host:9090");
  });

  it("saving the page's own origin's host/port is the documented way back to default behaviour", () => {
    setServer("example.com", 9090);
    setServer(DEFAULT_ORIGIN.host, DEFAULT_ORIGIN.port);
    // Still an explicit localStorage entry (apiBase() is not "" — see
    // apiBase's own doc on why only "nothing ever saved" gets the bare
    // relative-path treatment), but it resolves to exactly the same
    // origin the page is already being served from, so every request
    // behaves identically to true same-origin from here on.
    expect(currentSettings().baseUrl).toBe(`http://${DEFAULT_ORIGIN.host}:${DEFAULT_ORIGIN.port}`);
  });

  it("rejects an empty host without saving anything", () => {
    expect(() => setServer("", 9090)).toThrow(HeliosError);
    expect(apiBase()).toBe("");
  });

  it("rejects a host containing whitespace", () => {
    expect(() => setServer("exa mple.com", 9090)).toThrow(HeliosError);
  });

  it("rejects a host carrying a path", () => {
    expect(() => setServer("example.com/path", 9090)).toThrow(HeliosError);
  });

  it("rejects a host carrying a port", () => {
    expect(() => setServer("example.com:9090", 9090)).toThrow(HeliosError);
  });

  it("rejects a host containing '@', '%' or '\\\\'", () => {
    expect(() => setServer("user@example.com", 9090)).toThrow(HeliosError);
  });

  it("rejects a port of 0", () => {
    expect(() => setServer("example.com", 0)).toThrow(HeliosError);
  });

  it("rejects a port above 65535", () => {
    expect(() => setServer("example.com", 99999)).toThrow(HeliosError);
  });

  it("rejects a non-integer port", () => {
    expect(() => setServer("example.com", 8080.5)).toThrow(HeliosError);
  });

  it("every rejection carries code invalid_input", () => {
    try {
      setServer("", 9090);
      expect.unreachable();
    } catch (err) {
      expect(err).toBeInstanceOf(HeliosError);
      expect((err as HeliosError).code).toBe("invalid_input");
    }
  });

  it("rejects an explicit http:// target from an https page (mixed content) without saving", () => {
    withLocation("https://ats.example.com/", () => {
      // A bare host with no typed prefix would default to https here (the
      // page's own, currently-effective scheme — see setServer's "honours
      // an explicit https:// prefix" test above) and so would not trigger
      // this at all; only an explicit "http://" override does.
      expect(() => setServer("http://example.com", 9090)).toThrow(/mixed-content/);
      expect(apiBase()).toBe("");
    });
  });

  it("accepts an explicit https:// target from an https page", () => {
    withLocation("https://ats.example.com/", () => {
      const result = setServer("https://example.com", 9443);
      expect(result.baseUrl).toBe("https://example.com:9443");
    });
  });

  it("does not reject a plain http target from an http page", () => {
    expect(() => setServer("example.com", 9090)).not.toThrow();
  });

  it("matches the scheme prefix case-sensitively, exactly like desktop/serversettings.go's strings.HasPrefix", () => {
    // "HTTPS://" is not recognised as a typed prefix at all (unlike the
    // old case-insensitive regex) -- it falls through to the same "://"
    // rejection cleanServerHost gives it, not a silently-accepted https
    // override.
    expect(() => setServer("HTTPS://example.com", 9090)).toThrow(/scheme/);
  });
});

describe("localStorage throwing (private browsing, strict CSP, full quota — see readStored/writeStored's own doc)", () => {
  it("apiBase() falls back to the default when localStorage.getItem throws", () => {
    const spy = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("access denied");
    });
    try {
      expect(apiBase()).toBe("");
    } finally {
      spy.mockRestore();
    }
  });

  it("setServer does not throw when localStorage.setItem throws — the save is a best-effort one", () => {
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    try {
      expect(() => setServer("example.com", 9090)).not.toThrow();
      // Nothing actually got persisted, so the effective server is still
      // the default -- exactly like setItem never having been called at
      // all (see writeStored's doc: a failed write is silently swallowed).
      expect(apiBase()).toBe("");
    } finally {
      spy.mockRestore();
    }
  });
});

describe("validateCandidate", () => {
  it("validates without persisting anything", () => {
    const candidate = validateCandidate("example.com", 9090);
    expect(candidate).toEqual({ host: "example.com", port: 9090, scheme: "http" });
    expect(apiBase()).toBe("");
  });

  it("rejects the exact same malformed input setServer would, without persisting it either", () => {
    expect(() => validateCandidate("", 9090)).toThrow(HeliosError);
    expect(apiBase()).toBe("");
  });

  it("rejects a mixed-content-blocked candidate without persisting it", () => {
    withLocation("https://ats.example.com/", () => {
      expect(() => validateCandidate("http://example.com", 9090)).toThrow(/mixed-content/);
      expect(apiBase()).toBe("");
    });
  });
});
