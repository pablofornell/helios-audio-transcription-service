import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// The Wails bindings only exist inside a Wails window. Every test double for
// them goes through vi.mock("../../lib") or vi.mock("../../../wailsjs/...")
// in the test file itself; this file only provides the browser APIs jsdom
// lacks that our components actually touch.

// Used by the reduced-motion and theme code paths.
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })) as unknown as typeof window.matchMedia;
}

// LibraryList's infinite-scroll sentinel.
if (!window.IntersectionObserver) {
  window.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
    root = null;
    rootMargin = "";
    thresholds = [];
  } as unknown as typeof window.IntersectionObserver;
}

// jsdom implements neither of these, and both are used by real components:
// Select scrolls its active option into view, and Dialog/Menu measure to
// position themselves. Without the stubs, a component test fails on the
// environment rather than on the component.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = vi.fn();
}
if (!Element.prototype.scrollTo) {
  Element.prototype.scrollTo = vi.fn() as unknown as typeof Element.prototype.scrollTo;
}
