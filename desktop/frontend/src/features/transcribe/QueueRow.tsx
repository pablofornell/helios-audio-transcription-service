import { useId, useRef, useState, type KeyboardEvent, type MouseEvent, type ReactNode } from "react";
import { IconButton, Menu, ProgressBar, Row, StatusPill, useContextMenu, type MenuItem } from "../../components";
import {
  displayStatus as computeDisplayStatus,
  formatBytes,
  formatDuration,
  formatLanguages,
  formatTimestamp,
  secondsSince,
  type DisplayStatus,
  type Transcription,
} from "../../lib";
import { errorCopyFor } from "./errorCopy";
import { isTerminalStatus, type QueueRowModel } from "./useTranscribeQueue";
import styles from "./QueueRow.module.css";

export interface QueueRowProps {
  model: QueueRowModel;
  canceling: boolean;
  entering: boolean;
  /** True while the connection is lost and this row is still non-terminal. */
  stale: boolean;
  /** `Date.now()` from the shared 1Hz clock (`useClockTick`) — never `new Date()` per row. */
  now: number;
  onCancel: (id: string) => void;
  onRetry: (id: string) => void;
  /** Shows the "Priority processing" button on eligible rows. Omitted: no button. */
  onPrioritize?: (id: string) => void;
  onRemove: (transcription: Transcription) => void;
  onOpenLibrary?: (id?: string) => void;
}

/**
 * One Queue row. Fixed 64px in every status — every branch below only ever
 * changes what goes *inside* the fixed thumb/title/meta/status/metric/action
 * slots `Row` lays out, never the slots themselves or the row's height.
 * Handles both a `QueueRowModel` still uploading (no real id yet) and a
 * fully-known `Transcription`.
 */
export function QueueRow({ model, canceling, entering, stale, now, onCancel, onRetry, onPrioritize, onRemove, onOpenLibrary }: QueueRowProps) {
  const uid = useId();
  const titleId = `${uid}-title`;
  const statusId = `${uid}-status`;
  const metricId = `${uid}-metric`;
  const ctxMenu = useContextMenu();

  if (model.kind === "uploading") {
    return (
      <Row
        variant="queue"
        status="pending"
        className={entering ? styles.entering : undefined}
        aria-labelledby={titleId}
        aria-describedby={statusId}
        thumb={
          <span className={styles.thumbPlaceholder}>
            <AudioGlyph />
          </span>
        }
        title={
          <span id={titleId} className={[styles.title, "num", "selectable"].join(" ")}>
            {model.name}
          </span>
        }
        meta={<span className={styles.meta}>uploading…</span>}
        statusSlot={
          <span id={statusId}>
            <StatusPill status="pending" />
          </span>
        }
        metric={
          <span id={metricId} className={["num", styles.metric].join(" ")}>
            —
          </span>
        }
        lane={<ProgressBar mode="indeterminate" value={0} label={model.name} />}
      />
    );
  }

  const { transcription } = model;
  const status = computeDisplayStatus(transcription, canceling);

  function open() {
    onOpenLibrary?.(transcription.id);
  }

  const title = transcription.audio.filename || transcription.id;
  const meta = renderMeta(transcription, status, stale, now);
  const metric = renderMetric(transcription, status, now);
  const { mode: laneMode, value: laneValue } = laneFor(transcription, status);

  return (
    <Row
      variant="queue"
      status={status}
      stale={stale}
      className={entering ? styles.entering : undefined}
      tabIndex={0}
      data-row-key={model.key}
      onKeyDown={(e) => {
        // Enter on a button, link or menu item inside the row must do the
        // action of that control. The row handler must not cancel its click.
        if (e.key === "Enter" && onOpenLibrary && !isFromInteractiveChild(e)) {
          e.preventDefault();
          open();
        }
      }}
      onDoubleClick={onOpenLibrary ? open : undefined}
      onMouseDown={(e) => {
        if (e.detail > 1) e.preventDefault();
      }}
      onContextMenu={ctxMenu.onContextMenu}
      aria-labelledby={titleId}
      aria-describedby={`${statusId} ${metricId}`}
      thumb={
        <span className={styles.thumbPlaceholder}>
          <AudioGlyph />
        </span>
      }
      title={
        <span id={titleId} className={[styles.title, "num", "selectable"].join(" ")} title={title}>
          {title}
        </span>
      }
      meta={meta}
      statusSlot={
        <span id={statusId} className={styles.statusGroup}>
          {hasPriority(transcription) ? <PriorityBadge /> : null}
          <StatusPill status={status} errorCode={transcription.error?.code} />
        </span>
      }
      metric={
        <span id={metricId} className={["num", styles.metric].join(" ")}>
          {metric}
        </span>
      }
      primaryAction={
        <PrimaryAction transcription={transcription} status={status} onCancel={onCancel} onRetry={onRetry} onPrioritize={onPrioritize} />
      }
      secondaryActions={
        <RowMenu transcription={transcription} status={status} onRetry={onRetry} onRemove={onRemove} onOpenLibrary={onOpenLibrary} ctxMenu={ctxMenu} />
      }
      lane={<ProgressBar mode={laneMode} value={laneValue} label={title} />}
    />
  );
}

const INTERACTIVE_SELECTOR = "button, a[href], input, select, textarea, [role='button'], [role='menuitem']";

/** True when a key event started in a control inside the row, not on the row itself. */
function isFromInteractiveChild(e: KeyboardEvent<HTMLElement>): boolean {
  return e.target !== e.currentTarget && e.target instanceof Element && e.target.closest(INTERACTIVE_SELECTOR) !== null;
}

function AudioGlyph() {
  return (
    <svg width="18" height="18" viewBox="0 0 18 18" fill="none" aria-hidden="true">
      <path d="M6 5.5h4l3 3v6a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V6.5a1 1 0 0 1 1-1z" stroke="currentColor" strokeWidth="1.3" strokeLinejoin="round" />
      <path d="M7 10.5v2.2M9 9.2v4.8M11 10.5v2.2" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  );
}

/** True while the user's priority request is active: set, and the row has not left the queue. */
function hasPriority(transcription: Transcription): boolean {
  return Boolean(transcription.priorityAt) && (transcription.status === "pending" || transcription.status === "running");
}

/** The "this row goes first" marker. The glyph and the label always show together. */
function PriorityBadge() {
  return (
    <span className={styles.priorityBadge}>
      <PriorityGlyph className={styles.priorityGlyph} />
      <span>Priority</span>
    </span>
  );
}

function renderMeta(transcription: Transcription, status: DisplayStatus, stale: boolean, now: number): ReactNode {
  if (stale && (status === "pending" || status === "running")) {
    const seconds = secondsSince(transcription.updatedAt, new Date(now));
    return <span className={styles.meta}>last update {seconds ?? 0}s ago</span>;
  }

  if (status === "pending" && transcription.attempts > 0) {
    return (
      <span className={styles.meta}>
        Retry {transcription.attempts} of {transcription.maxAttempts}
      </span>
    );
  }

  // This service has no cooldown that spans several rows, and no rate limit
  // from an outside service. A priority row that is not running waits for
  // one thing only: a free worker slot. Say so, or the row shows nothing
  // about why it is not running.
  if (status === "pending" && hasPriority(transcription)) {
    return <span className={styles.meta}>Waiting for the next slot</span>;
  }

  if (status === "failed") {
    return <span className={styles.meta}>{errorCopyFor(transcription.error?.code).headline}</span>;
  }
  if (status === "canceled") {
    return <span className={styles.meta}>Canceled before finishing</span>;
  }

  const segments: string[] = [];
  segments.push(transcription.result?.model || transcription.options.model || "default model");
  segments.push(formatLanguages(transcription));
  if (transcription.audio.durationSeconds > 0) segments.push(formatDuration(transcription.audio.durationSeconds));
  else if (transcription.audio.sizeBytes > 0) segments.push(formatBytes(transcription.audio.sizeBytes));

  return (
    <span className={styles.meta}>
      {segments.map((segment, i) => (
        <span key={i}>
          {i > 0 ? <span className={styles.sep}> · </span> : null}
          {segment}
        </span>
      ))}
    </span>
  );
}

function renderMetric(transcription: Transcription, status: DisplayStatus, now: number): ReactNode {
  if (status === "running" && transcription.startedAt) {
    const elapsed = secondsSince(transcription.startedAt, new Date(now));
    return elapsed !== undefined ? formatTimestamp(elapsed) : "—";
  }
  if (status === "succeeded") {
    return formatDuration(transcription.audio.durationSeconds);
  }
  return "—";
}

function laneFor(transcription: Transcription, status: DisplayStatus): { mode: "determinate" | "indeterminate" | "stale" | "complete" | "failed"; value: number } {
  switch (status) {
    case "pending":
      return { mode: "determinate", value: 0 };
    case "running":
      // The API reports no progress percentage for a running job (no
      // `progressPercent` field in transcriptionDTO) — indeterminate is
      // the honest rendering, not a guessed number.
      return { mode: "indeterminate", value: 0 };
    case "succeeded":
      return { mode: "complete", value: 100 };
    case "failed":
      return { mode: "failed", value: 0 };
    case "canceled":
    case "canceling":
      return { mode: "stale", value: 0 };
    default:
      return { mode: "determinate", value: 0 };
  }
}

interface PrimaryActionProps {
  transcription: Transcription;
  status: DisplayStatus;
  onCancel: (id: string) => void;
  onRetry: (id: string) => void;
  onPrioritize?: (id: string) => void;
}

function stopDoubleClick(e: MouseEvent<HTMLButtonElement>) {
  e.stopPropagation();
}

/** Pending rows and failed rows with a Retry control can go first. The server rejects running, succeeded and canceled rows. */
function canPrioritize(transcription: Transcription, status: DisplayStatus): boolean {
  if (status === "pending") return true;
  return status === "failed" && errorCopyFor(transcription.error?.code).canRetry;
}

function PrimaryAction(props: PrimaryActionProps) {
  const { transcription, status, onPrioritize } = props;
  const action = <StatusAction {...props} />;
  if (!onPrioritize || !canPrioritize(transcription, status)) return action;
  return (
    <>
      <IconButton
        icon={<PriorityGlyph />}
        label="Priority processing"
        onClick={() => onPrioritize(transcription.id)}
        onDoubleClick={stopDoubleClick}
      />
      {action}
    </>
  );
}

function StatusAction({ transcription, status, onCancel, onRetry }: PrimaryActionProps) {
  if (status === "pending" || status === "running") {
    return <IconButton icon={<CancelGlyph />} label="Cancel" onClick={() => onCancel(transcription.id)} onDoubleClick={stopDoubleClick} />;
  }
  if (status === "canceling") {
    return <IconButton icon={<CancelGlyph />} label="Canceling…" disabled onDoubleClick={stopDoubleClick} />;
  }
  if (status === "failed" && errorCopyFor(transcription.error?.code).canRetry) {
    return <IconButton icon={<RetryGlyph />} label="Retry" onClick={() => onRetry(transcription.id)} onDoubleClick={stopDoubleClick} />;
  }
  return null;
}

interface RowMenuProps {
  transcription: Transcription;
  status: DisplayStatus;
  onRetry: (id: string) => void;
  onRemove: (transcription: Transcription) => void;
  onOpenLibrary?: (id?: string) => void;
  ctxMenu: ReturnType<typeof useContextMenu>;
}

function RowMenu({ transcription, status, onRetry, onRemove, onOpenLibrary, ctxMenu }: RowMenuProps) {
  const triggerRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);

  const items: MenuItem[] = [];
  if (onOpenLibrary) {
    items.push({ id: "library", label: "View in Library", onSelect: () => onOpenLibrary(transcription.id) });
  }
  if (status === "failed" && errorCopyFor(transcription.error?.code).canRetry) {
    items.push({ id: "retry", label: "Retry", onSelect: () => onRetry(transcription.id) });
  }
  if (isTerminalStatus(transcription.status)) {
    items.push({ id: "remove", label: "Remove", destructive: true, onSelect: () => onRemove(transcription) });
  }
  if (items.length === 0) return null;

  return (
    <>
      <IconButton ref={triggerRef} icon={<MoreGlyph />} label="More actions" onClick={() => setOpen((v) => !v)} onDoubleClick={stopDoubleClick} />
      <Menu items={items} open={open} onClose={() => setOpen(false)} anchorRef={triggerRef} returnFocusRef={triggerRef} />
      <Menu items={items} open={ctxMenu.open} position={ctxMenu.position} onClose={ctxMenu.close} returnFocusRef={triggerRef} />
    </>
  );
}

function PriorityGlyph({ className }: { className?: string }) {
  return (
    <svg className={className} width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <path d="M3.4 7.6L7 4l3.6 3.6M3.4 11L7 7.4 10.6 11" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}

function CancelGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <rect x="3" y="3" width="8" height="8" rx="1" fill="currentColor" />
    </svg>
  );
}

function RetryGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <path d="M11 7A4 4 0 1 1 9.5 3.8" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      <path d="M9 2.5L9.7 4.3L11.5 3.9" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
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
