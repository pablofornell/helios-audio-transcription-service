import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { HeliosError, list, type ListFilter, type Status, type Transcription } from "../../lib";
import { matchesSearch, type StatusFilterValue } from "./helpers";

const PAGE_SIZE = 40;

export interface UseLibraryQueryResult {
  /** Loaded items, client-side searched — what the list should render. */
  items: Transcription[];
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

/**
 * Library's own data source: cursor-paginated, filtered by the single
 * `status` query param docs/openapi.yaml's `GET /transcriptions` supports,
 * searched client-side by filename. Deliberately independent of
 * `lib/store.ts`'s `useTranscriptions()` — that store is a single
 * unfiltered, unpaginated (well, up to 200) page; Library's own filtered,
 * paginated view calls `list()` directly instead.
 */
export function useLibraryQuery(): UseLibraryQueryResult {
  const [status, setStatus] = useState<StatusFilterValue | undefined>(undefined);
  const [search, setSearch] = useState("");

  const [items, setItems] = useState<Transcription[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<HeliosError | undefined>(undefined);
  const [hasMore, setHasMore] = useState(false);

  // Guards against a slow, superseded request (filters changed again while
  // it was in flight) clobbering a newer one's result.
  const requestIdRef = useRef(0);

  const apiFilter: ListFilter = useMemo(() => ({ status: status as Status | undefined, limit: PAGE_SIZE }), [status]);

  const fetchFirstPage = useCallback(() => {
    const requestId = ++requestIdRef.current;
    setLoading(true);
    setError(undefined);
    list(apiFilter)
      .then((page) => {
        if (requestId !== requestIdRef.current) return;
        setItems(page.items);
        setCursor(page.nextCursor);
        setHasMore(Boolean(page.nextCursor));
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (requestId !== requestIdRef.current) return;
        setError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
        setLoading(false);
      });
  }, [apiFilter]);

  useEffect(() => {
    fetchFirstPage();
  }, [fetchFirstPage]);

  const loadMore = useCallback(() => {
    if (loadingMore || !hasMore || !cursor) return;
    const requestId = requestIdRef.current;
    setLoadingMore(true);
    list({ ...apiFilter, cursor })
      .then((page) => {
        if (requestId !== requestIdRef.current) return;
        setItems((prev) => {
          const seen = new Set(prev.map((i) => i.id));
          return [...prev, ...page.items.filter((i) => !seen.has(i.id))];
        });
        setCursor(page.nextCursor);
        setHasMore(Boolean(page.nextCursor));
        setLoadingMore(false);
      })
      .catch((err: unknown) => {
        if (requestId !== requestIdRef.current) return;
        setError(err instanceof HeliosError ? err : new HeliosError("unknown", String(err)));
        setLoadingMore(false);
      });
  }, [apiFilter, cursor, hasMore, loadingMore]);

  const updateItem = useCallback((id: string, patch: Partial<Transcription>) => {
    setItems((prev) => prev.map((i) => (i.id === id ? { ...i, ...patch } : i)));
  }, []);

  const removeItem = useCallback((id: string) => {
    setItems((prev) => prev.filter((i) => i.id !== id));
  }, []);

  const searched = useMemo(() => items.filter((i) => matchesSearch(i, search)), [items, search]);

  return {
    items: searched,
    loading,
    loadingMore,
    error,
    hasMore,
    status,
    setStatus,
    search,
    setSearch,
    loadMore,
    refresh: fetchFirstPage,
    updateItem,
    removeItem,
  };
}
