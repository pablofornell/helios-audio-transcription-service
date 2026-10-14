import { useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { Button, EmptyState, Menu, Segmented, Skeleton, TextField, type MenuItem } from "../../components";
import { hasDownloadableTranscript } from "../../lib/contentEligibility";
import { errorCopyFor } from "../transcribe/errorCopy";
import { STATUS_FILTER_LABELS, type StatusFilterValue } from "./helpers";
import { LibraryRow } from "./LibraryRow";
import type { UseLibraryQueryResult } from "./useLibraryQuery";
import styles from "./LibraryList.module.css";

export interface LibraryListProps extends UseLibraryQueryResult {
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onOpenDetail: (id: string) => void;
  onRetry: (id: string) => void;
  onSaveAs: (id: string) => void;
  onDelete: (id: string) => void;
}

const STATUS_CHIPS: { value: StatusFilterValue | "all"; label: string }[] = [
  { value: "all", label: "All" },
  { value: "succeeded", label: STATUS_FILTER_LABELS.succeeded },
  { value: "failed", label: STATUS_FILTER_LABELS.failed },
  { value: "canceled", label: STATUS_FILTER_LABELS.canceled },
];

const SKELETON_ROWS = 6;

/** Library's list column: search + status filter toolbar, the scrollable row list, and the shared "more actions" menu every row's `⋯` opens. */
export function LibraryList({
  items,
  loading,
  loadingMore,
  error,
  hasMore,
  status,
  setStatus,
  search,
  setSearch,
  loadMore,
  refresh,
  selectedId,
  onSelect,
  onOpenDetail,
  onRetry,
  onSaveAs,
  onDelete,
}: LibraryListProps) {
  const [menuFor, setMenuFor] = useState<{ id: string; anchor: HTMLElement } | undefined>(undefined);
  const [menuPosition, setMenuPosition] = useState<{ x: number; y: number } | undefined>(undefined);
  const anchorRef = useRef<HTMLElement | null>(null);

  function openMenu(id: string, anchor: HTMLElement) {
    anchorRef.current = anchor;
    setMenuPosition(undefined);
    setMenuFor({ id, anchor });
  }

  function openContextMenu(id: string, e: ReactMouseEvent) {
    anchorRef.current = null;
    setMenuPosition({ x: e.clientX, y: e.clientY });
    setMenuFor({ id, anchor: e.currentTarget as HTMLElement });
  }

  function closeMenu() {
    setMenuFor(undefined);
    setMenuPosition(undefined);
  }

  const menuItem = menuFor ? items.find((i) => i.id === menuFor.id) : undefined;
  const menuItems: MenuItem[] = menuItem
    ? [
        ...(menuItem.status === "failed" && errorCopyFor(menuItem.error?.code).retryable
          ? [{ id: "retry", label: "Retry", onSelect: () => onRetry(menuItem.id) }]
          : []),
        ...(hasDownloadableTranscript(menuItem) ? [{ id: "save", label: "Save transcript as…", onSelect: () => onSaveAs(menuItem.id) }] : []),
        { id: "delete", label: "Delete…", destructive: true, onSelect: () => onDelete(menuItem.id) },
      ]
    : [];

  return (
    <div className={styles.root}>
      <div className={styles.toolbar}>
        <div className={styles.search}>
          <TextField
            variant="search"
            placeholder="Search by filename…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            aria-label="Search transcriptions"
          />
        </div>
        <Segmented
          label="Status"
          value={status ?? "all"}
          onChange={(v) => setStatus(v === "all" ? undefined : (v as StatusFilterValue))}
          options={STATUS_CHIPS}
        />
      </div>

      <div className={styles.listArea}>
        {loading ? (
          <ul className={styles.list} aria-label="Transcriptions" aria-busy="true">
            {Array.from({ length: SKELETON_ROWS }).map((_, i) => (
              <li key={i} className={styles.skeletonRow}>
                <Skeleton width={32} height={24} />
                <div className={styles.skeletonText}>
                  <Skeleton width="60%" height={13} />
                  <Skeleton width="40%" height={11} />
                </div>
              </li>
            ))}
          </ul>
        ) : error ? (
          <div className={styles.errorState}>
            <p>Couldn't load the library — {error.message}</p>
            <Button size="sm" variant="neutral" onClick={() => refresh()}>
              Try again
            </Button>
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            variant="inline"
            body={
              search.trim() || status
                ? "Nothing matches the current filters."
                : "No stored audio yet — uploaded files show up here."
            }
          />
        ) : (
          <>
            <ul className={styles.list} aria-label="Transcriptions">
              {items.map((t) => (
                <LibraryRow
                  key={t.id}
                  transcription={t}
                  selected={t.id === selectedId}
                  onSelect={onSelect}
                  onOpenDetail={onOpenDetail}
                  onMoreClick={openMenu}
                  onContextMenu={openContextMenu}
                />
              ))}
            </ul>
            {hasMore ? (
              <div className={styles.footer}>
                <Button size="sm" variant="neutral" loading={loadingMore} onClick={loadMore}>
                  Load more
                </Button>
              </div>
            ) : null}
          </>
        )}
      </div>

      <Menu
        items={menuItems}
        open={menuFor !== undefined}
        onClose={closeMenu}
        anchorRef={menuPosition ? undefined : anchorRef}
        position={menuPosition}
        returnFocusRef={anchorRef}
      />
    </div>
  );
}
