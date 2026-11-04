import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LibraryScreen } from "./LibraryScreen";
import { HeliosError, type Transcription } from "../../lib";

const { removeItem, deleteTranscription, show } = vi.hoisted(() => ({
  removeItem: vi.fn(), deleteTranscription: vi.fn(), show: vi.fn(),
}));
vi.mock("../../lib", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib")>()),
  deleteTranscription: (...args: unknown[]) => deleteTranscription(...args),
}));
vi.mock("../../components", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../components")>()),
  useToast: () => ({ show }),
}));
vi.mock("./useLibraryQuery", () => ({
  useLibraryQuery: () => ({
    items: [{ id: "a" }, { id: "b" }], loadedItems: [], removeItem,
  }),
}));
vi.mock("./LibraryList", () => ({
  LibraryList: ({ onDelete }: { onDelete: (id: string) => void }) => (
    <><button onClick={() => onDelete("a")}>Open A</button><button onClick={() => onDelete("b")}>Open B</button></>
  ),
}));
vi.mock("./DeleteConfirmDialog", () => ({
  DeleteConfirmDialog: ({ transcription, busy, onCancel, onConfirm }: {
    transcription?: Transcription; busy: boolean; onCancel: () => void; onConfirm: () => void;
  }) => transcription ? (
    <div data-testid="delete-dialog">
      <span data-testid="delete-target">{transcription.id}</span>
      <span data-testid="delete-busy">{String(busy)}</span>
      <button onClick={onCancel}>Cancel delete</button>
      <button onClick={onConfirm}>Confirm delete</button>
      <button onClick={() => { onConfirm(); onConfirm(); }}>Confirm twice</button>
    </div>
  ) : null,
}));

beforeEach(() => {
  deleteTranscription.mockReset();
  removeItem.mockReset();
  show.mockReset();
});

describe("LibraryScreen delete dialog sessions", () => {
  it.each(["success", "failure"])("keeps B busy when canceled deletion A later returns %s", async (outcome) => {
    let finishA: () => void = () => {};
    deleteTranscription.mockImplementationOnce(() => new Promise<void>((resolve, reject) => {
      finishA = outcome === "success" ? resolve : () => reject(new Error("old deletion failed"));
    }));
    let finishB: () => void = () => {};
    deleteTranscription.mockImplementationOnce(() => new Promise<void>((resolve) => { finishB = resolve; }));
    render(<LibraryScreen />);
    fireEvent.click(screen.getByText("Open A"));
    fireEvent.click(screen.getByText("Confirm delete"));
    fireEvent.click(screen.getByText("Cancel delete"));
    fireEvent.click(screen.getByText("Open B"));
    fireEvent.click(screen.getByText("Confirm delete"));
    await act(async () => finishA());
    expect(screen.getByTestId("delete-target")).toHaveTextContent("b");
    expect(screen.getByTestId("delete-busy")).toHaveTextContent("true");
    expect(show).not.toHaveBeenCalled();
    if (outcome === "success") expect(removeItem).toHaveBeenCalledWith("a");
    fireEvent.click(screen.getByText("Confirm delete"));
    expect(deleteTranscription).toHaveBeenCalledTimes(2);
    await act(async () => finishB());
    expect(screen.queryByTestId("delete-dialog")).not.toBeInTheDocument();
  });

  it("treats not_found as a finished deletion and shows no error", async () => {
    deleteTranscription.mockRejectedValueOnce(new HeliosError("not_found", "gone"));
    render(<LibraryScreen />);
    fireEvent.click(screen.getByText("Open A"));
    await act(async () => { fireEvent.click(screen.getByText("Confirm delete")); });
    expect(removeItem).toHaveBeenCalledWith("a");
    expect(show).not.toHaveBeenCalled();
    expect(screen.queryByTestId("delete-dialog")).not.toBeInTheDocument();
  });

  it("shows an error for another delete failure", async () => {
    deleteTranscription.mockRejectedValueOnce(new HeliosError("internal", "boom"));
    render(<LibraryScreen />);
    fireEvent.click(screen.getByText("Open A"));
    await act(async () => { fireEvent.click(screen.getByText("Confirm delete")); });
    expect(removeItem).not.toHaveBeenCalled();
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("fences a canceled session when the same id is reopened", async () => {
    let finish: () => void = () => {};
    deleteTranscription.mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
    render(<LibraryScreen />);
    fireEvent.click(screen.getByText("Open A"));
    fireEvent.click(screen.getByText("Confirm delete"));
    fireEvent.click(screen.getByText("Cancel delete"));
    fireEvent.click(screen.getByText("Open A"));
    await act(async () => finish());
    expect(screen.getByTestId("delete-target")).toHaveTextContent("a");
    expect(screen.getByTestId("delete-busy")).toHaveTextContent("false");
  });

  it("sends only one request when confirmation repeats before React rerenders", async () => {
    let finish: () => void = () => {};
    deleteTranscription.mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
    render(<LibraryScreen />);
    fireEvent.click(screen.getByText("Open A"));
    fireEvent.click(screen.getByText("Confirm twice"));
    expect(deleteTranscription).toHaveBeenCalledTimes(1);
    expect(deleteTranscription).toHaveBeenCalledWith("a");
    await act(async () => finish());
  });
});
