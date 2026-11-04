import { useCallback, useEffect, useRef, useState } from "react";
import { useToast } from "../../components";
import {
  HeliosError,
  getServerSettingsVersion,
  useServerSettingsVersion,
  cancel as apiCancel,
  retry as apiRetry,
  submit as apiSubmit,
  transcriptionStore,
  useTranscriptions,
  type AudioSource,
  type Options,
  type Status,
  type Transcription,
} from "../../lib";

/*
 * The Queue's live model — merges `useTranscriptions()` (the app-wide,
 * polling-updated cache, shared with Library) with this screen's own
 * upload-in-flight bookkeeping, and owns the row-lifecycle behaviour this
 * feature needs: an "uploading…" placeholder for the (possibly slow, for a
 * large file) window between choosing a file and the server actually
 * accepting the job, cancel's optimism, and "Clear completed"'s
 * view-local dismissal.
 *
 * Deliberately NOT a second polling cache — every row this hook produces
 * once it has a real id is read straight from `useTranscriptions()`'s
 * snapshot; this hook only ever adds *ahead* of that snapshot, for however
 * long the upload itself takes.
 */

export type SubmitOutcome = { ok: true; id: string } | { ok: false; error: HeliosError };

export type QueueRowModel =
  | { kind: "uploading"; key: string; name: string }
  | { kind: "real"; key: string; transcription: Transcription };

export interface UseTranscribeQueue {
  /** Upload placeholders and non-dismissed transcriptions; QueueList orders the sections. */
  rows: QueueRowModel[];
  /** True only for the very first load (mirrors `useTranscriptions()`'s own `loading`). */
  loading: boolean;
  error?: HeliosError;
  submit: (source: AudioSource, options: Options) => Promise<SubmitOutcome>;
  cancel: (id: string) => void;
  retry: (id: string) => void;
  isCanceling: (id: string) => boolean;
  isEntering: (key: string) => boolean;
  /** Hides every succeeded row from view. Client-only — never deletes anything, just stops showing it until the app restarts. Failed and canceled rows stay: the queue is their only list. */
  clearFinished: () => void;
}

export function isTerminalStatus(status: Status): boolean {
  return status === "succeeded" || status === "failed" || status === "canceled";
}

function without<T>(set: Set<T>, item: T): Set<T> {
  if (!set.has(item)) return set;
  const next = new Set(set);
  next.delete(item);
  return next;
}

interface UploadEntry {
  key: string;
  name: string;
}

const ENTER_MS = 220;

export function useTranscribeQueue(): UseTranscribeQueue {
  const { transcriptions, loading, error } = useTranscriptions();
  const toast = useToast();
  const serverVersion = useServerSettingsVersion();
  const mountedRef = useRef(false);
  const entranceTimers = useRef(new Set<ReturnType<typeof setTimeout>>());
  const pendingCancels = useRef(new Set<string>());
  const pendingRetries = useRef(new Set<string>());

  const isCurrent = useCallback((version: number) => mountedRef.current && version === getServerSettingsVersion(), []);

  const [uploading, setUploading] = useState<UploadEntry[]>([]);
  const [cancelingIds, setCancelingIds] = useState<Set<string>>(new Set());
  const [enteringKeys, setEnteringKeys] = useState<Set<string>>(new Set());
  const [dismissedIds, setDismissedIds] = useState<Set<string>>(new Set());

  const transcriptionsRef = useRef(transcriptions);
  const seenKeysRef = useRef<Set<string>>(new Set());
  const initializedRef = useRef(false);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      for (const timer of entranceTimers.current) clearTimeout(timer);
      entranceTimers.current.clear();
    };
  }, []);

  useEffect(() => {
    for (const timer of entranceTimers.current) clearTimeout(timer);
    entranceTimers.current.clear();
    pendingCancels.current.clear();
    pendingRetries.current.clear();
    seenKeysRef.current.clear();
    initializedRef.current = false;
    setUploading([]);
    setCancelingIds(new Set());
    setEnteringKeys(new Set());
    setDismissedIds(new Set());
  }, [serverVersion]);

  useEffect(() => {
    transcriptionsRef.current = transcriptions;
  }, [transcriptions]);

  const uploadingRows: QueueRowModel[] = uploading.map((e) => ({ kind: "uploading", key: e.key, name: e.name }));
  const storeRows: QueueRowModel[] = transcriptions
    .filter((t) => !dismissedIds.has(t.id))
    .map((t) => ({ kind: "real", key: t.id, transcription: t }));
  const rows: QueueRowModel[] = [...uploadingRows, ...storeRows];

  // Entrance bookkeeping (new rows) and clearing `cancelingIds` once a
  // cancel is confirmed by the next poll.
  useEffect(() => {
    const currentKeys = new Set(rows.map((r) => r.key));
    const newlyEntering: string[] = [];
    for (const row of rows) {
      if (!seenKeysRef.current.has(row.key)) {
        seenKeysRef.current.add(row.key);
        if (initializedRef.current) newlyEntering.push(row.key);
      }
    }
    // Only a first *loaded* snapshot counts as "already here" — this
    // effect's very first run fires while the store is still `loading`
    // with an empty list.
    if (!loading) initializedRef.current = true;

    if (newlyEntering.length) {
      setEnteringKeys((prev) => new Set([...prev, ...newlyEntering]));
      const version = getServerSettingsVersion();
      for (const key of newlyEntering) {
        const timer = setTimeout(() => {
          entranceTimers.current.delete(timer);
          if (isCurrent(version)) setEnteringKeys((prev) => without(prev, key));
        }, ENTER_MS);
        entranceTimers.current.add(timer);
      }
    }

    for (const key of seenKeysRef.current) {
      if (!currentKeys.has(key)) seenKeysRef.current.delete(key);
    }

    setCancelingIds((prev) => {
      if (prev.size === 0) return prev;
      let changed = false;
      const next = new Set(prev);
      for (const id of prev) {
        const t = transcriptions.find((i) => i.id === id);
        // Any terminal status — not just "canceled" — ends the optimistic
        // cancel: a job that resolves (or fails) a beat ahead of the
        // cancel landing must not sit showing a disabled "Canceling…" pill
        // forever.
        if (!t || isTerminalStatus(t.status)) {
          next.delete(id);
          changed = true;
        }
      }
      return changed ? next : prev;
    });
    // rows is derived from [transcriptions, uploading] each render;
    // depending on those two directly avoids re-running this effect every
    // render for no reason.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transcriptions, uploading, loading]);

  const submit = useCallback(async (source: AudioSource, options: Options): Promise<SubmitOutcome> => {
    const version = getServerSettingsVersion();
    const key = crypto.randomUUID();
    const name = source.kind === "file" ? source.file.name : source.name;
    setUploading((prev) => [{ key, name }, ...prev]);

    try {
      const transcription = await apiSubmit(source, options);
      if (isCurrent(version)) {
        seenKeysRef.current.add(transcription.id);
        setUploading((prev) => prev.filter((e) => e.key !== key));
        transcriptionStore.refreshNow();
      }
      return { ok: true, id: transcription.id };
    } catch (err) {
      if (isCurrent(version)) setUploading((prev) => prev.filter((e) => e.key !== key));
      const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
      return { ok: false, error: heliosErr };
    }
  }, [isCurrent]);

  const cancel = useCallback(
    (id: string) => {
      if (pendingCancels.current.has(id) || cancelingIds.has(id)) return;
      const version = getServerSettingsVersion();
      pendingCancels.current.add(id);
      setCancelingIds((prev) => new Set(prev).add(id));
      apiCancel(id)
        .then(() => {
          if (isCurrent(version)) transcriptionStore.refreshNow();
        })
        .catch((err: unknown) => {
          if (!isCurrent(version)) return;
          setCancelingIds((prev) => without(prev, id));
          const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
          toast.show({ message: `Couldn't cancel — ${heliosErr.message}` });
        })
        .finally(() => {
          if (isCurrent(version)) pendingCancels.current.delete(id);
        });
    },
    [cancelingIds, isCurrent, toast.show],
  );

  const retry = useCallback(
    (id: string) => {
      if (pendingRetries.current.has(id)) return;
      const version = getServerSettingsVersion();
      pendingRetries.current.add(id);
      apiRetry(id)
        .then(() => {
          if (isCurrent(version)) transcriptionStore.refreshNow();
        })
        .catch((err: unknown) => {
          if (!isCurrent(version)) return;
          const heliosErr = err instanceof HeliosError ? err : new HeliosError("unknown", String(err));
          toast.show({ message: `Couldn't retry — ${heliosErr.message}` });
        })
        .finally(() => {
          if (isCurrent(version)) pendingRetries.current.delete(id);
        });
    },
    [isCurrent, toast.show],
  );

  const clearFinished = useCallback(() => {
    setDismissedIds((prev) => {
      const next = new Set(prev);
      for (const t of transcriptionsRef.current) {
        if (t.status === "succeeded") next.add(t.id);
      }
      return next;
    });
  }, []);

  return {
    rows,
    loading,
    error,
    submit,
    cancel,
    retry,
    isCanceling: (id: string) => cancelingIds.has(id),
    isEntering: (key: string) => enteringKeys.has(key),
    clearFinished,
  };
}
