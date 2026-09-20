import styles from "./Skeleton.module.css";

export interface SkeletonProps {
  width?: string | number;
  height?: string | number;
  className?: string;
}

/** Skeleton — DESIGN.md §6.12. `--bg-hover` fill, `--radius-xs`, 1200ms opacity pulse (static under reduced motion). Never a centred spinner on a list — a skeleton tells you the shape of what is coming. */
export function Skeleton({ width = "100%", height = 12, className }: SkeletonProps) {
  return (
    <span
      aria-hidden="true"
      className={[styles.skeleton, className].filter(Boolean).join(" ")}
      style={{ width, height }}
    />
  );
}
