import { forwardRef, type MouseEvent } from "react";
import { IconButton, Row, StatusPill } from "../../components";
import { formatAbsolute, formatBytes, type Transcription } from "../../lib";
import { buildLibraryMeta, libraryStatusSlot } from "./helpers";

export interface LibraryRowProps {
  transcription: Transcription;
  selected: boolean;
  onSelect: (id: string) => void;
  onOpenDetail: (id: string) => void;
  /** Opens the shared menu, anchored below the "More actions" button — the menu itself lives once in `LibraryList`, not per row. */
  onMoreClick: (id: string, anchor: HTMLElement) => void;
  onContextMenu: (id: string, e: MouseEvent) => void;
}

function AudioGlyph() {
  return (
    <svg width="16" height="16" viewBox="0 0 18 18" fill="none" aria-hidden="true">
      <path d="M6 5.5h4l3 3v6a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V6.5a1 1 0 0 1 1-1z" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
      <path d="M7 10.5v2.2M9 9.2v4.8M11 10.5v2.2" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  );
}

function MoreGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <circle cx="3" cy="7" r="1.2" fill="currentColor" />
      <circle cx="7" cy="7" r="1.2" fill="currentColor" />
      <circle cx="11" cy="7" r="1.2" fill="currentColor" />
    </svg>
  );
}

/** One row of the Library list, built on the shared `Row` primitive. */
export const LibraryRow = forwardRef<HTMLLIElement, LibraryRowProps>(function LibraryRow(
  { transcription, selected, onSelect, onOpenDetail, onMoreClick, onContextMenu },
  ref,
) {
  const status = libraryStatusSlot(transcription);
  const railStatus = status ?? "succeeded";
  const titleId = `lib-row-title-${transcription.id}`;
  const descId = `lib-row-desc-${transcription.id}`;
  const title = transcription.audio.filename || transcription.id;

  /** The one path to "open the detail panel" — double-click and `Enter` both route through this, so they cannot drift apart. */
  function open() {
    onSelect(transcription.id);
    onOpenDetail(transcription.id);
  }

  return (
    <Row
      ref={ref}
      variant="library"
      status={railStatus}
      selected={selected}
      thumb={
        <span aria-hidden="true" style={{ display: "flex", alignItems: "center", justifyContent: "center", width: "100%", height: "100%", color: "var(--text-tertiary)" }}>
          <AudioGlyph />
        </span>
      }
      title={
        <span id={titleId} className="selectable" title={title} dir="auto">
          {title}
        </span>
      }
      meta={
        <span id={descId} title={formatAbsolute(transcription.completedAt ?? transcription.createdAt)}>
          {buildLibraryMeta(transcription)}
        </span>
      }
      statusSlot={status ? <StatusPill status={status} errorCode={transcription.error?.code} dense /> : undefined}
      metric={<span className="num">{transcription.audio.sizeBytes > 0 ? formatBytes(transcription.audio.sizeBytes) : "—"}</span>}
      secondaryActions={
        <IconButton
          size={28}
          icon={<MoreGlyph />}
          label="More actions"
          onClick={(e) => {
            e.stopPropagation();
            onMoreClick(transcription.id, e.currentTarget);
          }}
          onDoubleClick={(e) => e.stopPropagation()}
        />
      }
      tabIndex={0}
      aria-labelledby={titleId}
      aria-describedby={descId}
      onClick={() => onSelect(transcription.id)}
      onDoubleClick={open}
      onMouseDown={(e) => {
        if (e.detail > 1) e.preventDefault();
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          open();
        }
      }}
      onFocus={() => onSelect(transcription.id)}
      onContextMenu={(e) => {
        e.preventDefault();
        onSelect(transcription.id);
        onContextMenu(transcription.id, e);
      }}
    />
  );
});
