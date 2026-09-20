import { useState } from "react";
import { Button, Dialog, EmptyState, SectionHeader, Skeleton, useToast } from "../../components";
import { HeliosError, deleteTranscription, type Transcription } from "../../lib";
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
}

const SKELETON_ROWS = 3;

/** The rows a keyboard user can actually land on — uploading placeholders are skipped since they carry no id yet. */
function realRows(rows: QueueRowModel[]): Extract<QueueRowModel, { kind: "real" }>[] {
  return rows.filter((r): r is Extract<QueueRowModel, { kind: "real" }> => r.kind === "real");
}

/**
 * The Queue proper — ACTIVE / RECENT sections, each row fixed at 64px
 * regardless of status. Renders nothing at all when there is truly nothing
 * ever transcribed (`rows.length === 0`): the expanded first-run composer
 * is the entire screen at that point.
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

  if (rows.length === 0) return null;

  const activeRows = rows.filter((r) => r.kind === "uploading" || !isTerminalStatus(r.transcription.status));
  const recentRows = realRows(rows).filter((r) => isTerminalStatus(r.transcription.status));

  const runningCount = activeRows.filter((r) => r.kind === "real" && r.transcription.status === "running").length;
  const queuedCount = activeRows.length - runningCount;

  async function confirmRemove() {
    if (!removalTarget) return;
    setRemoving(true);
    try {
      await deleteTranscription(removalTarget.id);
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
      {activeRows.length === 0 && recentRows.length > 0 ? (
        <EmptyState variant="inline">
          Queue is clear.{" "}
          {onOpenLibrary ? (
            <button type="button" className={styles.libraryLink} onClick={() => onOpenLibrary()}>
              {libraryCount}
              {libraryCount >= 200 ? "+" : ""} items in Library →
            </button>
          ) : (
            <span className="num">
              {libraryCount}
              {libraryCount >= 200 ? "+" : ""} items in Library
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
              <Button size="sm" variant="ghost" onClick={clearFinished}>
                Clear finished
              </Button>
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
