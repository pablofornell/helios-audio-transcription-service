import { useEffect, useRef, useState } from "react";
import { Button, Dialog, Segmented, TextField, useToast } from "../../components";
import {
  chooseSaveDirectory,
  defaultSaveDirectory,
  HeliosError,
  saveTranscript,
  type Transcription,
  type TranscriptFormat,
} from "../../lib";
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

/**
 * Collects a destination folder and writes the rendered transcript there —
 * `SaveTranscript` (see `app.App.SaveTranscript`) writes
 * `<destDir>/<name>.<format>` and returns the path actually written.
 * `destDir` must already exist; this dialog does not create it. It
 * pre-fills the field with `defaultSaveDirectory`'s answer (the user's
 * system downloads folder) whenever one resolves, and offers a native
 * folder picker (`chooseSaveDirectory`) alongside typing it directly.
 */
export function SaveAsDialog({ transcription, initialFormat, onClose }: SaveAsDialogProps) {
  const toast = useToast();
  const inputRef = useRef<HTMLInputElement>(null);
  const [format, setFormat] = useState<TranscriptFormat>(initialFormat);
  const [destDir, setDestDir] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const touchedRef = useRef(false);

  useEffect(() => {
    if (!transcription) return;
    setFormat(initialFormat);
    setDestDir("");
    setError(undefined);
    touchedRef.current = false;

    let cancelled = false;
    defaultSaveDirectory()
      .then((dir) => {
        if (cancelled || touchedRef.current) return;
        setDestDir(dir);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transcription?.id]);

  async function handleChooseFolder() {
    try {
      const dir = await chooseSaveDirectory();
      if (dir) {
        touchedRef.current = true;
        setDestDir(dir);
      }
    } catch (err) {
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      // A browser tab has no native picker (`"media_unavailable"`) — not
      // worth an error toast, the text field below still works.
      if (helios.code !== "media_unavailable") setError(helios.message);
    }
  }

  async function handleSave() {
    if (!transcription || !destDir.trim()) return;
    setSaving(true);
    setError(undefined);
    try {
      const path = await saveTranscript(transcription.id, format, destDir.trim());
      toast.show({ message: `Saved to ${path}` });
      onClose();
    } catch (err) {
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      setError(helios.message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open={transcription !== undefined}
      onClose={onClose}
      title="Save transcript as"
      initialFocusRef={inputRef}
      footer={
        <>
          <Button variant="neutral" onClick={onClose} data-dialog-safe-action="">
            Cancel
          </Button>
          <Button variant="primary" loading={saving} disabled={!destDir.trim()} onClick={handleSave}>
            Save
          </Button>
        </>
      }
    >
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
    </Dialog>
  );
}
