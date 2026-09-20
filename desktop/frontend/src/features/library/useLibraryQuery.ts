import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ACTIVE_POLL_MS, HeliosError, IDLE_POLL_MS, list, type ListFilter, type Status, type Transcription } from "../../lib";
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
  // True once the user has loaded a second page; see the polling effect.
  const pagedRef = useRef(false);


  const apiFilter: ListFilter = useMemo(() => ({ status: status as Status | undefined, limit: PAGE_SIZE }), [status]);

  const fetchFirstPage = useCallback(() => {
    pagedRef.current = false;
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

  // Derived, not read from a ref: the effect must re-subscribe when this
  // flips so a job that only just started is polled at the fast cadence
  // immediately, rather than waiting out the idle timer that was already
  // scheduled when nothing was running.
  const hasActiveJob = useMemo(
    () => items.some((i) => i.status === "pending" || i.status === "running"),
    [items],
  );

  // Keep the library live. The sibling service could leave this view alone
  // because its SSE stream pushed every change; this service has no such
  // endpoint, so without polling the library is a snapshot frozen at mount
  // -- submit a file, come back, and a finished job is still shown as
  // running (or missing entirely). It refreshes on the same cadence as
  // lib/store.ts, fast while anything is in flight and slow when nothing
  // is.
  //
  // Two things it deliberately does NOT do: it never touches `loading` (a
  // background refresh must not flash the skeleton over a list the user is
  // reading), and it stands down entirely once the user has paged past the
  // first page, because replacing `items` with page one would silently
  // yank the rest out from under them. Paging is a deliberate act; it wins
  // until the next explicit refresh or filter change.
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const schedule = () => {
      timer = setTimeout(run, hasActiveJob ? ACTIVE_POLL_MS : IDLE_POLL_MS);
    };

    const run = () => {
      if (stopped || pagedRef.current) {
        if (!stopped) schedule();
        return;
      }
      const requestId = ++requestIdRef.current;
      list(apiFilter)
        .then((page) => {
          if (stopped || requestId !== requestIdRef.current) return;
          setItems(page.items);
          setCursor(page.nextCursor);
          setHasMore(Boolean(page.nextCursor));
          setError(undefined);
        })
        .catch(() => {
          // Swallowed on purpose: a failed background poll must not
          // replace a readable list with an error screen. ConnectionBar
          // already reports an unreachable server, and the next tick
          // recovers on its own.
        })
        .finally(() => {
          if (!stopped) schedule();
        });
    };

    schedule();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
    };
  }, [apiFilter, hasActiveJob]);

  const loadMore = useCallback(() => {
    if (loadingMore || !hasMore || !cursor) return;
    // Past the first page, the background refresh below stands down -- see
    // pagedRef.
    pagedRef.current = true;
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
