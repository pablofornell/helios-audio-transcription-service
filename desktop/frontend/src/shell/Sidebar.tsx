import type { ReactNode } from "react";
import { useTranscriptions, type HealthReport } from "../lib";
import { Tooltip } from "../components";
import styles from "./Sidebar.module.css";

export type ScreenId = "transcribe" | "library";

export interface SidebarProps {
  screen: ScreenId;
  onNavigate: (screen: ScreenId) => void;
  collapsed: boolean;
  health?: HealthReport;
}

/**
 * Sidebar — DESIGN.md §4.1, adapted: two destinations only (Submit is a
 * docked composer inside Transcribe, not a destination). The Transcribe
 * item's live count badge is tabular, `--st-running` while anything is
 * transcribing, `--text-tertiary` when only queued, and omitted at zero.
 * Below `--sidebar-w-collapsed` (1040px window width, decided by the
 * caller) labels move into tooltips and only the glyph + count remain —
 * DESIGN.md §8's screen-reader rule that a count is never icon-only still
 * holds via each item's `aria-label`.
 */
export function Sidebar({ screen, onNavigate, collapsed, health }: SidebarProps) {
  const { transcriptions } = useTranscriptions();
  const runningCount = transcriptions.filter((t) => t.status === "running").length;
  const queuedCount = transcriptions.filter((t) => t.status === "pending").length;
  const activeCount = runningCount + queuedCount;

  const storageCheck = health?.checks?.storage;
  const storageWarning = storageCheck && !storageCheck.ok;

  return (
    <nav className={[styles.sidebar, collapsed ? styles.collapsed : ""].filter(Boolean).join(" ")} aria-label="Primary">
      {!collapsed ? <div className={styles.wordmark}>HELIOS-ATS</div> : null}
      <ul className={styles.nav}>
        <SidebarItem
          id="transcribe"
          label="Transcribe"
          active={screen === "transcribe"}
          collapsed={collapsed}
          count={activeCount}
          countTone={runningCount > 0 ? "running" : "tertiary"}
          onNavigate={onNavigate}
          icon={<TranscribeGlyph />}
          ariaLabel={activeCount > 0 ? `Transcribe, ${activeCount} active jobs.` : "Transcribe"}
        />
        <SidebarItem
          id="library"
          label="Library"
          active={screen === "library"}
          collapsed={collapsed}
          onNavigate={onNavigate}
          icon={<LibraryGlyph />}
          ariaLabel="Library"
        />
      </ul>
      <div className={styles.footer}>
        {storageWarning ? (
          <Tooltip content={storageCheck?.detail ?? "Storage check failed"}>
            <span className={styles.storageWarning}>{collapsed ? "!" : "Low disk space"}</span>
          </Tooltip>
        ) : null}
      </div>
    </nav>
  );
}

interface SidebarItemProps {
  id: ScreenId;
  label: string;
  active: boolean;
  collapsed: boolean;
  count?: number;
  countTone?: "running" | "tertiary";
  onNavigate: (screen: ScreenId) => void;
  icon: ReactNode;
  ariaLabel: string;
}

function SidebarItem({ id, label, active, collapsed, count, countTone, onNavigate, icon, ariaLabel }: SidebarItemProps) {
  const item = (
    <li>
      <button
        type="button"
        className={[styles.item, active ? styles.active : ""].filter(Boolean).join(" ")}
        onClick={() => onNavigate(id)}
        aria-current={active ? "page" : undefined}
        aria-label={ariaLabel}
      >
        <span className={styles.itemIcon} aria-hidden="true">
          {icon}
        </span>
        {!collapsed ? <span className={styles.itemLabel}>{label}</span> : null}
        {count ? (
          <span
            className={[styles.count, "num", countTone === "running" ? styles.countRunning : styles.countTertiary].join(" ")}
            aria-live="off"
          >
            {count}
          </span>
        ) : null}
      </button>
    </li>
  );
  return collapsed ? <Tooltip content={ariaLabel} side="right">{item}</Tooltip> : item;
}

function TranscribeGlyph() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <path d="M4 2.5h6l2.5 2.5V13a.5.5 0 0 1-.5.5H4a.5.5 0 0 1-.5-.5V3a.5.5 0 0 1 .5-.5z" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
      <path d="M5.5 8h5M5.5 10.2h3.5" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" />
    </svg>
  );
}

function LibraryGlyph() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <rect x="2.5" y="3" width="11" height="10" rx="1" stroke="currentColor" strokeWidth="1.5" />
      <path d="M2.5 6.5h11" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  );
}
