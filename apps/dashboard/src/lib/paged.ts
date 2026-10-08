import { infiniteQueryOptions, type InfiniteData, type QueryKey } from "@tanstack/react-query";

/**
 * One page of a list from the API (internal/page): newest first, and a
 * nextCursor while more follow. Lists that can grow are read a page at a
 * time with useInfiniteQuery and filtered on the server, never by filtering
 * page one here.
 */
export type Paged<T> = { items: T[]; nextCursor?: string };

/** Query options for a paged list: fetchPage reads the page after cursor (undefined for the first). */
export function pagedQuery<T, K extends QueryKey>(
  queryKey: K,
  fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<Paged<T>>,
  o: { refetchInterval?: number; enabled?: boolean; staleTime?: number } = {},
) {
  return infiniteQueryOptions({
    queryKey,
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => fetchPage(pageParam || undefined, signal),
    getNextPageParam: (last: Paged<T>) => last.nextCursor || undefined,
    ...o,
  });
}

/** Every loaded row once, in order: a row that moved up between two pages' reads shows where it was first seen. */
export function pagedRows<T>(data: InfiniteData<Paged<T>> | undefined, id: (row: T) => string | number): T[] {
  if (!data) return [];
  const seen = new Set<string | number>();
  const out: T[] = [];
  for (const p of data.pages)
    for (const r of p.items ?? []) {
      const k = id(r);
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(r);
    }
  return out;
}

/** A count that may be partial: "200+" while more pages follow. */
export function countShown(n: number, more: boolean | undefined): string {
  return more ? `${n.toLocaleString("en-GB")}+` : n.toLocaleString("en-GB");
}
