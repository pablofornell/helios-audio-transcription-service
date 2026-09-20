import { useRef } from "react";
import { Button, Dialog } from "../../components";
import type { Transcription } from "../../lib";
import styles from "./DeleteConfirmDialog.module.css";

export interface DeleteConfirmDialogProps {
  transcription: Transcription | undefined;
  busy: boolean;
  onCancel: () => void;
  onConfirm: (id: string) => void;
}

/**
 * The one dialog every destructive Library action funnels through —
 * deleting removes the job's row and its stored files (source audio and
 * transcript) and cannot be undone, so it needs a real confirm naming the
 * item, not a toast you can miss. Deleting a still-active item gets one
 * extra line, since `app.App.Delete` deletes unconditionally without first
 * canceling it (see docs/openapi.yaml's `deleteTranscription` doc).
 */
export function DeleteConfirmDialog({ transcription, busy, onCancel, onConfirm }: DeleteConfirmDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const name = transcription?.audio.filename || transcription?.id || "";
  const active = transcription?.status === "pending" || transcription?.status === "running";

  return (
    <Dialog
      open={transcription !== undefined}
      onClose={onCancel}
      title="Delete this transcription?"
      initialFocusRef={cancelRef}
      footer={
        <>
          <Button ref={cancelRef} variant="neutral" onClick={onCancel} data-dialog-safe-action="">
            Cancel
          </Button>
          <Button variant="danger" loading={busy} onClick={() => transcription && onConfirm(transcription.id)}>
            Delete
          </Button>
        </>
      }
    >
      <p className={styles.message}>
        Delete "{name}" and its transcript? This can't be undone.
        {active ? " It's still in progress — it will keep running server-side; only this record is removed." : ""}
      </p>
    </Dialog>
  );
}
