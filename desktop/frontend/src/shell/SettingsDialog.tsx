import { useRef } from "react";
import { Button, Dialog, Segmented } from "../components";
import { useTheme, type ServerSettings } from "../lib";
import { ServerSettingsSection } from "./ServerSettingsSection";
import styles from "./SettingsDialog.module.css";

export interface SettingsDialogProps {
  open: boolean;
  onClose: () => void;
  /** Forwarded to `ServerSettingsSection` — see its own doc. */
  onServerSettingsSaved?: (settings: ServerSettings) => void;
}

const SHORTCUTS: Array<[string, string]> = [
  ["⌘/Ctrl + O", "Choose an audio file to transcribe — from anywhere"],
  ["⌘/Ctrl + Enter", "Submit (from anywhere in the composer)"],
  ["⌘/Ctrl + 1 / 2", "Transcribe / Library"],
  ["⌘/Ctrl + F", "Focus Library search"],
  ["↑ ↓ or J K", "Move row selection"],
  ["Enter", "Open detail"],
  ["⌘/Ctrl + R", "Retry the selected failed item"],
  ["Delete / Backspace", "Delete selected (confirm dialog)"],
  ["Esc", "Close detail → close menu → clear search → blur"],
  ["⌘/Ctrl + ,", "Settings"],
];

/**
 * SettingsDialog — one of the app's exactly two Dialogs (DESIGN.md §6.11).
 * Theme picker (System / Light / Dark — the explicit toggle DESIGN.md §2.2
 * requires alongside the OS preference), the Server section (the backend
 * host/port — see `ServerSettingsSection`'s own doc), and the keyboard
 * shortcut reference DESIGN.md §8 requires living here.
 */
export function SettingsDialog({ open, onClose, onServerSettingsSaved }: SettingsDialogProps) {
  const [preference, setPreference] = useTheme();
  const closeButtonRef = useRef<HTMLButtonElement>(null);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Settings"
      size="lg"
      initialFocusRef={closeButtonRef}
      footer={
        <Button ref={closeButtonRef} variant="primary" onClick={onClose} data-dialog-safe-action="">
          Done
        </Button>
      }
    >
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>Theme</h3>
        <Segmented
          label="Theme"
          value={preference}
          onChange={(v) => setPreference(v as typeof preference)}
          options={[
            { value: "system", label: "System" },
            { value: "light", label: "Light" },
            { value: "dark", label: "Dark" },
          ]}
        />
      </section>
      <ServerSettingsSection onSaved={onServerSettingsSaved} />
      <section className={styles.section}>
        <h3 className={styles.sectionTitle}>Keyboard</h3>
        <table className={styles.table}>
          <tbody>
            {SHORTCUTS.map(([key, action]) => (
              <tr key={key}>
                <td className={[styles.key, "num"].join(" ")}>{key}</td>
                <td className={styles.action}>{action}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </Dialog>
  );
}
