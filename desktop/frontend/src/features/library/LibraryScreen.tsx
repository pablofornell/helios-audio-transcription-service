import { useEffect, useRef, useState } from "react";
import { useToast } from "../../components";
import {
  ACTIVE_POLL_MS,
  IDLE_POLL_MS,
  getServerSettingsVersion,
  deleteTranscription,
  get,
  HeliosError,
  retry,
  transcriptionStore,
  useServerSettingsVersion,
  type Transcription,
  type TranscriptFormat,
} from "../../lib";
import { hasDownloadableTranscript } from "../../lib/contentEligibility";
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
  /** Called once the screen has taken `openId`, so the shell can clear the hand-off. Without it, a later visit to the Library would open the same item again. */
  onOpenIdConsumed?: () => void;
}

/**
 * LibraryScreen — the catalogue of everything transcribed, plus its detail
 * view. Mirrors the sibling project's list+detail shape: a list column and
 * a detail panel that is a third column above `OVERLAY_BREAKPOINT` and a
 * scrim-backed overlay sheet below it.
 */
export function LibraryScreen({ openId, onOpenIdConsumed }: LibraryScreenProps) {
  const toast = useToast();
  const query = useLibraryQuery();
  const [selectedId, setSelectedId] = useState<string | undefined>(undefined);
  const [detail, setDetail] = useState<Transcription | undefined>(undefined);
  const [deleteTarget, setDeleteTarget] = useState<Transcription | undefined>(undefined);
  const [deleting, setDeleting] = useState(false);
  const [saveAsTarget, setSaveAsTarget] = useState<{ transcription: Transcription; format: TranscriptFormat } | undefined>(undefined);
  const [overlay, setOverlay] = useState(() => window.innerWidth < OVERLAY_BREAKPOINT);
  const consumedOpenIdRef = useRef<string | undefined>(undefined);
  const deleteSessionRef = useRef(0);
  const deletingSessionRef = useRef<number | undefined>(undefined);

  const settingsVersion = useServerSettingsVersion();
  const detailRequestRef = useRef(0);
  const lifecycleRef = useRef(0);
  useEffect(() => () => {
    ++detailRequestRef.current;
    ++lifecycleRef.current;
    ++deleteSessionRef.current;
    consumedOpenIdRef.current = undefined;
  }, []);
  const loadedDetail = query.loadedItems.find((item) => item.id === detail?.id);
  useEffect(() => { if (loadedDetail) setDetail(loadedDetail); }, [loadedDetail]);

  // A status filter may exclude the open job. Keep its detail live independently.
  useEffect(() => {
    if (!detail || loadedDetail) return;
    const id = detail.id;
    const requestId = detailRequestRef.current;
    const server = getServerSettingsVersion();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const run = async () => {
      try {
        const full = await get(id);
        if (stopped || requestId !== detailRequestRef.current || server !== getServerSettingsVersion()) return;
        setDetail((current) => current?.id === id ? full : current);
      } catch { /* Keep the existing detail while a transient failure recovers. */ }
      finally { if (!stopped) schedule(); }
    };
    const schedule = () => { timer = setTimeout(run, detail.status === "pending" || detail.status === "running" ? ACTIVE_POLL_MS : IDLE_POLL_MS); };
    schedule();
    return () => { stopped = true; clearTimeout(timer); };
  }, [detail?.id, detail?.status, loadedDetail, settingsVersion]);

  // Settings -> Server saving a new server: the open detail panel (if any)
  // still shows the old server's transcript, while `DetailPanel`'s own
  // audio element switches to the new server right under it (its own
  // `settingsVersion` effect, `DetailPanel.tsx`) -- the old id simply does
  // not exist there, a 404 waiting to happen. Closing it here is the
  // simplest correct behavior: the id belongs to the old server, and
  // nothing worth keeping survives the switch. The ref comparison (not a
  // plain effect on `settingsVersion`) is what keeps this from clobbering
  // an `openId` hand-off that lands in the very same first render.
  const lastSettingsVersionRef = useRef(settingsVersion);
  useEffect(() => {
    if (settingsVersion === lastSettingsVersionRef.current) return;
    lastSettingsVersionRef.current = settingsVersion;
    ++detailRequestRef.current;
    ++lifecycleRef.current;
    setSaveAsTarget(undefined);
    closeDelete();
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
    onOpenIdConsumed?.();
    void openDetail(openId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openId]);

  async function openDetail(id: string) {
    const requestId = ++detailRequestRef.current;
    const server = getServerSettingsVersion();
    setSelectedId(id);
    setDetail(undefined);
    try {
      const full = await get(id);
      if (requestId !== detailRequestRef.current || server !== getServerSettingsVersion()) return;
      setDetail(full);
    } catch (err) {
      if (requestId !== detailRequestRef.current || server !== getServerSettingsVersion()) return;
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't load details — ${heliosErr.message}` });
    }
  }

  function closeDetail() {
    ++detailRequestRef.current;
    setSelectedId(undefined);
    setDetail(undefined);
  }

  async function handleRetry(id: string) {
    const requestId = lifecycleRef.current;
    const server = getServerSettingsVersion();
    try {
      const updated = await retry(id);
      if (requestId !== lifecycleRef.current || server !== getServerSettingsVersion()) return;
      query.updateItem(id, updated);
      setDetail((current) => current?.id === id ? updated : current);
    } catch (err) {
      if (requestId !== lifecycleRef.current || server !== getServerSettingsVersion()) return;
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't retry — ${heliosErr.message}` });
    }
  }

  function openDelete(id: string) {
    ++deleteSessionRef.current;
    deletingSessionRef.current = undefined;
    setDeleting(false);
    setDeleteTarget(findById(id) ?? (detail?.id === id ? detail : undefined));
  }

  function closeDelete() {
    ++deleteSessionRef.current;
    deletingSessionRef.current = undefined;
    setDeleteTarget(undefined);
    setDeleting(false);
  }

  async function confirmDelete() {
    const session = deleteSessionRef.current;
    if (!deleteTarget || deletingSessionRef.current === session) return;
    deletingSessionRef.current = session;
    const id = deleteTarget.id;
    const requestId = lifecycleRef.current;
    const server = getServerSettingsVersion();
    const sameServer = () => requestId === lifecycleRef.current && server === getServerSettingsVersion();
    const sameDialog = () => sameServer() && session === deleteSessionRef.current;
    setDeleting(true);
    try {
      try {
        await deleteTranscription(id);
      } catch (err) {
        // The server no longer has the item, so the deletion is done.
        if (!(err instanceof HeliosError && err.code === "not_found")) throw err;
      }
      if (!sameServer()) return;
      transcriptionStore.forget(id);
      // A canceled dialog still completed the deletion on this server.
      query.removeItem(id);
      setDetail((current) => current?.id === id ? undefined : current);
      if (sameDialog()) closeDelete();
    } catch (err) {
      if (!sameDialog()) return;
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't delete — ${heliosErr.message}` });
    } finally {
      if (sameDialog()) {
        deletingSessionRef.current = undefined;
        setDeleting(false);
      }
    }
  }

  function openSaveAs(id: string, format: TranscriptFormat) {
    const transcription = detail?.id === id ? detail : query.items.find((i) => i.id === id);
    if (transcription && hasDownloadableTranscript(transcription)) setSaveAsTarget({ transcription, format });
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
          onSaveAs={(id) => openSaveAs(id, "txt")}
          onDelete={openDelete}
        />
      </div>
      {detail ? (
        <DetailPanel
          transcription={detail}
          onClose={closeDetail}
          onRetry={handleRetry}
          onSaveAs={openSaveAs}
          onDelete={openDelete}
          overlay={overlay}
        />
      ) : null}
      <DeleteConfirmDialog transcription={deleteTarget} busy={deleting} onCancel={closeDelete} onConfirm={confirmDelete} />
      <SaveAsDialog
        transcription={saveAsTarget?.transcription}
        initialFormat={saveAsTarget?.format ?? "txt"}
        onClose={() => setSaveAsTarget(undefined)}
      />
    </div>
  );
}
