import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { triggerBrowserDownloadFromClick, useBrowserDownloadBusy } from "./download";
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers(); });
it("deduplicates same URLs synchronously and launches distinct URLs immediately", () => {
  vi.useFakeTimers();
  const clicks = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  expect(triggerBrowserDownloadFromClick("/native-a")).toBe(true);
  expect(triggerBrowserDownloadFromClick("/native-a")).toBe(false);
  expect(triggerBrowserDownloadFromClick("/native-b")).toBe(true);
  expect(clicks).toHaveBeenCalledTimes(2);
  act(() => vi.advanceTimersByTime(1500));
  expect(triggerBrowserDownloadFromClick("/native-a")).toBe(true);
  act(() => vi.advanceTimersByTime(1500));
});
it("shares cooldown feedback across controls while other URLs remain enabled", () => {
  vi.useFakeTimers();
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  function Action({ url }: { url: string }) {
    const busy = useBrowserDownloadBusy(url);
    return <button disabled={busy} onClick={() => triggerBrowserDownloadFromClick(url)}>{busy ? "Requested" : url}</button>;
  }
  render(<><Action url="/shared" /><Action url="/shared" /><Action url="/distinct" /></>);
  fireEvent.click(screen.getAllByRole("button", { name: "/shared" })[0]);
  expect(screen.getAllByRole("button", { name: "Requested" })).toHaveLength(2);
  expect(screen.getByRole("button", { name: "/distinct" })).toBeEnabled();
  act(() => vi.advanceTimersByTime(1500));
  expect(screen.getAllByRole("button", { name: "/shared" })).toHaveLength(2);
});
it("uses a safe new context only for cross origin", () => {
  vi.useFakeTimers();
  const captures: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { captures.push(this); });
  triggerBrowserDownloadFromClick("/same-native");
  triggerBrowserDownloadFromClick("https://other.example/native");
  expect(captures[0].target).toBe("");
  expect(captures[1].target).toBe("_blank");
  expect(captures[1].rel).toBe("noopener");
  expect(captures.every((anchor) => anchor.hasAttribute("download") && !anchor.isConnected)).toBe(true);
  act(() => vi.advanceTimersByTime(1500));
});
