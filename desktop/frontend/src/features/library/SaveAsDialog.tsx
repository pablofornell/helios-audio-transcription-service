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
import { transcriptDownloadUrl } from "../../lib/browser/api";
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

/**
 * Desktop (`hasWailsRuntime()`): collects a destination folder and writes
 * the rendered transcript there — `SaveTranscript` (see
 * `app.App.SaveTranscript`) writes `<destDir>/<name>.<format>` and returns
 * the path actually written. `destDir` must already exist; this dialog
 * does not create it. It pre-fills the field with `defaultSaveDirectory`'s
 * answer (the user's system downloads folder) whenever one resolves, and
 * offers a native folder picker (`chooseSaveDirectory`) alongside typing
 * it directly.
 *
 * Browser tab: there is no local folder to write into at all (see
 * `../../lib/browser/api.ts`'s `saveTranscript`, which always rejects), so
 * this instead offers only the format choice and hands off to the
 * browser's own download manager — `transcriptDownloadUrl` plus a
 * temporary `<a download>` click (`triggerBrowserDownload` below), not
 * `fetch` + a blob URL, so this never buffers the whole transcript in
 * memory and works against a cross-origin saved server with no CORS
 * configuration needed for it (an ordinary cross-origin navigation, unlike
 * a `fetch`).
 */
export function SaveAsDialog({ transcription, initialFormat, onClose }: SaveAsDialogProps) {
  const toast = useToast();
  const inputRef = useRef<HTMLInputElement>(null);
  const browser = !hasWailsRuntime();
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

    // A browser tab has no destination folder to pre-fill at all (see this
    // module's own doc) — skip the round trip entirely rather than call
    // `defaultSaveDirectory()` only to set unused state from its `""`.
    if (browser) return;

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

  function handleBrowserDownload() {
    if (!transcription) return;
    triggerBrowserDownload(transcriptDownloadUrl(transcription.id, format));
    // "requested", not "started"/"saved" — this only fired off a
    // navigation; whether it actually became a download (as opposed to a
    // 404/error page in the throwaway tab `target="_blank"` sends a
    // failure to — see `triggerBrowserDownload`'s own doc) is something
    // this dialog has no way to observe at all.
    toast.show({ message: `${format.toUpperCase()} download requested` });
    onClose();
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
      initialFocusRef={browser ? undefined : inputRef}
      footer={
        <>
          <Button variant="neutral" onClick={onClose} data-dialog-safe-action="">
            Cancel
          </Button>
          {browser ? (
            <Button variant="primary" onClick={handleBrowserDownload}>
              Download
            </Button>
          ) : (
            <Button variant="primary" loading={saving} disabled={!destDir.trim()} onClick={handleSave}>
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

/**
 * Navigates a temporary, invisible `<a download>` at `url` and removes it
 * immediately — the browser's own download manager takes it from there.
 * Deliberately not `fetch` + a `Blob` URL: that would buffer the entire
 * transcript in memory before it could start writing to disk, and would
 * need CORS to succeed at all against a saved, cross-origin server
 * (`../../lib/browser/serverConfig.ts`'s `apiBase()`) — a plain navigation,
 * same as clicking a link, needs neither.
 *
 * `target="_blank"` matters specifically for that saved, cross-origin case:
 * a browser ignores the `download` attribute across origins, so a
 * same-tab navigation whose response is NOT actually an attachment (the
 * transcript is gone by the time this lands, the id no longer exists, the
 * saved server is unreachable — a 404/error page or raw JSON instead of
 * `Content-Disposition: attachment`) would otherwise replace this whole
 * SPA tab and lose all its state. With `target="_blank"`, a genuine
 * attachment download still triggers with no lingering tab left behind
 * (modern browsers close a `_blank` target that immediately turned into a
 * download without ever rendering anything in it); a failure instead opens
 * and is stranded in a throwaway tab, which costs nothing here.
 * `rel="noopener"` keeps that new tab from getting a `window.opener` back
 * to this one, same as any other `target="_blank"` link.
 */
function triggerBrowserDownload(url: string): void {
  const a = document.createElement("a");
  a.href = url;
  a.download = "";
  a.target = "_blank";
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
