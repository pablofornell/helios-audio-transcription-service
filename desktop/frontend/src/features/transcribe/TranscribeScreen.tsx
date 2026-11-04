import { useToast } from "../../components";
import { Composer } from "./Composer";
import { QueueList } from "./QueueList";
import { useTranscribeQueue } from "./useTranscribeQueue";
import { HeliosError, transcriptionStore, useConnection, useTranscriptions } from "../../lib";
import { isLibraryEligible } from "../../lib/contentEligibility";
import styles from "./TranscribeScreen.module.css";

export interface TranscribeScreenProps {
  /** Called with a transcription's id when a row's "View in Library" action is used. Owned by `App.tsx`'s `screen`/`setScreen` state. */
  onOpenLibrary?: (id?: string) => void;
}

/**
 * TranscribeScreen — the composer docked above the live queue. This is the
 * only export the shell needs; everything else in `src/features/transcribe`
 * is this component's own implementation detail.
 */
export function TranscribeScreen({ onOpenLibrary }: TranscribeScreenProps) {
  const { transcriptions, hasMore, loadingMore } = useTranscriptions();
  const toast = useToast();
  const connection = useConnection();
  const queue = useTranscribeQueue();

  // First run: nothing ever transcribed, so the composer fills the surface
  // and *is* the screen. Deliberately gated on `!loading` too — during the
  // first fetch we don't yet know whether this is a first run or not, and
  // defaulting to the docked bar over the full-surface takeover is the
  // safer guess either way.
  const expanded = !queue.loading && queue.rows.length === 0;
  // The count covers the loaded rows. While older rows exist, it is a lower bound.
  const libraryCount = transcriptions.filter(isLibraryEligible).length;
  const loadMore = () => {
    transcriptionStore.loadMore().catch((err: unknown) => {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't load older transcriptions — ${heliosErr.message}` });
    });
  };
  const connectionStale = connection.status === "reconnecting";

  return (
    <div className={styles.screen}>
      {/* One `Composer` element, always at this position — its `variant`
          is what flips between first-run and docked, never the element
          itself. */}
      <Composer variant={expanded ? "expanded" : "collapsed"} submit={queue.submit} />
      {!expanded ? (
        <div className={styles.queueArea}>
          <QueueList
            rows={queue.rows}
            loading={queue.loading}
            connectionStale={connectionStale}
            isCanceling={queue.isCanceling}
            isEntering={queue.isEntering}
            cancel={queue.cancel}
            retry={queue.retry}
            clearFinished={queue.clearFinished}
            onOpenLibrary={onOpenLibrary}
            libraryCount={libraryCount}
            libraryCountIsLowerBound={hasMore}
            hasMore={hasMore}
            loadingMore={loadingMore}
            onLoadMore={loadMore}
          />
        </div>
      ) : null}
    </div>
  );
}
