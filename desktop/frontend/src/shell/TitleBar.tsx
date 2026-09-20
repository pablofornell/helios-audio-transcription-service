import { IconButton } from "../components";
import { useTheme } from "../lib";
import styles from "./TitleBar.module.css";

export interface TitleBarProps {
  onOpenSettings: () => void;
}

/**
 * TitleBar — the 44px app toolbar under the native window chrome
 * (DESIGN.md §4.1's mockup). Carries the explicit theme toggle (System /
 * Light / Dark cycles through on click; the full picker also lives in
 * Settings) and the Settings entry point.
 */
export function TitleBar({ onOpenSettings }: TitleBarProps) {
  const [preference, setPreference] = useTheme();

  function cycleTheme() {
    const order: Array<typeof preference> = ["system", "light", "dark"];
    const next = order[(order.indexOf(preference) + 1) % order.length]!;
    setPreference(next);
  }

  return (
    <header className={styles.toolbar}>
      <span className={styles.title}>HELIOS-ATS</span>
      <div className={styles.actions}>
        <IconButton
          size={28}
          icon={<ThemeGlyph preference={preference} />}
          label={`Theme: ${themeLabel(preference)} — click to change`}
          onClick={cycleTheme}
        />
        <IconButton size={28} icon={<GearGlyph />} label="Settings" onClick={onOpenSettings} />
      </div>
    </header>
  );
}

function themeLabel(preference: "system" | "light" | "dark"): string {
  return preference === "system" ? "System" : preference === "light" ? "Light" : "Dark";
}

function ThemeGlyph({ preference }: { preference: "system" | "light" | "dark" }) {
  if (preference === "dark") {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
        <path
          d="M13.5 9.5A5.8 5.8 0 0 1 6.5 2.5a5.8 5.8 0 1 0 7 7z"
          stroke="currentColor"
          strokeWidth="1.4"
          strokeLinejoin="round"
        />
      </svg>
    );
  }
  if (preference === "light") {
    return (
      <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
        <circle cx="8" cy="8" r="3" stroke="currentColor" strokeWidth="1.4" />
        <path d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.5 3.5l1.4 1.4M11.1 11.1l1.4 1.4M12.5 3.5l-1.4 1.4M4.9 11.1l-1.4 1.4" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round" />
      </svg>
    );
  }
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <rect x="2" y="3.5" width="12" height="8" rx="1" stroke="currentColor" strokeWidth="1.4" />
      <path d="M5.5 13.5h5" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  );
}

function GearGlyph() {
  return (
    // Toothed cog (Lucide "settings", ISC) so it can't be mistaken for the
    // light-theme sun next to it. Drawn on a 24 grid; stroke 2 renders ~1.33px at 16px.
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
      <path
        d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <circle cx="12" cy="12" r="3" stroke="currentColor" strokeWidth="2" />
    </svg>
  );
}
