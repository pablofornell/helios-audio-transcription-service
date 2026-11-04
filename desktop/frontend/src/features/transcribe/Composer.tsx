import { useEffect, useRef, useState, type DragEvent } from "react";
import { EmptyState, Select, Switch } from "../../components";
import { chooseAudioFile, getServerSettingsVersion, useServerSettingsVersion, type AudioSource, type Options } from "../../lib";
import { hasWailsRuntime } from "../../lib/runtime";
import { SUPPORTED_MODEL, useComposerOptions } from "./useComposerOptions";
import type { SubmitOutcome } from "./useTranscribeQueue";
import styles from "./Composer.module.css";

export interface ComposerProps {
  /** Full-surface first-run composer vs the docked bar once a row exists. */
  variant: "expanded" | "collapsed";
  submit: (source: AudioSource, options: Options) => Promise<SubmitOutcome>;
}

// Empty Model uses the configured default; explicit selection uses the same
// supported quantized turbo model.
const MODEL_OPTIONS = [
  { value: "", label: "Server default" },
  { value: SUPPORTED_MODEL, label: SUPPORTED_MODEL },
];

// Optional hint for the language spoken in the audio. A hint skips language
// detection; it does not choose the output language: the service always
// transcribes Spanish audio in Spanish and gives English for all other audio.
// ISO-639-1/639-2 codes domain.Options.Validate accepts (2-3 lowercase
// letters) — a representative set of whisper.cpp's supported languages,
// not an exhaustive one. "" is "Auto-detect" (domain.Options.AutoDetectLanguage).
const LANGUAGE_OPTIONS = [
  { value: "", label: "Auto-detect" },
  { value: "en", label: "English" },
  { value: "es", label: "Spanish" },
  { value: "fr", label: "French" },
  { value: "de", label: "German" },
  { value: "it", label: "Italian" },
  { value: "pt", label: "Portuguese" },
  { value: "nl", label: "Dutch" },
  { value: "ru", label: "Russian" },
  { value: "zh", label: "Chinese" },
  { value: "ja", label: "Japanese" },
  { value: "ko", label: "Korean" },
  { value: "ar", label: "Arabic" },
  { value: "hi", label: "Hindi" },
  { value: "tr", label: "Turkish" },
  { value: "pl", label: "Polish" },
  { value: "sv", label: "Swedish" },
  { value: "uk", label: "Ukrainian" },
  { value: "vi", label: "Vietnamese" },
];

/** Base name of a filesystem path, tolerant of both `/` and `\` separators (a dropped OS path can carry either, depending on platform). */
function basename(path: string): string {
  const parts = path.split(/[/\\]/);
  return parts[parts.length - 1] || path;
}

/**
 * The composer — a drop zone + file picker, plus the real transcription
 * options (model, audio language hint, word timestamps). One component, two
 * layouts: `expanded` (first run, fills the surface) and `collapsed` (a
 * docked bar once a row exists) share every field, handler and piece of
 * state below; only the surrounding chrome differs, so the two can never
 * drift into describing different behaviour — mirrors the sibling
 * project's Composer, adapted from a URL field to a file drop.
 */
export function Composer({ variant, submit }: ComposerProps) {
  const { options, setOptions, isDefault, reset } = useComposerOptions();
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | undefined>(undefined);
  const [dragOver, setDragOver] = useState(false);

  const serverVersion = useServerSettingsVersion();
  const mountedRef = useRef(false);
  const submittingRef = useRef(false);
  const choosingRef = useRef(false);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useEffect(() => {
    submittingRef.current = false;
    setSubmitting(false);
    setSubmitError(undefined);
  }, [serverVersion]);

  const fileInputRef = useRef<HTMLInputElement>(null);

  async function doSubmit(source: AudioSource) {
    if (!mountedRef.current || submittingRef.current) return;
    const version = getServerSettingsVersion();
    const isCurrent = () => mountedRef.current && version === getServerSettingsVersion();
    submittingRef.current = true;
    setSubmitError(undefined);
    setSubmitting(true);
    try {
      const outcome = await submit(source, options);
      if (isCurrent() && !outcome.ok) setSubmitError(outcome.error.message);
    } finally {
      if (isCurrent()) {
        submittingRef.current = false;
        setSubmitting(false);
      }
    }
  }

  /**
   * The primary "choose a file" action. In the Wails shell this opens the
   * native OS file dialog (`ChooseAudioFile()`), which hands back a real
   * absolute path — exactly what the bound `Submit` needs. A plain browser
   * tab has no such dialog (see `browser/api.ts`'s doc); there, this opens
   * an ordinary `<input type="file">`, whose chosen `File`'s bytes are
   * uploaded directly.
   */
  async function pickFile() {
    if (hasWailsRuntime()) {
      if (!mountedRef.current || choosingRef.current) return;
      const version = getServerSettingsVersion();
      const isCurrent = () => mountedRef.current && version === getServerSettingsVersion();
      choosingRef.current = true;
      try {
        const path = await chooseAudioFile();
        if (isCurrent() && path) void doSubmit({ kind: "path", path, name: basename(path) });
      } catch (err) {
        if (isCurrent()) setSubmitError(err instanceof Error ? err.message : "Couldn't open the file dialog");
      } finally {
        choosingRef.current = false;
      }
      return;
    }
    fileInputRef.current?.click();
  }

  function onFileInputChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = ""; // let the same file be picked again later
    if (file) void doSubmit({ kind: "file", file });
  }

  /**
   * Drag-and-drop. The Wails webview's `<input type="file">`/HTML5 drop
   * events are real OS-backed files, and Wails is known to leave the
   * dropped `File`'s non-standard `.path` populated with the real
   * filesystem path (unlike an ordinary browser, which never exposes one)
   * — checked defensively rather than assumed, so a plain browser tab (no
   * `.path`, ever) falls through to the same `{ kind: "file" }` upload
   * path a file-input pick takes there.
   */
  function onDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(false);
    const file = e.dataTransfer.files?.[0];
    if (!file) return;
    const path = (file as unknown as { path?: string }).path;
    if (hasWailsRuntime()) {
      if (path) {
        void doSubmit({ kind: "path", path, name: basename(path) });
      } else {
        setSubmitError("Couldn't read this file's path — use “Choose a file” instead.");
      }
      return;
    }
    void doSubmit({ kind: "file", file });
  }

  function onDragOver(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragOver(true);
  }

  function onDragLeave() {
    setDragOver(false);
  }

  const dropZone = (
    <div
      className={[styles.dropZone, dragOver ? styles.dropZoneActive : "", submitting ? styles.dropZoneBusy : ""].filter(Boolean).join(" ")}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      role="button"
      tabIndex={0}
      aria-label="Drop an audio file here, or choose a file"
      aria-busy={submitting || undefined}
      onClick={() => void pickFile()}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          void pickFile();
        }
      }}
    >
      <DropGlyph />
      <p className={styles.dropText}>
        {submitting ? "Uploading…" : (
          <>
            Drop audio here, or{" "}
            <span className={styles.dropLink}>choose a file</span>
          </>
        )}
      </p>
      {submitError ? <p className={styles.dropError}>{submitError}</p> : null}
      <input
        ref={fileInputRef}
        type="file"
        accept="audio/*,video/*"
        className={styles.hiddenInput}
        onChange={onFileInputChange}
        tabIndex={-1}
      />
    </div>
  );

  const optionsRow = (
    <div className={styles.optionsRow}>
      <div className={styles.optionGroup}>
        <span className={styles.optionLabel}>Model</span>
        <Select label="Model" options={MODEL_OPTIONS} value={options.model} onChange={(v) => setOptions({ ...options, model: v })} />
      </div>
      <div className={styles.optionGroup}>
        <span className={styles.optionLabel}>Audio language</span>
        <Select label="Audio language" options={LANGUAGE_OPTIONS} value={options.language} onChange={(v) => setOptions({ ...options, language: v })} />
      </div>
      <Switch
        label="Word timestamps"
        checked={options.wordTimestamps}
        onChange={(e) => setOptions({ ...options, wordTimestamps: e.target.checked })}
      />
      {!isDefault ? (
        <button type="button" className={styles.resetLink} onClick={reset}>
          Reset to defaults
        </button>
      ) : null}
    </div>
  );

  if (variant === "expanded") {
    return (
      <div className={styles.expandedRoot}>
        <EmptyState
          variant="surface"
          headline="Nothing here yet."
          headlineSize="xl"
          body={<p>Drop an audio or video file and HELIOS-ATS will transcribe it with whisper.cpp.</p>}
        >
          <div className={styles.expandedForm}>
            {dropZone}
            {optionsRow}
          </div>
        </EmptyState>
      </div>
    );
  }

  return (
    <div className={styles.collapsedRoot}>
      {dropZone}
      {optionsRow}
    </div>
  );
}

function DropGlyph() {
  return (
    <svg width="28" height="28" viewBox="0 0 28 28" fill="none" aria-hidden="true">
      <path d="M14 4v13m0 0l-4.5-4.5M14 17l4.5-4.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
      <path d="M5 18.5V22a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-3.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}
