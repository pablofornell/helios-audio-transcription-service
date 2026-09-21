import { useEffect, useRef, useState } from "react";
import { Button, IconButton, Segmented, Skeleton, StatusPill, useToast } from "../../components";
import {
  audioLocation,
  formatAbsolute,
  formatBytes,
  formatDuration,
  formatRelative,
  formatTimestamp,
  getServerSettings,
  HeliosError,
  openAudio,
  revealInFileManager,
  transcript as fetchTranscript,
  useServerSettingsVersion,
  type MediaLocation as MediaLocationInfo,
  type Transcription,
  type TranscriptFormat,
} from "../../lib";
import { errorCopyFor } from "../transcribe/errorCopy";
import { isWithinOwnedWidget } from "./helpers";
import styles from "./DetailPanel.module.css";

export interface DetailPanelProps {
  transcription: Transcription;
  onClose: () => void;
  onRetry: (id: string) => void | Promise<void>;
  onSaveAs: (id: string, format: TranscriptFormat) => void;
  onDelete: (id: string) => void;
  /** Below the overlay breakpoint the panel is a sheet with a scrim rather than a third column. */
  overlay: boolean;
}

const FORMAT_OPTIONS: { value: TranscriptFormat; label: string }[] = [
  { value: "txt", label: "Text" },
  { value: "srt", label: "SRT" },
  { value: "vtt", label: "VTT" },
  { value: "json", label: "JSON" },
];

/** The Library detail view — a real transcript viewer: full text, timestamped segments, a format switcher, copy/save-as, and source-audio actions. */
export function DetailPanel({ transcription, onClose, onRetry, onSaveAs, onDelete, overlay }: DetailPanelProps) {
  const toast = useToast();
  const panelRef = useRef<HTMLDivElement>(null);
  const [mediaLoc, setMediaLoc] = useState<MediaLocationInfo | undefined>(undefined);
  const [serverBase, setServerBase] = useState("");
  const [format, setFormat] = useState<TranscriptFormat>("txt");
  const [content, setContent] = useState<Partial<Record<TranscriptFormat, string>>>({});
  const [contentLoading, setContentLoading] = useState(false);
  const [contentError, setContentError] = useState<HeliosError | undefined>(undefined);
  const [retrying, setRetrying] = useState(false);
  const [opening, setOpening] = useState(false);
  const [revealing, setRevealing] = useState(false);
  const audioRef = useRef<HTMLAudioElement>(null);
  // Bumped by `setServerSettings` (`../../lib/serverSettingsSignal.ts`) the
  // moment Settings -> Server saves a new one — included below so the
  // audio effect re-reads `getServerSettings()` right then, not only when
  // `transcription.id` next changes (which is all it used to be keyed on,
  // leaving the player pointed at whatever server was effective when this
  // panel was first opened for this transcription).
  const settingsVersion = useServerSettingsVersion();

  useEffect(() => {
    let cancelled = false;
    setFormat("txt");
    setContent({});
    setContentError(undefined);
    setMediaLoc(undefined);
    audioLocation(transcription.id)
      .then((loc) => {
        if (!cancelled) setMediaLoc(loc);
      })
      .catch(() => {
        if (!cancelled) setMediaLoc({ exists: false });
      });
    return () => {
      cancelled = true;
    };
  }, [transcription.id]);

  useEffect(() => {
    let cancelled = false;
    getServerSettings()
      .then((settings) => {
        if (!cancelled) setServerBase(settings.baseUrl.replace(/\/$/, ""));
      })
      .catch(() => {
        if (!cancelled) setServerBase("");
      });
    return () => {
      cancelled = true;
    };
  }, [settingsVersion]);

  useEffect(() => {
    if (transcription.status !== "succeeded") return;
    if (content[format] !== undefined) return;
    let cancelled = false;
    setContentLoading(true);
    setContentError(undefined);
    fetchTranscript(transcription.id, format)
      .then((text) => {
        if (cancelled) return;
        setContent((prev) => ({ ...prev, [format]: text }));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setContentError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
      })
      .finally(() => {
        if (!cancelled) setContentLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transcription.id, transcription.status, format]);

  useEffect(() => {
    panelRef.current?.focus();
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
      if (isWithinOwnedWidget(document.activeElement)) return;
      e.preventDefault();
      onClose();
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transcription.id]);

  const title = transcription.audio.filename || transcription.id;
  const status = transcription.status === "succeeded" ? undefined : transcription.status;
  const errorCopy = transcription.status === "failed" ? errorCopyFor(transcription.error?.code) : undefined;
  const result = transcription.result;
  const segmentsAvailable = Boolean(result && result.segments.length > 0 && transcription.options.wordTimestamps);
  const audioSrc = `${serverBase}/api/v1/transcriptions/${transcription.id}/audio`;

  async function copyText(label: string, text: string) {
    try {
      await navigator.clipboard.writeText(text);
      toast.show({ message: `${label} copied` });
    } catch {
      toast.show({ message: `Couldn't copy the ${label.toLowerCase()}` });
    }
  }

  async function handleOpen() {
    setOpening(true);
    try {
      await openAudio(transcription.id);
      // OpenAudio downloads and caches the file first when it wasn't
      // already local (desktop/reveal.go's ensureLocalAudio) — a
      // successful call means it is now cached, so the button's own label
      // ("Open" vs "Download and open") stays accurate without a second
      // AudioLocation round trip.
      setMediaLoc((prev) => (prev ? { ...prev, exists: true } : prev));
    } catch (err) {
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: helios.code === "media_unavailable" ? "Opening a file isn't available from a browser tab." : helios.message });
    } finally {
      setOpening(false);
    }
  }

  async function handleReveal() {
    setRevealing(true);
    try {
      await revealInFileManager(transcription.id);
      setMediaLoc((prev) => (prev ? { ...prev, exists: true } : prev));
    } catch (err) {
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      toast.show({ message: helios.code === "media_unavailable" ? "Revealing a file isn't available from a browser tab." : helios.message });
    } finally {
      setRevealing(false);
    }
  }

  async function handleRetry() {
    setRetrying(true);
    try {
      await onRetry(transcription.id);
    } finally {
      setRetrying(false);
    }
  }

  function seekTo(seconds: number) {
    const el = audioRef.current;
    if (!el) return;
    el.currentTime = seconds;
    void el.play().catch(() => {
      // Autoplay can be blocked; the seek itself still took effect.
    });
  }

  return (
    <>
      {overlay ? <div className={styles.scrim} onClick={onClose} aria-hidden="true" /> : null}
      <div
        ref={panelRef}
        className={[styles.panel, overlay ? styles.overlay : ""].filter(Boolean).join(" ")}
        role="region"
        aria-label={`Details for ${title}`}
        tabIndex={-1}
      >
        <div className={styles.header}>
          <div className={styles.thumbWrap}>
            <AudioGlyph />
          </div>
          <IconButton className={styles.closeButton} size={28} icon={<CloseGlyph />} label="Close details" onClick={onClose} />
        </div>

        <div className={styles.body}>
          {status ? (
            <div className={styles.statusRow}>
              <StatusPill status={status} errorCode={transcription.error?.code} />
            </div>
          ) : null}
          <h2 className={styles.title} title={title}>
            {title}
          </h2>
          <p className={styles.subtitle}>
            {transcription.options.task === "translate" ? "Translate to English" : "Transcribe"}
            {" · "}
            {transcription.options.model || "default model"}
            {" · "}
            {transcription.options.language || "auto-detect"}
          </p>

          {errorCopy ? (
            <>
              <hr className={styles.hr} />
              <div className={styles.reasonBlock}>
                <p className={styles.reasonHeadline}>{errorCopy.headline}</p>
                {transcription.error?.message ? <p className={styles.reasonBody}>{transcription.error.message}</p> : null}
                <span className={["num", styles.reasonCode].join(" ")}>{transcription.error?.code ?? "unknown"}</span>
                <div className={styles.reasonActions}>
                  {errorCopy.retryable ? (
                    <Button variant="neutral" size="sm" loading={retrying} onClick={handleRetry}>
                      Try again
                    </Button>
                  ) : null}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => copyText("Details", `${transcription.error?.code ?? "unknown"}: ${transcription.error?.message ?? ""}`)}
                  >
                    Copy details
                  </Button>
                  <Button variant="danger" size="sm" onClick={() => onDelete(transcription.id)}>
                    Remove
                  </Button>
                </div>
              </div>
            </>
          ) : null}

          <hr className={styles.hr} />
          <dl className={styles.definitionList}>
            <dt>Media type</dt>
            <dd className="num">{transcription.audio.mediaType || "—"}</dd>
            <dt>Size</dt>
            <dd className="num">{formatBytes(transcription.audio.sizeBytes)}</dd>
            <dt>Duration</dt>
            <dd className="num">{formatDuration(transcription.audio.durationSeconds)}</dd>
            {transcription.audio.sampleRateHz ? (
              <>
                <dt>Sample rate</dt>
                <dd className="num">{transcription.audio.sampleRateHz} Hz</dd>
              </>
            ) : null}
            {transcription.audio.channels ? (
              <>
                <dt>Channels</dt>
                <dd className="num">{transcription.audio.channels}</dd>
              </>
            ) : null}
            {transcription.audio.codec ? (
              <>
                <dt>Codec</dt>
                <dd>{transcription.audio.codec}</dd>
              </>
            ) : null}
            <dt>Word timestamps</dt>
            <dd>{transcription.options.wordTimestamps ? "On" : "Off"}</dd>
            <dt>Submitted</dt>
            <dd title={formatRelative(transcription.createdAt)}>{formatAbsolute(transcription.createdAt)}</dd>
            {transcription.completedAt ? (
              <>
                <dt>Completed</dt>
                <dd title={formatRelative(transcription.completedAt)}>{formatAbsolute(transcription.completedAt)}</dd>
              </>
            ) : null}
          </dl>

          <hr className={styles.hr} />
          <h3 className={styles.sectionHeading}>Source audio</h3>
          <audio ref={audioRef} className={styles.audioPlayer} src={audioSrc} controls preload="none" />
          <MediaActions location={mediaLoc} opening={opening} revealing={revealing} onOpen={handleOpen} onReveal={handleReveal} />

          {transcription.status === "succeeded" && result ? (
            <>
              <hr className={styles.hr} />
              <div className={styles.transcriptHeader}>
                <h3 className={styles.sectionHeading}>Transcript</h3>
                <Segmented label="Format" value={format} onChange={(v) => setFormat(v as TranscriptFormat)} options={FORMAT_OPTIONS} />
              </div>
              <TranscriptContent
                loading={contentLoading}
                error={contentError}
                text={content[format]}
                onCopy={() => content[format] && copyText("Transcript", content[format]!)}
                onSaveAs={() => onSaveAs(transcription.id, format)}
              />

              {segmentsAvailable ? (
                <>
                  <hr className={styles.hr} />
                  <h3 className={styles.sectionHeading}>Segments</h3>
                  <ol className={styles.segmentList}>
                    {result.segments.map((seg) => (
                      <li key={seg.id} className={styles.segmentRow}>
                        <button
                          type="button"
                          className={["num", styles.segmentTime].join(" ")}
                          onClick={() => seekTo(seg.start)}
                        >
                          [{formatTimestamp(seg.start)}]
                        </button>
                        <span className={styles.segmentText}>{seg.text}</span>
                      </li>
                    ))}
                  </ol>
                </>
              ) : null}
            </>
          ) : null}

          {!errorCopy ? (
            <>
              <hr className={styles.hr} />
              <div className={styles.actions}>
                <Button variant="danger" size="sm" onClick={() => onDelete(transcription.id)}>
                  Delete…
                </Button>
              </div>
            </>
          ) : null}
        </div>
      </div>
    </>
  );
}

interface TranscriptContentProps {
  loading: boolean;
  error: HeliosError | undefined;
  text: string | undefined;
  onCopy: () => void;
  onSaveAs: () => void;
}

function TranscriptContent({ loading, error, text, onCopy, onSaveAs }: TranscriptContentProps) {
  if (error) {
    return <p className={styles.anomaly}>Couldn't load the transcript — {error.message}</p>;
  }
  if (loading || text === undefined) {
    return (
      <div className={styles.transcriptSkeleton}>
        <Skeleton width="95%" height={12} />
        <Skeleton width="88%" height={12} />
        <Skeleton width="70%" height={12} />
      </div>
    );
  }
  return (
    <>
      <pre className={[styles.transcriptText, "selectable"].join(" ")}>{text}</pre>
      <div className={styles.actions}>
        <Button variant="neutral" size="sm" onClick={onCopy}>
          Copy
        </Button>
        <Button variant="ghost" size="sm" onClick={onSaveAs}>
          Save as…
        </Button>
      </div>
    </>
  );
}

interface MediaActionsProps {
  location: MediaLocationInfo | undefined;
  opening: boolean;
  revealing: boolean;
  onOpen: () => void;
  onReveal: () => void;
}

/**
 * Open/Reveal are always offered (never gated on `AudioLocation.exists`):
 * `app.App.OpenAudio`/`RevealInFileManager` transparently download and
 * cache the source audio first if this process does not already hold a
 * local copy (see `desktop/reveal.go`'s own doc — unlike the sibling
 * ingestion project, this remote-only client never has direct filesystem
 * access to the server's storage at all). `location.exists` only changes
 * the label, so a first click that will trigger a download reads
 * differently from one that opens instantly. A rejected click (a browser
 * tab, which has no such capability at all — see `browser/api.ts`'s doc)
 * is handled by the caller's own catch, not here.
 */
function MediaActions({ location, opening, revealing, onOpen, onReveal }: MediaActionsProps) {
  if (location === undefined) {
    return (
      <div className={styles.actions}>
        <Skeleton width={72} height={32} />
        <Skeleton width={140} height={32} />
      </div>
    );
  }
  return (
    <div className={styles.actions}>
      <Button variant="neutral" size="sm" loading={opening} onClick={onOpen}>
        {location.exists ? "Open" : "Download and open"}
      </Button>
      <Button variant="ghost" size="sm" loading={revealing} onClick={onReveal}>
        {location.exists ? "Reveal in file manager" : "Download and reveal"}
      </Button>
    </div>
  );
}

function AudioGlyph() {
  return (
    <svg width="22" height="22" viewBox="0 0 22 22" fill="none" aria-hidden="true">
      <path d="M7 6h6l4 4v7a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1z" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
      <path d="M8.5 12.5v3M11 10.5v6.5M13.5 12.5v3" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </svg>
  );
}

function CloseGlyph() {
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
      <path d="M3 3l8 8M11 3l-8 8" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}
