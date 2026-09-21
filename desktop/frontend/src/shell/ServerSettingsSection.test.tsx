import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HeliosError, type ServerSettings } from "../lib";
import { ServerSettingsSection } from "./ServerSettingsSection";

// ServerSettingsSection's only backend calls go through this module
// boundary — see SaveCopyDialog.test.tsx's own mock for the pattern this
// follows. connectionMonitor and transcriptionStore are both mocked
// outright (not spread from actual): nothing here exercises
// useConnection/getSnapshot or useTranscriptions, and the real
// retryNow/resetForServerChange implementations would each kick off an
// actual fetch/poll timer this test has no reason to let run (or, for
// transcriptionStore, no server to answer it at all — see lib/store.ts's
// own doc).
vi.mock("../lib", async () => {
  const actual = await vi.importActual<typeof import("../lib")>("../lib");
  return {
    ...actual,
    getServerSettings: vi.fn(),
    setServerSettings: vi.fn(),
    testServerConnection: vi.fn(),
    connectionMonitor: { retryNow: vi.fn() },
    transcriptionStore: { resetForServerChange: vi.fn() },
  };
});

import { connectionMonitor, getServerSettings, setServerSettings, testServerConnection, transcriptionStore } from "../lib";

function editableSettings(overrides: Partial<ServerSettings> = {}): ServerSettings {
  return { host: "localhost", port: 8080, baseUrl: "http://localhost:8080", editable: true, ...overrides };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("ServerSettingsSection — loading and non-editable modes", () => {
  it("pre-fills Host/Port once getServerSettings resolves", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings({ host: "example.com", port: 9090 }));
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("example.com"));
    expect(screen.getByLabelText("Port")).toHaveValue("9090");
  });

  it("disables the fields and shows a note when not editable", async () => {
    vi.mocked(getServerSettings).mockResolvedValue({ host: "", port: 0, baseUrl: "", editable: false });
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByText(/not editable here/i)).toBeInTheDocument());
    expect(screen.getByLabelText("Host")).toBeDisabled();
    expect(screen.getByLabelText("Port")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Test connection" })).toBeDisabled();
  });

  it("disables the fields while still loading, before getServerSettings resolves", () => {
    vi.mocked(getServerSettings).mockReturnValue(new Promise(() => {})); // never resolves
    render(<ServerSettingsSection />);

    expect(screen.getByLabelText("Host")).toBeDisabled();
    expect(screen.getByLabelText("Port")).toBeDisabled();
  });

  it("shows the backend's warning when a saved value could not be used", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(
      editableSettings({ warning: "Saved server settings were invalid and ignored" }),
    );
    render(<ServerSettingsSection />);

    expect(await screen.findByText("Saved server settings were invalid and ignored")).toBeInTheDocument();
  });

  it("shows no warning when none is reported", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("localhost"));
    expect(screen.queryByText(/invalid and ignored/)).not.toBeInTheDocument();
  });

  it("shows the real effective URL, not editable, when not editable", async () => {
    vi.mocked(getServerSettings).mockResolvedValue({ host: "", port: 0, baseUrl: "", editable: false });
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByText(/not editable here/i)).toBeInTheDocument());
    expect(screen.queryByText(/Currently connected to/)).not.toBeInTheDocument();
  });

  it("shows a read-only line naming the exact effective URL, including a non-default scheme/path", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(
      editableSettings({ baseUrl: "https://example.com:8443/gateway" }),
    );
    render(<ServerSettingsSection />);

    expect(await screen.findByText("https://example.com:8443/gateway")).toBeInTheDocument();
  });
});

describe("ServerSettingsSection — validation and Save", () => {
  it("keeps Save disabled until the fields actually change", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("localhost"));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("enables Save on the prefilled fallback values, unedited, when a warning is present", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(
      editableSettings({ warning: "Saved server settings were invalid and ignored" }),
    );
    vi.mocked(setServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("localhost"));
    const saveButton = screen.getByRole("button", { name: "Save" });
    expect(saveButton).toBeEnabled();

    fireEvent.click(saveButton);
    await waitFor(() => expect(setServerSettings).toHaveBeenCalledWith("localhost", 8080));
  });

  it("disables Save (rather than letting it submit) for an empty host", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    const host = await screen.findByLabelText("Host");
    fireEvent.change(host, { target: { value: "" } });

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(setServerSettings).not.toHaveBeenCalled();
  });

  it("disables Save (rather than letting it submit) for an out-of-range port", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    const port = await screen.findByLabelText("Port");
    fireEvent.change(port, { target: { value: "99999" } });

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(setServerSettings).not.toHaveBeenCalled();
  });

  it("disables Save for a non-numeric port", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    const host = await screen.findByLabelText("Host");
    const port = screen.getByLabelText("Port");
    fireEvent.change(host, { target: { value: "other-host" } });
    fireEvent.change(port, { target: { value: "not-a-port" } });

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(setServerSettings).not.toHaveBeenCalled();
  });

  it("shows no inline error before the user has touched a field", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    await waitFor(() => expect(screen.getByLabelText("Host")).toHaveValue("localhost"));
    expect(screen.queryByText("Host is required.")).not.toBeInTheDocument();
    expect(screen.queryByText("Port must be a number between 1 and 65535.")).not.toBeInTheDocument();
  });

  it("shows an inline error under Host once the user clears it", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    const host = await screen.findByLabelText("Host");
    await waitFor(() => expect(host).toHaveValue("localhost"));
    fireEvent.change(host, { target: { value: "" } });

    expect(await screen.findByText("Host is required.")).toBeInTheDocument();
    expect(host).toHaveAttribute("aria-invalid", "true");
  });

  it("shows an inline error under Port once the user types an out-of-range value", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    render(<ServerSettingsSection />);

    const port = await screen.findByLabelText("Port");
    await waitFor(() => expect(port).toHaveValue("8080"));
    fireEvent.change(port, { target: { value: "99999" } });

    expect(await screen.findByText("Port must be a number between 1 and 65535.")).toBeInTheDocument();
    expect(port).toHaveAttribute("aria-invalid", "true");
  });

  it("saves, shows the updated values, and re-checks the connection", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    const updated = editableSettings({ host: "new-host", port: 9999, baseUrl: "http://new-host:9999" });
    vi.mocked(setServerSettings).mockResolvedValue(updated);
    const onSaved = vi.fn();

    render(<ServerSettingsSection onSaved={onSaved} />);

    const host = await screen.findByLabelText("Host");
    const port = screen.getByLabelText("Port");
    fireEvent.change(host, { target: { value: "new-host" } });
    fireEvent.change(port, { target: { value: "9999" } });

    const saveButton = screen.getByRole("button", { name: "Save" });
    await waitFor(() => expect(saveButton).toBeEnabled());
    fireEvent.click(saveButton);

    await waitFor(() => expect(setServerSettings).toHaveBeenCalledWith("new-host", 9999));
    expect(connectionMonitor.retryNow).toHaveBeenCalledTimes(1);
    // The Library/Transcribe queue's cache is told to drop rows from the
    // old server and re-fetch against the new one right away too.
    expect(transcriptionStore.resetForServerChange).toHaveBeenCalledTimes(1);
    expect(onSaved).toHaveBeenCalledWith(updated);
    // Freshly saved values are no longer "dirty", so Save goes back to disabled.
    await waitFor(() => expect(saveButton).toBeDisabled());
    // The effective-URL line reflects the newly-saved value, not the old one.
    expect(screen.getByText("http://new-host:9999")).toBeInTheDocument();
  });

  it("shows the rejection's message and leaves the fields editable when the save fails", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    vi.mocked(setServerSettings).mockRejectedValue(new HeliosError("invalid_input", "server port must be between 1 and 65535, got 0"));

    render(<ServerSettingsSection />);

    const host = await screen.findByLabelText("Host");
    fireEvent.change(host, { target: { value: "other-host" } });
    const saveButton = screen.getByRole("button", { name: "Save" });
    await waitFor(() => expect(saveButton).toBeEnabled());
    fireEvent.click(saveButton);

    expect(await screen.findByText("server port must be between 1 and 65535, got 0")).toBeInTheDocument();
    expect(connectionMonitor.retryNow).not.toHaveBeenCalled();
    expect(transcriptionStore.resetForServerChange).not.toHaveBeenCalled();
    expect(host).toBeEnabled();
  });
});

describe("ServerSettingsSection — Test connection", () => {
  it("reports a reachable server without saving anything", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    vi.mocked(testServerConnection).mockResolvedValue({ ok: true, checks: {} });

    render(<ServerSettingsSection />);
    const testButton = screen.getByRole("button", { name: "Test connection" });
    await waitFor(() => expect(testButton).toBeEnabled());
    fireEvent.click(testButton);

    await waitFor(() => expect(testServerConnection).toHaveBeenCalledWith("localhost", 8080));
    expect(await screen.findByText("Reachable")).toBeInTheDocument();
    expect(setServerSettings).not.toHaveBeenCalled();
  });

  it("reports an unreachable server with the connection check's detail", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    vi.mocked(testServerConnection).mockResolvedValue({
      ok: false,
      checks: { connection: { ok: false, detail: "connection refused" } },
    });

    render(<ServerSettingsSection />);
    const testButton = screen.getByRole("button", { name: "Test connection" });
    await waitFor(() => expect(testButton).toBeEnabled());
    fireEvent.click(testButton);

    expect(await screen.findByText("connection refused")).toBeInTheDocument();
  });

  it("ignores a test result whose tested host/port no longer match the draft", async () => {
    vi.mocked(getServerSettings).mockResolvedValue(editableSettings());
    let resolveTest: (report: { ok: boolean; checks: Record<string, { ok: boolean; detail: string }> }) => void;
    vi.mocked(testServerConnection).mockReturnValue(
      new Promise((resolve) => {
        resolveTest = resolve;
      }),
    );

    render(<ServerSettingsSection />);
    const host = await screen.findByLabelText("Host");
    await waitFor(() => expect(host).toHaveValue("localhost"));

    const testButton = screen.getByRole("button", { name: "Test connection" });
    fireEvent.click(testButton);
    await waitFor(() => expect(testServerConnection).toHaveBeenCalledWith("localhost", 8080));

    // The user changes the Host while the request for "localhost" is
    // still in flight — the response, once it arrives, describes a value
    // no longer in the field and must not be shown as if it did.
    fireEvent.change(host, { target: { value: "somewhere-else.example" } });

    resolveTest!({ ok: true, checks: {} });
    await waitFor(() => expect(testButton).toBeEnabled());

    expect(screen.queryByText("Reachable")).not.toBeInTheDocument();
  });
});
