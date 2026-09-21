import { useEffect, useRef, useState } from "react";
import { useToast } from "../../components";
import {
  deleteTranscription,
  get,
  HeliosError,
  retry,
  useServerSettingsVersion,
  type Transcription,
  type TranscriptFormat,
} from "../../lib";
import { DeleteConfirmDialog } from "./DeleteConfirmDialog";
import { DetailPanel } from "./DetailPanel";
import { SaveAsDialog } from "./SaveAsDialog";
import { LibraryList } from "./LibraryList";
import { useLibraryQuery } from "./useLibraryQuery";
import styles from "./LibraryScreen.module.css";

const OVERLAY_BREAKPOINT = 1120;

export interface LibraryScreenProps {
  /** A transcription id the Transcribe queue asked us to open, e.g. via a row's "View in Library" action — a one-shot hand-off from `App.tsx`, not shared state. */
  openId?: string;
}

/**
 * LibraryScreen — the catalogue of everything transcribed, plus its detail
 * view. Mirrors the sibling project's list+detail shape: a list column and
 * a detail panel that is a third column above `OVERLAY_BREAKPOINT` and a
 * scrim-backed overlay sheet below it.
 */
export function LibraryScreen({ openId }: LibraryScreenProps) {
  const toast = useToast();
  const query = useLibraryQuery();
  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const [detail, setDetail] = useState<Transcription | undefined>(undefined);
  const [deleteTarget, setDeleteTarget] = useState<Transcription | undefined>(undefined);
  const [deleting, setDeleting] = useState(false);
  const [saveAsTarget, setSaveAsTarget] = useState<{ transcription: Transcription; format: TranscriptFormat } | undefined>(undefined);
  const [overlay, setOverlay] = useState(() => window.innerWidth < OVERLAY_BREAKPOINT);
  const consumedOpenIdRef = useRef<string | undefined>(undefined);

  // Settings -> Server saving a new server: the open detail panel (if any)
  // still shows the old server's transcript, while `DetailPanel`'s own
  // audio element switches to the new server right under it (its own
  // `settingsVersion` effect, `DetailPanel.tsx`) -- the old id simply does
  // not exist there, a 404 waiting to happen. Closing it here is the
  // simplest correct behavior: the id belongs to the old server, and
  // nothing worth keeping survives the switch. The ref comparison (not a
  // plain effect on `settingsVersion`) is what keeps this from clobbering
  // an `openId` hand-off that lands in the very same first render.
  const settingsVersion = useServerSettingsVersion();
  const lastSettingsVersionRef = useRef(settingsVersion);
  useEffect(() => {
    if (settingsVersion === lastSettingsVersionRef.current) return;
    lastSettingsVersionRef.current = settingsVersion;
    setSelectedId(undefined);
    setDetail(undefined);
  }, [settingsVersion]);

  useEffect(() => {
    function onResize() {
      setOverlay(window.innerWidth < OVERLAY_BREAKPOINT);
    }
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  // A queue row's "View in Library" hand-off — open exactly once per id,
  // regardless of how many times this screen re-renders with the same prop.
  useEffect(() => {
    if (!openId || consumedOpenIdRef.current === openId) return;
    consumedOpenIdRef.current = openId;
    void openDetail(openId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openId]);

  async function openDetail(id: string) {
    setSelectedId(id);
    try {
      const full = await get(id);
      setDetail(full);
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't load details — ${heliosErr.message}` });
    }
  }

  function closeDetail() {
    setDetail(undefined);
  }

  async function handleRetry(id: string) {
    try {
      const updated = await retry(id);
      query.updateItem(id, updated);
      if (detail?.id === id) setDetail(updated);
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't retry — ${heliosErr.message}` });
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await deleteTranscription(deleteTarget.id);
      query.removeItem(deleteTarget.id);
      if (detail?.id === deleteTarget.id) setDetail(undefined);
      setDeleteTarget(undefined);
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't delete — ${heliosErr.message}` });
    } finally {
      setDeleting(false);
    }
  }

  function openSaveAs(id: string, format: TranscriptFormat) {
    const transcription = detail?.id === id ? detail : query.items.find((i) => i.id === id);
    if (transcription) setSaveAsTarget({ transcription, format });
  }

  function findById(id: string): Transcription | undefined {
    return query.items.find((i) => i.id === id);
  }

  return (
    <div className={styles.screen}>
      <div className={styles.listColumn}>
        <LibraryList
          {...query}
          selectedId={selectedId}
          onSelect={setSelectedId}
          onOpenDetail={openDetail}
          onRetry={handleRetry}
          onSaveAs={(id) => openSaveAs(id, "txt")}
          onDelete={(id) => setDeleteTarget(findById(id) ?? detail)}
        />
      </div>
      {detail ? (
        <DetailPanel
          transcription={detail}
          onClose={closeDetail}
          onRetry={handleRetry}
          onSaveAs={openSaveAs}
          onDelete={(id) => setDeleteTarget(findById(id) ?? detail)}
          overlay={overlay}
        />
      ) : null}
      <DeleteConfirmDialog transcription={deleteTarget} busy={deleting} onCancel={() => setDeleteTarget(undefined)} onConfirm={confirmDelete} />
      <SaveAsDialog
        transcription={saveAsTarget?.transcription}
        initialFormat={saveAsTarget?.format ?? "txt"}
        onClose={() => setSaveAsTarget(undefined)}
      />
    </div>
  );
}
