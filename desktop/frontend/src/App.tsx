import { useEffect, useState } from "react";
import styles from "./App.module.css";
import { ToastViewport } from "./components";
import { getServerSettings, useConnection, useTheme } from "./lib";
import { TranscribeScreen } from "./features/transcribe";
import { LibraryScreen } from "./features/library";
import { ColdStart } from "./shell/ColdStart";
import { ConnectionBar } from "./shell/ConnectionBar";
import { Sidebar, type ScreenId } from "./shell/Sidebar";
import { SettingsDialog } from "./shell/SettingsDialog";
import { TitleBar } from "./shell/TitleBar";

const COLLAPSE_WIDTH = 1040;

/**
 * App — the shell: window chrome, theme, sidebar, connection handling, and
 * the two screen mount points. The screens own everything inside them; the
 * shell owns only what crosses between them, which is exactly one thing:
 * following "View in Library" from a queue row.
 */
function App() {
  // Establishes the persisted/first-run theme on the document element —
  // see lib/theme.ts's doc on why the default is "dark", not "system".
  useTheme();

  const connection = useConnection();

  // A one-shot hand-off, not shared state: a queue row asks the Library to
  // open a specific transcription, and the Library clears the intent by
  // simply not reading it again. Keeping it here rather than in a store
  // means the two screens never need to know about each other.
  const [pendingLibraryId, setPendingLibraryId] = useState<string | undefined>(undefined);
  const [screen, setScreen] = useState<ScreenId>("transcribe");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(() => window.innerWidth < COLLAPSE_WIDTH);
  // What ColdStart tells the user it tried — the real, currently configured
  // server URL, via GetServerSettings (desktop/serversettings.go), not a
  // hardcoded guess. Empty until that first resolves and again whenever
  // the Server settings section (SettingsDialog) saves a new one.
  const [apiURL, setApiURL] = useState("");

  useEffect(() => {
    let cancelled = false;
    getServerSettings()
      .then((settings) => {
        if (!cancelled && settings.baseUrl) setApiURL(settings.baseUrl);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    function onResize() {
      setCollapsed(window.innerWidth < COLLAPSE_WIDTH);
    }
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod) return;
      // Ctrl/Cmd+R must never reach the WebView's own reload accelerator —
      // that would look like a crash, not a shortcut.
      if (e.key.toLowerCase() === "r") {
        e.preventDefault();
        return;
      }
      if (e.key === "1") {
        e.preventDefault();
        setScreen("transcribe");
      } else if (e.key === "2") {
        e.preventDefault();
        setScreen("library");
      } else if (e.key === ",") {
        e.preventDefault();
        setSettingsOpen(true);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const coldStart = connection.status === "offline";

  return (
    <div className={styles.shell}>
      <TitleBar onOpenSettings={() => setSettingsOpen(true)} />
      {coldStart ? (
        <ColdStart connection={connection} apiURL={apiURL} />
      ) : (
        <div className={styles.body}>
          <Sidebar screen={screen} onNavigate={setScreen} collapsed={collapsed} health={connection.report} />
          <div className={styles.content}>
            <ConnectionBar connection={connection} />
            <div className={styles.screen}>
              {screen === "transcribe" ? (
                <TranscribeScreen
                  onOpenLibrary={(id) => {
                    setPendingLibraryId(id);
                    setScreen("library");
                  }}
                />
              ) : (
                <LibraryScreen openId={pendingLibraryId} />
              )}
            </div>
          </div>
        </div>
      )}
      <SettingsDialog open={settingsOpen} onClose={() => setSettingsOpen(false)} onServerSettingsSaved={(settings) => setApiURL(settings.baseUrl)} />
      <ToastViewport />
    </div>
  );
}

export default App;
