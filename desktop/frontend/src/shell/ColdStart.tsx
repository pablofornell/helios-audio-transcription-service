import { useState } from "react";
import { Button, EmptyState } from "../components";
import { connectionMonitor, type ConnectionState } from "../lib";
import styles from "./ColdStart.module.css";

export interface ColdStartProps {
  connection: ConnectionState;
  apiURL: string;
}

/**
 * ColdStart — DESIGN.md §5.6. The full-surface "can't reach the backend at
 * all" state, shown only while `connection.status === "offline"` (Health()
 * has never once succeeded). Names the URL it actually tried, says whose
 * fault it is, and hands over the exact command — the serif's second and
 * last appearance in the happy path.
 *
 * `apiURL` is empty in one ordinary case, not just a slow load: "local"
 * mode, which has no separate server URL to name at all (see
 * `GetServerSettings`'s own doc). A plain browser tab always has one —
 * same-origin by default, or whatever Settings → Server last saved (see
 * `lib/browser/serverConfig.ts`) — so this only ever happens there while
 * `App`'s own `getServerSettings()` call is still in flight. Rendering
 * "Tried  — nothing answered" with a blank in the middle would look broken
 * in either case, so the "Tried ..." clause is simply omitted whenever
 * there is nothing real to put there instead.
 */
export function ColdStart({ connection, apiURL }: ColdStartProps) {
  const [showHelp, setShowHelp] = useState(false);
  const seconds =
    connection.retryAt !== undefined ? Math.max(0, Math.round((connection.retryAt - Date.now()) / 1000)) : undefined;

  return (
    <EmptyState
      headline="Can't reach the HELIOS-ATS service."
      headlineSize="lg"
      body={
        <p>
          {apiURL ? (
            <>
              Tried <span className={["num", styles.url].join(" ")}>{apiURL}</span> — nothing answered.{" "}
            </>
          ) : null}
          The desktop app is fine; the backend isn't running.
        </p>
      }
      actions={
        <div className={styles.actions}>
          <Button variant="primary" onClick={() => connectionMonitor.retryNow()}>
            Retry now
          </Button>
          {seconds !== undefined ? <span className={styles.retryHint}>retrying in {seconds}s</span> : null}
        </div>
      }
    >
      <div className={styles.disclosure}>
        <button type="button" className={styles.disclosureTrigger} onClick={() => setShowHelp((v) => !v)} aria-expanded={showHelp}>
          {showHelp ? "⌄" : "›"} How to start it
        </button>
        {showHelp ? <pre className={["num", "selectable", styles.command].join(" ")}>cd deploy && docker compose up -d</pre> : null}
      </div>
    </EmptyState>
  );
}
