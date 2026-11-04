import { useState } from "react";
import { Button, Dialog, EmptyState, SectionHeader, Skeleton, useToast } from "../../components";
import { HeliosError, deleteTranscription, transcriptionStore, type Transcription } from "../../lib";
import { useClockTick } from "./clock";
import { QueueRow } from "./QueueRow";
import { isTerminalStatus, type QueueRowModel } from "./useTranscribeQueue";
import styles from "./QueueList.module.css";

export interface QueueListProps {
  rows: QueueRowModel[];
  loading: boolean;
  /** True while `useConnection()` reports `"reconnecting"` — every non-terminal row goes stale. */
  connectionStale: boolean;
  isCanceling: (id: string) => boolean;
  isEntering: (key: string) => boolean;
  cancel: (id: string) => void;
  retry: (id: string) => void;
  clearFinished: () => void;
  onOpenLibrary?: (id?: string) => void;
  libraryCount: number;
  /** True when more rows exist than the loaded ones, so `libraryCount` is a lower bound. */
  libraryCountIsLowerBound?: boolean;
  /** True when older rows exist on the server that are not loaded yet. */
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  /** True when rows exist on the server, even if none is visible (for example after "Clear completed"). */
  hasHistory?: boolean;
}

const SKELETON_ROWS = 3;

type RealQueueRow = Extract<QueueRowModel, { kind: "real" }>;

// Date.parse drops sub-millisecond precision. Keep the fractional tail so
// submissions within the same millisecond still follow server FIFO order.
function compareTimestamps(a: string, b: string): number {
  const milliseconds = Date.parse(a) - Date.parse(b);
  if (milliseconds) return milliseconds;
  const fraction = (value: string) => (value.match(/\.(\d+)/)?.[1] ?? "").padEnd(9, "0");
  return fraction(a).localeCompare(fraction(b));
}

/** Running first, pending in submission order, then upload placeholders. */
function queueSections(rows: QueueRowModel[]) {
  const real = rows.filter((row): row is RealQueueRow => row.kind === "real");
  const oldestFirst = (a: RealQueueRow, b: RealQueueRow) =>
    compareTimestamps(a.transcription.createdAt, b.transcription.createdAt) || a.key.localeCompare(b.key);
  const running = real.filter((row) => row.transcription.status === "running").sort(oldestFirst);
  const pending = real.filter((row) => row.transcription.status === "pending").sort(oldestFirst);
  const uploading = rows.filter((row) => row.kind === "uploading").reverse();
  const recentRows = real.filter((row) => isTerminalStatus(row.transcription.status)).sort((a, b) =>
    compareTimestamps(b.transcription.completedAt || b.transcription.createdAt, a.transcription.completedAt || a.transcription.createdAt)
    || a.key.localeCompare(b.key),
  );
  return { activeRows: [...running, ...pending, ...uploading], recentRows };
}

/**
 * The Queue proper — ACTIVE / RECENT sections, each row fixed at 64px
 * regardless of status. Renders nothing at all when there is truly nothing
 * ever transcribed (no rows, no older rows, no hidden rows): the expanded
 * first-run composer is the entire screen at that point.
 */
export function QueueList({
  rows,
  loading,
  connectionStale,
  isCanceling,
  isEntering,
  cancel,
  retry,
  clearFinished,
  onOpenLibrary,
  libraryCount,
  libraryCountIsLowerBound = false,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  hasHistory = false,
}: QueueListProps) {
  const now = useClockTick();
  const toast = useToast();
  const [removalTarget, setRemovalTarget] = useState<Transcription | undefined>(undefined);
  const [removing, setRemoving] = useState(false);

  if (loading) {
    return (
      <ul className={styles.list} aria-label="Transcriptions" aria-busy="true">
        {Array.from({ length: SKELETON_ROWS }).map((_, i) => (
          <li key={i} className={styles.skeletonRow}>
            <Skeleton width={40} height={32} />
            <div className={styles.skeletonText}>
              <Skeleton width="55%" height={14} />
              <Skeleton width="35%" height={11} />
            </div>
          </li>
        ))}
      </ul>
    );
  }

  if (rows.length === 0 && !hasMore && !hasHistory) return null;

  const { activeRows, recentRows } = queueSections(rows);

  // "Clear completed" hides succeeded rows only. Failed and canceled rows stay
  // until the user removes them: the queue is their only list.
  const hasCompleted = recentRows.some((row) => row.transcription.status === "succeeded");
  const runningCount = activeRows.filter((r) => r.kind === "real" && r.transcription.status === "running").length;
  const queuedCount = activeRows.length - runningCount;

  async function confirmRemove() {
    if (!removalTarget) return;
    setRemoving(true);
    try {
      try {
        await deleteTranscription(removalTarget.id);
      } catch (err) {
        // The server no longer has the row, so the removal is done.
        if (!(err instanceof HeliosError && err.code === "not_found")) throw err;
      }
      transcriptionStore.forget(removalTarget.id);
      setRemovalTarget(undefined);
    } catch (err) {
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: `Couldn't remove — ${heliosErr.message}` });
    } finally {
      setRemoving(false);
    }
  }

  return (
    <div className={styles.container}>
      {activeRows.length === 0 && (recentRows.length > 0 || hasHistory || hasMore) ? (
        <EmptyState variant="inline">
          Queue is clear.{" "}
          {onOpenLibrary ? (
            <button type="button" className={styles.libraryLink} onClick={() => onOpenLibrary()}>
              {libraryCount}
              {libraryCountIsLowerBound ? "+" : ""} items in Library →
            </button>
          ) : (
            <span className="num">
              {libraryCount}
              {libraryCountIsLowerBound ? "+" : ""} items in Library
            </span>
          )}
        </EmptyState>
      ) : null}

      {activeRows.length > 0 ? (
        <>
          <SectionHeader sticky>ACTIVE · {sectionSummary(runningCount, queuedCount)}</SectionHeader>
          <ul className={styles.list} aria-label="Active transcriptions">
            {activeRows.map((row) => (
              <QueueRow
                key={row.key}
                model={row}
                canceling={row.kind === "real" && isCanceling(row.transcription.id)}
                entering={isEntering(row.key)}
                stale={connectionStale}
                now={now}
                onCancel={cancel}
                onRetry={retry}
                onRemove={setRemovalTarget}
                onOpenLibrary={onOpenLibrary}
              />
            ))}
          </ul>
        </>
      ) : null}

      {recentRows.length > 0 ? (
        <>
          <SectionHeader
            sticky
            action={
              hasCompleted ? (
                <Button size="sm" variant="ghost" onClick={clearFinished}>
                  Clear completed
                </Button>
              ) : undefined
            }
          >
            RECENT
          </SectionHeader>
          <ul className={styles.list} aria-label="Recent transcriptions">
            {recentRows.map((row) => (
              <QueueRow
                key={row.key}
                model={row}
                canceling={false}
                entering={isEntering(row.key)}
                stale={false}
                now={now}
                onCancel={cancel}
                onRetry={retry}
                onRemove={setRemovalTarget}
                onOpenLibrary={onOpenLibrary}
              />
            ))}
          </ul>
        </>
      ) : null}

      {hasMore ? (
        <div className={styles.footer}>
          <Button size="sm" variant="neutral" loading={loadingMore} onClick={onLoadMore}>
            Load older transcriptions
          </Button>
        </div>
      ) : null}

      <Dialog
        open={removalTarget !== undefined}
        onClose={() => setRemovalTarget(undefined)}
        title="Remove this transcription?"
        footer={
          <>
            <Button variant="neutral" onClick={() => setRemovalTarget(undefined)} data-dialog-safe-action="">
              Cancel
            </Button>
            <Button variant="danger" onClick={confirmRemove} loading={removing}>
              Remove
            </Button>
          </>
        }
      >
        {removalTarget ? <p>{removeCopy(removalTarget)}</p> : null}
      </Dialog>
    </div>
  );
}

function sectionSummary(runningCount: number, queuedCount: number): string {
  const parts: string[] = [];
  if (runningCount > 0) parts.push(`${runningCount} transcribing`);
  if (queuedCount > 0) parts.push(`${queuedCount} queued`);
  return parts.length > 0 ? parts.join(", ") : "0 active";
}

function removeCopy(transcription: Transcription): string {
  const name = transcription.audio.filename || "this file";
  return `Remove "${name}" and its transcript? This can't be undone.`;
}
