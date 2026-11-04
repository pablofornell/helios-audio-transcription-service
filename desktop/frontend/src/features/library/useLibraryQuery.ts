import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { IDLE_POLL_MS, HeliosError, get, list, getServerSettingsVersion, useServerSettingsVersion, type Transcription } from "../../lib";
import { isLibraryEligible } from "../../lib/contentEligibility";
import { compareLibraryItems, matchesSearch } from "./helpers";

const PAGE_SIZE = 40;
const FILTER = { library: true, limit: PAGE_SIZE };

export interface UseLibraryQueryResult {
  /** Loaded items, client-side searched — what the list should render. */
  items: Transcription[];
  /** Loaded records before filename search, for the open detail panel. */
  loadedItems: Transcription[];
  loading: boolean;
  loadingMore: boolean;
  error?: HeliosError;
  hasMore: boolean;
  search: string;
  setSearch: (s: string) => void;
  loadMore: () => void;
  refresh: () => void;
  updateItem: (id: string, patch: Partial<Transcription>) => void;
  removeItem: (id: string) => void;
}

/** Library lists finished items in the server's order. It keeps loaded pages live and drops any item that is no longer finished. */
export function useLibraryQuery(): UseLibraryQueryResult {
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
    list(FILTER).then((page) => {
      if (token !== generation.current || server !== getServerSettingsVersion()) return;
      setItems(page.items.filter(isLibraryEligible).sort(compareLibraryItems));
      setCursor(page.nextCursor);
      setHasMore(Boolean(page.nextCursor));
    }).catch((err: unknown) => {
      if (token === generation.current && server === getServerSettingsVersion()) setError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
    }).finally(() => {
      if (token === generation.current && server === getServerSettingsVersion()) setLoading(false);
    });
  }, [invalidate]);
  useEffect(() => {
    refresh();
    return () => { ++generation.current; };
  }, [refresh, settingsVersion]);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const schedule = () => { timer = setTimeout(run, IDLE_POLL_MS); };
    const run = async () => {
      if (stopped) return;
      if (paging.current || loading) { schedule(); return; }
      const token = generation.current;
      const server = getServerSettingsVersion();
      const previous = loaded.current;
      try {
        const page = await list(FILTER);
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
        setItems([...merged.values()].filter(isLibraryEligible).sort(compareLibraryItems));
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
  }, [loading, settingsVersion]);

  const loadMore = useCallback(() => {
    if (loading || paging.current || !hasMore || !cursor) return;
    invalidate();
    const token = generation.current;
    const server = getServerSettingsVersion();
    paging.current = true;
    paged.current = true;
    setLoadingMore(true);
    list({ ...FILTER, cursor }).then((page) => {
      if (token !== generation.current || server !== getServerSettingsVersion()) return;
      setItems((previous) => {
        const merged = new Map([...page.items, ...previous].map((item) => [item.id, item]));
        return [...merged.values()].filter(isLibraryEligible).sort(compareLibraryItems);
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
  }, [loading, hasMore, cursor, invalidate]);
  const updateItem = useCallback((id: string, patch: Partial<Transcription>) => {
    if (!loaded.current.some((item) => item.id === id)) return;
    invalidate();
    setItems((previous) => previous.map((item) => item.id === id ? { ...item, ...patch } : item).filter(isLibraryEligible).sort(compareLibraryItems));
  }, [invalidate]);
  const removeItem = useCallback((id: string) => {
    if (!loaded.current.some((item) => item.id === id)) return;
    invalidate();
    setItems((previous) => previous.filter((item) => item.id !== id));
  }, [invalidate]);
  const searched = useMemo(() => items.filter((item) => matchesSearch(item, search)), [items, search]);
  return { items: searched, loadedItems: items, loading, loadingMore, error, hasMore, search, setSearch, loadMore, refresh, updateItem, removeItem };
}
