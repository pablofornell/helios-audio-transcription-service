import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ConnectionState } from "../lib";
import { ColdStart } from "./ColdStart";

function offlineConnection(): ConnectionState {
  return { status: "offline", attempt: 1, retryDelayMs: 1000 };
}

/*
 * `apiURL` is legitimately empty both in "local" mode (no separate server
 * to name at all) and before `getServerSettings()` first resolves —
 * App.tsx never renders ColdStart with a stale/wrong URL, but an *empty*
 * one must not render as a blank "Tried  — nothing answered" either.
 */
describe("ColdStart — the 'Tried <url>' clause", () => {
  it("names the URL it tried when one is known", () => {
    render(<ColdStart connection={offlineConnection()} apiURL="http://localhost:8080" />);
    expect(screen.getByText("http://localhost:8080")).toBeInTheDocument();
    expect(screen.getByText(/^Tried/)).toBeInTheDocument();
  });

  it("omits the 'Tried ...' clause entirely when apiURL is empty, rather than rendering a blank", () => {
    render(<ColdStart connection={offlineConnection()} apiURL="" />);
    expect(screen.queryByText(/^Tried/)).not.toBeInTheDocument();
    expect(screen.getByText(/The desktop app is fine/)).toBeInTheDocument();
  });
});
