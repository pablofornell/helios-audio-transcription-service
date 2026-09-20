import { useEffect, useRef, useState } from "react";
import { Button } from "../components";
import { connectionMonitor, type ConnectionState } from "../lib";
import styles from "./ConnectionBar.module.css";

export interface ConnectionBarProps {
  connection: ConnectionState;
}

/**
 * ConnectionBar — DESIGN.md §5.7. A 28px bar that slides in under the
 * toolbar on a mid-session connection loss (never a full-surface takeover
 * — the user's rows are still true, just frozen). `role="status"`,
 * announces once on appearance and once on "Reconnected", never per retry
 * attempt.
 *
 * Renders nothing while `status` is `"connecting"` (too early to say
 * anything useful) or `"offline"` (the cold-start case is a full-surface
 * state owned by the shell, not this bar — see `ColdStart.tsx`).
 */
export function ConnectionBar({ connection }: ConnectionBarProps) {
  const [showReconnected, setShowReconnected] = useState(false);
  const wasReconnectingRef = useRef(false);
  const [seconds, setSeconds] = useState(computeSeconds(connection.retryAt));

  useEffect(() => {
    if (wasReconnectingRef.current && connection.status === "online") {
      setShowReconnected(true);
      const t = setTimeout(() => setShowReconnected(false), 1500);
      wasReconnectingRef.current = false;
      return () => clearTimeout(t);
    }
    wasReconnectingRef.current = connection.status === "reconnecting";
    return undefined;
  }, [connection.status]);

  useEffect(() => {
    setSeconds(computeSeconds(connection.retryAt));
    if (connection.retryAt === undefined) return undefined;
    const id = setInterval(() => setSeconds(computeSeconds(connection.retryAt)), 1000);
    return () => clearInterval(id);
  }, [connection.retryAt]);

  const visible = connection.status === "reconnecting" || showReconnected;
  if (!visible) return null;

  return (
    <div className={[styles.bar, showReconnected ? styles.success : ""].filter(Boolean).join(" ")} role="status">
      {showReconnected ? (
        <span className={styles.message}>Reconnected</span>
      ) : (
        <>
          <span className={styles.message}>
            ⚠ Lost connection to HELIOS-ATS. Reconnecting… (attempt {connection.attempt})
            {seconds !== undefined ? <span className={styles.countdown}> · retrying in {seconds}s</span> : null}
          </span>
          <Button size="sm" variant="ghost" onClick={() => connectionMonitor.retryNow()}>
            Retry now
          </Button>
        </>
      )}
    </div>
  );
}

function computeSeconds(retryAt: number | undefined): number | undefined {
  if (retryAt === undefined) return undefined;
  return Math.max(0, Math.round((retryAt - Date.now()) / 1000));
}
