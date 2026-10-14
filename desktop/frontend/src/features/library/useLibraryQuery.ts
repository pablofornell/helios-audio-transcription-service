import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ACTIVE_POLL_MS, IDLE_POLL_MS, HeliosError, get, list, getServerSettingsVersion, useServerSettingsVersion, type Transcription, type Status } from "../../lib";
import { hasStoredAudio } from "../../lib/contentEligibility";
import { compareLibraryItems, matchesSearch, type StatusFilterValue } from "./helpers";

const PAGE_SIZE = 40;

export interface UseLibraryQueryResult {
  /** Loaded items, client-side searched — what the list should render. */
  items: Transcription[];
  /** Loaded records before filename search, for the open detail panel. */
  loadedItems: Transcription[];
  loading: boolean;
  loadingMore: boolean;
  error?: HeliosError;
  hasMore: boolean;
  status: StatusFilterValue | undefined;
  setStatus: (status: StatusFilterValue | undefined) => void;
  search: string;
  setSearch: (s: string) => void;
  loadMore: () => void;
  refresh: () => void;
  updateItem: (id: string, patch: Partial<Transcription>) => void;
  removeItem: (id: string) => void;
}

/** Library uses the server's stored-audio ordering and keeps loaded pages live. */
export function useLibraryQuery(): UseLibraryQueryResult {
  const [status, setStatus] = useState<StatusFilterValue>();
  const [search, setSearch] = useState("");
  const [items, setItems] = useState<Transcription[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<HeliosError>();
  const [hasMore, setHasMore] = useState(false);
  const generation = useRef(0);
  const paging = useRef(false);
  const paged = useRef(false);
  const loaded = useRef(items);
  loaded.current = items;
  const settingsVersion = useServerSettingsVersion();
  const filter = useMemo(() => ({ library: true, limit: PAGE_SIZE, status: status as Status | undefined }), [status]);
  const eligible = useCallback((item: Transcription) => hasStoredAudio(item) && (!filter.status || item.status === filter.status), [filter]);
  const currentEligibility = useRef(eligible);
  currentEligibility.current = eligible;
  const invalidate = useCallback(() => {
    ++generation.current;
    paging.current = false;
    setLoadingMore(false);
  }, []);
  const refresh = useCallback(() => {
    invalidate();
    paged.current = false;
    const token = generation.current;
    const server = getServerSettingsVersion();
    setLoading(true);
    setError(undefined);
    setItems([]);
    setCursor(undefined);
    setHasMore(false);
    list(filter).then((page) => {
      if (token !== generation.current || server !== getServerSettingsVersion()) return;
      setItems(page.items.filter(eligible).sort(compareLibraryItems));
      setCursor(page.nextCursor);
      setHasMore(Boolean(page.nextCursor));
    }).catch((err: unknown) => {
      if (token === generation.current && server === getServerSettingsVersion()) setError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
    }).finally(() => {
      if (token === generation.current && server === getServerSettingsVersion()) setLoading(false);
    });
  }, [eligible, filter, invalidate]);
  useEffect(() => {
    refresh();
    return () => { ++generation.current; };
  }, [refresh, settingsVersion]);

  const hasActiveJob = items.some((item) => item.status === "running" || item.status === "pending");
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const schedule = () => { timer = setTimeout(run, hasActiveJob ? ACTIVE_POLL_MS : IDLE_POLL_MS); };
    const run = async () => {
      if (stopped) return;
      if (paging.current || loading) { schedule(); return; }
      const token = generation.current;
      const server = getServerSettingsVersion();
      const previous = loaded.current;
      try {
        const page = await list(filter);
        const firstIds = new Set(page.items.map((item) => item.id));
        const older = paged.current ? previous.filter((item) => !firstIds.has(item.id)) : [];
        const updated: Transcription[] = [];
        // Bound concurrent requests while retaining every loaded record.
        for (let start = 0; start < older.length; start += 8) {
          if (stopped || token !== generation.current || server !== getServerSettingsVersion()) return;
          const batch = await Promise.all(older.slice(start, start + 8).map(async (item) => {
            try { return await get(item.id); }
            catch (err) {
              if (err instanceof HeliosError && err.code === "not_found") return undefined;
              throw err;
            }
          }));
          updated.push(...batch.filter((item): item is Transcription => item !== undefined));
        }
        if (stopped || token !== generation.current || server !== getServerSettingsVersion()) return;
        const merged = new Map([...updated, ...page.items].map((item) => [item.id, item]));
        setItems([...merged.values()].filter(eligible).sort(compareLibraryItems));
        if (!paged.current) {
          setCursor(page.nextCursor);
          setHasMore(Boolean(page.nextCursor));
        }
        setError(undefined);
      } catch { /* A background failure leaves the readable list intact. */ }
      finally { if (!stopped) schedule(); }
    };
    schedule();
    return () => { stopped = true; clearTimeout(timer); };
  }, [eligible, filter, hasActiveJob, loading, settingsVersion]);

  const loadMore = useCallback(() => {
    if (loading || paging.current || !hasMore || !cursor) return;
    invalidate();
    const token = generation.current;
    const server = getServerSettingsVersion();
    paging.current = true;
    paged.current = true;
    setLoadingMore(true);
    list({ ...filter, cursor }).then((page) => {
      if (token !== generation.current || server !== getServerSettingsVersion()) return;
      setItems((previous) => {
        const merged = new Map([...page.items, ...previous].map((item) => [item.id, item]));
        return [...merged.values()].filter(eligible).sort(compareLibraryItems);
      });
      setCursor(page.nextCursor);
      setHasMore(Boolean(page.nextCursor));
    }).catch((err: unknown) => {
      if (token === generation.current && server === getServerSettingsVersion()) setError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
    }).finally(() => {
      if (token !== generation.current || server !== getServerSettingsVersion()) return;
      paging.current = false;
      setLoadingMore(false);
    });
  }, [loading, hasMore, cursor, filter, eligible, invalidate]);
  const updateItem = useCallback((id: string, patch: Partial<Transcription>) => {
    if (!loaded.current.some((item) => item.id === id)) return;
    invalidate();
    setItems((previous) => previous.map((item) => item.id === id ? { ...item, ...patch } : item).filter(currentEligibility.current).sort(compareLibraryItems));
  }, [invalidate]);
  const removeItem = useCallback((id: string) => {
    if (!loaded.current.some((item) => item.id === id)) return;
    invalidate();
    setItems((previous) => previous.filter((item) => item.id !== id));
  }, [invalidate]);
  const searched = useMemo(() => items.filter((item) => matchesSearch(item, search)), [items, search]);
  return { items: searched, loadedItems: items, loading, loadingMore, error, hasMore, status, setStatus, search, setSearch, loadMore, refresh, updateItem, removeItem };
}
