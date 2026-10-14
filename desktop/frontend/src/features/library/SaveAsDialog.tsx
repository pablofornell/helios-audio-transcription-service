import { useEffect, useRef, useState } from "react";
import { Button, Dialog, Segmented, TextField, useToast } from "../../components";
import {
  chooseSaveDirectory,
  defaultSaveDirectory,
  HeliosError,
  saveTranscript,
  useServerSettingsVersion,
  type Transcription,
  type TranscriptFormat,
} from "../../lib";
import { transcriptDownloadUrl } from "../../lib/browser/api";
import { triggerBrowserDownloadFromClick, useBrowserDownloadBusy } from "../../lib/browser/download";
import { hasDownloadableTranscript } from "../../lib/contentEligibility";
import { getServerSettingsVersion } from "../../lib/serverSettingsSignal";
import { hasWailsRuntime } from "../../lib/runtime";
import styles from "./SaveAsDialog.module.css";

export interface SaveAsDialogProps {
  transcription: Transcription | undefined;
  /** The format currently showing in the detail panel's viewer — pre-selects the same format here, since "save what I'm looking at" is the common case. */
  initialFormat: TranscriptFormat;
  onClose: () => void;
}

const FORMAT_OPTIONS: { value: TranscriptFormat; label: string }[] = [
  { value: "txt", label: "Text" },
  { value: "srt", label: "SRT" },
  { value: "vtt", label: "VTT" },
  { value: "json", label: "JSON" },
];

export function SaveAsDialog({ transcription, initialFormat, onClose }: SaveAsDialogProps) {
  const toast = useToast();
  const inputRef = useRef<HTMLInputElement>(null);
  const browser = !hasWailsRuntime();
  const [format, setFormat] = useState<TranscriptFormat>(initialFormat);
  const [destDir, setDestDir] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const touchedRef = useRef(false);
  const settingsVersion = useServerSettingsVersion();
  const eligible = Boolean(transcription && hasDownloadableTranscript(transcription));
  const downloadUrl = transcription ? transcriptDownloadUrl(transcription.id, format) : "";
  const downloadBusy = useBrowserDownloadBusy(downloadUrl);
  const key = `${settingsVersion}:${transcription?.id ?? ""}:${eligible}:${initialFormat}`;
  const sessionRef = useRef({ key, active: true, saving: false, picker: 0 });
  if (sessionRef.current.key !== key) {
    sessionRef.current.active = false;
    sessionRef.current = { key, active: true, saving: false, picker: 0 };
  }
  const session = sessionRef.current;
  const current = () => sessionRef.current === session && session.active && settingsVersion === getServerSettingsVersion();
  function close() {
    session.active = false;
    onClose();
  }
  useEffect(() => {
    session.active = true;
    return () => { session.active = false; };
  }, [session]);

  useEffect(() => {
    setSaving(false);
    if (!transcription) return;
    setFormat(initialFormat);
    setDestDir("");
    setError(undefined);
    touchedRef.current = false;

    // A browser tab has no destination folder to pre-fill at all (see this
    // module's own doc) — skip the round trip entirely rather than call
    // `defaultSaveDirectory()` only to set unused state from its `""`.
    if (browser) return;

    let cancelled = false;
    defaultSaveDirectory()
      .then((dir) => {
        if (cancelled || !current() || touchedRef.current) return;
        setDestDir(dir);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session]);

  async function handleChooseFolder() {
    const picker = ++session.picker;
    touchedRef.current = true;
    try {
      const dir = await chooseSaveDirectory();
      if (!current() || picker !== session.picker) return;
      if (dir) {
        touchedRef.current = true;
        setDestDir(dir);
      }
    } catch (err) {
      if (!current() || picker !== session.picker) return;
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      // A browser tab has no native picker (`"media_unavailable"`) — not
      // worth an error toast, the text field below still works.
      if (helios.code !== "media_unavailable") setError(helios.message);
    }
  }

  function handleBrowserDownload() {
    if (!eligible || !current()) return;
    if (!triggerBrowserDownloadFromClick(downloadUrl)) return;
    toast.show({ message: `${format.toUpperCase()} download requested` });
    close();
  }

  async function handleSave() {
    if (!transcription || !eligible || !destDir.trim() || !current() || session.saving) return;
    session.saving = true;
    setSaving(true);
    setError(undefined);
    try {
      const path = await saveTranscript(transcription.id, format, destDir.trim());
      if (!current()) return;
      toast.show({ message: `Saved to ${path}` });
      close();
    } catch (err) {
      if (!current()) return;
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      setError(helios.message);
    } finally {
      session.saving = false;
      if (current()) setSaving(false);
    }
  }

  return (
    <Dialog
      open={transcription !== undefined}
      onClose={close}
      title="Save transcript as"
      initialFocusRef={browser ? undefined : inputRef}
      footer={
        <>
          <Button variant="neutral" onClick={close} data-dialog-safe-action="">
            Cancel
          </Button>
          {browser ? (
            <Button variant="primary" disabled={!eligible || downloadBusy} onClick={handleBrowserDownload}>
              {downloadBusy ? "Download requested…" : "Download"}
            </Button>
          ) : (
            <Button variant="primary" loading={saving} disabled={!eligible || !destDir.trim()} onClick={handleSave}>
              Save
            </Button>
          )}
        </>
      }
    >
      {browser ? (
        <>
          <p className={styles.intro}>Choose a format to download.</p>
          <Segmented label="Format" value={format} onChange={(v) => setFormat(v as TranscriptFormat)} options={FORMAT_OPTIONS} />
        </>
      ) : (
        <>
          <p className={styles.intro}>Choose an existing folder on this machine to write the transcript into.</p>
          <Segmented label="Format" value={format} onChange={(v) => setFormat(v as TranscriptFormat)} options={FORMAT_OPTIONS} className={styles.intro} />
          <div className={styles.row}>
            <TextField
              ref={inputRef}
              variant="plain"
              label="Destination folder"
              placeholder="/home/you/Downloads"
              value={destDir}
              onChange={(e) => {
                touchedRef.current = true;
                setDestDir(e.target.value);
              }}
              invalid={Boolean(error)}
              errorMessage={error}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  void handleSave();
                }
              }}
            />
            <Button variant="neutral" size="sm" onClick={handleChooseFolder}>
              Choose folder…
            </Button>
          </div>
        </>
      )}
    </Dialog>
  );
}
