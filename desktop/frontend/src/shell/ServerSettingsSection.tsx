import { useEffect, useRef, useState } from "react";
import { Button, TextField, useToast } from "../components";
import {
  connectionMonitor,
  getServerSettings,
  HeliosError,
  setServerSettings,
  testServerConnection,
  transcriptionStore,
  type ServerSettings,
} from "../lib";
import settingsStyles from "./SettingsDialog.module.css";
import styles from "./ServerSettingsSection.module.css";

export interface ServerSettingsSectionProps {
  /** Called with the settings actually applied, right after a successful save — see SettingsDialog's own doc for why App needs this. */
  onSaved?: (settings: ServerSettings) => void;
}

/** Local, string-typed mirror of a freshly-fetched ServerSettings' two editable fields — kept as plain strings so the Port field can hold "", a half-typed number, etc., without fighting `<input>`. */
interface DraftFields {
  host: string;
  port: string;
}

function draftFrom(settings: ServerSettings): DraftFields {
  return { host: settings.host, port: settings.port ? String(settings.port) : "" };
}

/** The three derived values every validity/dirty check below is built from — pulled into one function so handleTest's staleness check (see its own doc) can recompute them from a ref instead of duplicating the logic inline. */
function parseDraft(d: DraftFields): { trimmedHost: string; portNumber: number; portIsValid: boolean } {
  const trimmedHost = d.host.trim();
  const trimmedPort = d.port.trim();
  const portNumber = Number(trimmedPort);
  const portIsValid = trimmedPort !== "" && Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  return { trimmedHost, portNumber, portIsValid };
}

/**
 * True when currentDraft's Host/Port no longer match testedHost/testedPort
 * — i.e. the user edited a field while a Test connection request for the
 * *previous* value was still in flight. `handleTest` uses this to drop
 * that response on the floor rather than displaying a result that no
 * longer describes what is in the fields (a stale "Reachable" sitting
 * under a Host the user has since changed to something else entirely).
 */
function isStaleTestResult(currentDraft: DraftFields, testedHost: string, testedPort: number): boolean {
  const current = parseDraft(currentDraft);
  return current.trimmedHost !== testedHost || current.portNumber !== testedPort;
}

/**
 * Server — DESIGN.md §6.11's Settings dialog, Server section: the backend
 * host and port this process talks to in "remote" mode (`desktop/mode.go`'s
 * `HELIOS_MODE`), applied live on save with no restart (see
 * `desktop/serversettings.go`'s `SetServerSettings`).
 *
 * `getServerSettings().editable` is false only in "local" mode
 * (`desktop/mode.go`'s `HELIOS_MODE`), which has no separate server to
 * point anywhere — this section shows one explanation and disables its
 * fields in that case. A plain browser tab is always editable
 * (`lib/browser/api.ts`'s own doc): it defaults to same-origin, but
 * Settings → Server can point it at a different `helios-transcription-api`
 * entirely, persisted per browser in `localStorage` (see
 * `lib/browser/serverConfig.ts`).
 *
 * There is no separate scheme control: typing a whole URL into Host (e.g.
 * `"https://example.com"`) is how the effective scheme actually changes —
 * `setServerSettings` honours that prefix rather than silently discarding
 * it. Since that means what gets saved is not always literally "the Host
 * field's text", a read-only line below the fields always shows the real
 * effective URL (`settings.baseUrl`, updated the moment a save succeeds) so
 * the user never has to guess what actually took effect.
 */
export function ServerSettingsSection({ onSaved }: ServerSettingsSectionProps) {
  const toast = useToast();
  const [settings, setSettings] = useState<ServerSettings | undefined>(undefined);
  const [draft, setDraft] = useState<DraftFields>({ host: "", port: "" });
  // Whether the user has edited a field since the last load/save — gates
  // the inline Host/Port error messages below, so one never appears on
  // the section's very first render, before there is anything to be wrong
  // about (TextField's own doc: a field must not turn red while nobody
  // has touched it yet). touchedRef mirrors this for the load effect
  // below, which needs a value that does not go stale inside a closure
  // that only runs once — see its own doc.
  const [touched, setTouched] = useState(false);
  const touchedRef = useRef(false);
  const [saveError, setSaveError] = useState<string | undefined>(undefined);
  const [saving, setSaving] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; detail: string } | undefined>(undefined);
  const [testing, setTesting] = useState(false);
  // Always the latest `draft`, for handleTest's staleness check — a plain
  // closure over `draft` would instead capture whatever it was at the
  // moment Test connection was clicked, which is exactly the value that
  // check needs to compare *against*, not read from.
  const draftRef = useRef(draft);
  draftRef.current = draft;

  useEffect(() => {
    let cancelled = false;
    getServerSettings()
      .then((s) => {
        if (cancelled) return;
        setSettings(s);
        // Never clobber an edit the user already started typing while
        // this (network-free, but still async) call was in flight — a
        // narrow but real race, not a hypothetical one.
        if (!touchedRef.current) setDraft(draftFrom(s));
      })
      .catch(() => {
        // Reading the current settings failed (should not happen — see
        // GetServerSettings' own doc, it never rejects) — leave the
        // section in its "still loading" state (settings undefined),
        // which disables the fields exactly as a genuinely non-editable
        // mode would, rather than showing stale/blank values as if they
        // were real.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const editable = settings?.editable ?? false;
  const { trimmedHost, portNumber, portIsValid } = parseDraft(draft);
  const trimmedPort = draft.port.trim();
  // A saved value desktop.json could not use also counts as "dirty", even
  // though the fields still just show the prefilled fallback unchanged:
  // otherwise Save stays disabled and there is no way to actually save
  // that fallback back over the invalid file, so the warning (and the
  // invalid file underneath it) can never clear.
  const dirty =
    settings !== undefined &&
    (trimmedHost !== settings.host || trimmedPort !== String(settings.port) || Boolean(settings.warning));
  const canSave = editable && dirty && trimmedHost !== "" && portIsValid && !saving;
  const canTest = editable && trimmedHost !== "" && portIsValid && !testing;

  const hostError = touched && trimmedHost === "" ? "Host is required." : undefined;
  const portError = touched && trimmedPort !== "" && !portIsValid ? "Port must be a number between 1 and 65535." : undefined;

  function markTouched() {
    touchedRef.current = true;
    setTouched(true);
  }

  function updateHost(value: string) {
    markTouched();
    setDraft((d) => ({ ...d, host: value }));
    setSaveError(undefined);
    setTestResult(undefined);
  }

  function updatePort(value: string) {
    markTouched();
    setDraft((d) => ({ ...d, port: value }));
    setSaveError(undefined);
    setTestResult(undefined);
  }

  async function handleSave() {
    // Save is disabled unless canSave is already true (below) — this
    // guard is only ever hit defensively, e.g. a stray Enter keypress
    // this section does not itself wire up.
    if (!canSave) return;

    setSaveError(undefined);
    setSaving(true);
    try {
      const updated = await setServerSettings(trimmedHost, portNumber);
      setSettings(updated);
      setDraft(draftFrom(updated));
      setTouched(false);
      touchedRef.current = false;
      setTestResult(undefined);
      toast.show({ message: "Settings saved" });
      // Re-check now rather than waiting for the next steady-state poll
      // (up to lib/connection.ts's STEADY_POLL_MS away) — the point of an
      // immediate apply is not making the user wonder whether it worked.
      connectionMonitor.retryNow();
      // Same reasoning for the Library/Transcribe queue: without this,
      // rows from the old server would sit in lib/store.ts's cache until
      // its next poll tick happened to land against the new one.
      transcriptionStore.resetForServerChange();
      onSaved?.(updated);
    } catch (err) {
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      setSaveError(helios.message);
    } finally {
      setSaving(false);
    }
  }

  async function handleTest() {
    if (!canTest) return;

    // Snapshot exactly what is being tested, so the response (whichever
    // way it goes) can be checked against what the fields say *now* —
    // see isStaleTestResult's doc.
    const testedHost = trimmedHost;
    const testedPort = portNumber;

    setTestResult(undefined);
    setTesting(true);
    try {
      const report = await testServerConnection(testedHost, testedPort);
      if (isStaleTestResult(draftRef.current, testedHost, testedPort)) return;
      const detail = report.checks.connection?.detail;
      setTestResult({ ok: report.ok, detail: report.ok ? "Reachable" : detail || "Unreachable" });
    } catch (err) {
      if (isStaleTestResult(draftRef.current, testedHost, testedPort)) return;
      const helios = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      setTestResult({ ok: false, detail: helios.message });
    } finally {
      setTesting(false);
    }
  }

  return (
    <section className={settingsStyles.section}>
      <h3 className={settingsStyles.sectionTitle}>Server</h3>
      {settings?.warning ? <p className={styles.warning}>{settings.warning}</p> : null}
      {!editable ? (
        <p className={styles.hint}>Not editable here — this build runs its backend in-process (local mode).</p>
      ) : null}
      <div className={styles.row}>
        <div className={styles.hostField}>
          <TextField
            variant="plain"
            label="Host"
            placeholder="localhost"
            value={draft.host}
            disabled={!editable || settings === undefined}
            onChange={(e) => updateHost(e.target.value)}
            invalid={Boolean(hostError)}
            errorMessage={hostError}
          />
        </div>
        <div className={styles.portField}>
          <TextField
            variant="plain"
            label="Port"
            placeholder="7462"
            inputMode="numeric"
            value={draft.port}
            disabled={!editable || settings === undefined}
            onChange={(e) => updatePort(e.target.value)}
            invalid={Boolean(portError)}
            errorMessage={portError}
          />
        </div>
      </div>
      {editable && settings ? (
        <p className={styles.effectiveURL}>
          Currently connected to <span className={["num", styles.effectiveURLValue].join(" ")}>{settings.baseUrl}</span>
        </p>
      ) : null}
      <div className={styles.actions}>
        <Button variant="neutral" size="sm" loading={testing} disabled={!canTest} onClick={handleTest}>
          Test connection
        </Button>
        <Button variant="primary" size="sm" loading={saving} disabled={!canSave} onClick={handleSave}>
          Save
        </Button>
        {testResult ? (
          <span className={testResult.ok ? styles.testOK : styles.testFailed}>{testResult.detail}</span>
        ) : null}
      </div>
      {saveError ? <p className={styles.saveError}>{saveError}</p> : null}
    </section>
  );
}
