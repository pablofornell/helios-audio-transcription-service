import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import styles from "./Toast.module.css";

export interface ToastAction {
  label: string;
  onClick: () => void;
}

export interface ToastInput {
  message: string;
  action?: ToastAction;
}

interface ToastItem extends ToastInput {
  id: string;
}

const MAX_STACKED = 3;
const DISMISS_MS = 6000;

type Listener = () => void;

class ToastStore {
  private items: ToastItem[] = [];
  private listeners = new Set<Listener>();

  getSnapshot = (): ToastItem[] => this.items;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  show = (toast: ToastInput): string => {
    const id = crypto.randomUUID();
    this.items = [...this.items, { ...toast, id }].slice(-MAX_STACKED);
    this.emit();
    return id;
  };

  dismiss = (id: string): void => {
    this.items = this.items.filter((t) => t.id !== id);
    this.emit();
  };

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}

const toastStore = new ToastStore();

/**
 * `useToast()` — imperative toast API for feature code.
 *
 * DESIGN.md §6.10: toasts are ONLY for results the user cannot otherwise
 * see (an item deleted while on the Queue screen, "Path copied", "Settings
 * saved"). Never for a download completing (the row already said so) and
 * never for an error that belongs to a row (render it on the row/detail
 * panel instead).
 */
export function useToast(): { show: (toast: ToastInput) => string; dismiss: (id: string) => void } {
  return { show: toastStore.show, dismiss: toastStore.dismiss };
}

/**
 * ToastViewport — the bottom-right stack itself. Mount exactly once, at
 * the app shell's root, alongside every other screen.
 */
export function ToastViewport() {
  const items = useSyncExternalStore(toastStore.subscribe, toastStore.getSnapshot);
  return (
    <div className={styles.viewport}>
      {items.map((item) => (
        <ToastRow key={item.id} item={item} onDismiss={() => toastStore.dismiss(item.id)} />
      ))}
    </div>
  );
}

function ToastRow({ item, onDismiss }: { item: ToastItem; onDismiss: () => void }) {
  const [paused, setPaused] = useState(false);
  const remainingRef = useRef(DISMISS_MS);
  const startedAtRef = useRef(Date.now());
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => {
    if (paused) {
      if (timerRef.current) clearTimeout(timerRef.current);
      remainingRef.current -= Date.now() - startedAtRef.current;
      return;
    }
    startedAtRef.current = Date.now();
    timerRef.current = setTimeout(onDismiss, Math.max(0, remainingRef.current));
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paused]);

  return (
    <div
      className={styles.toast}
      role="status"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <span className={styles.message}>{item.message}</span>
      {item.action ? (
        <button
          type="button"
          className={styles.action}
          onClick={() => {
            item.action?.onClick();
            onDismiss();
          }}
        >
          {item.action.label}
        </button>
      ) : null}
    </div>
  );
}
