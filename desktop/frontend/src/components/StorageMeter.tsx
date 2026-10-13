import { Tooltip } from "./Tooltip";
import styles from "./StorageMeter.module.css";

export interface StorageMeterProps {
  freeBytes?: number | null;
  totalBytes?: number | null;
  detail?: string;
  collapsed?: boolean;
}

function bytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let power = 0;
  while (value >= 1000 && power < units.length - 1) { value /= 1000; power++; }
  return `${power === 0 ? Math.round(value) : value.toFixed(1)} ${units[power]}`;
}

/** Capacity is optional: diagnostic text never supplies a percentage. */
export function StorageMeter({ freeBytes, totalBytes, detail, collapsed = false }: StorageMeterProps) {
  const free = typeof freeBytes === "number" && Number.isFinite(freeBytes) && freeBytes >= 0 ? freeBytes : null;
  const total = typeof totalBytes === "number" && Number.isFinite(totalBytes) && totalBytes > 0 ? totalBytes : null;
  const percent = free !== null && total !== null && free <= total ? free / total * 100 : null;
  const tone = percent === null ? "unknown" : percent < 10 ? "critical" : percent < 20 ? "warning" : "healthy";
  const value = free === null ? detail || "Unavailable" : `${bytes(free)} free${total !== null && free <= total ? ` of ${bytes(total)}` : ""}`;
  const label = `Server storage: ${value}`;
  const meter = (
    <section className={[styles.meter, styles[tone], collapsed ? styles.collapsed : ""].join(" ")} aria-label={collapsed ? label : "Server storage"} title={label} tabIndex={collapsed ? 0 : undefined}>
      {!collapsed && <span className={styles.label}>Server storage</span>}
      <span className={styles.value}>{collapsed && free !== null ? bytes(free) : value}</span>
      <div className={styles.track} role={percent === null ? undefined : "meter"} aria-label="Server storage free space" aria-valuemin={percent === null ? undefined : 0} aria-valuemax={percent === null ? undefined : 100} aria-valuenow={percent ?? undefined} aria-valuetext={percent === null ? undefined : `${Math.round(percent)}% free — ${value}`}>
        {percent !== null && <span className={styles.fill} style={{ width: `${percent}%` }} />}
      </div>
    </section>
  );
  return collapsed ? <Tooltip content={label} side="right">{meter}</Tooltip> : meter;
}
