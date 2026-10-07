// How row writes reach the cached pages of a table: an immediate patch so
// the grid shows the change, then a refetch so filters, sort, counts and
// cursors come from the server again.
import type { InfiniteData, QueryClient, QueryKey } from "@tanstack/react-query";

type Page = { rows?: unknown[][]; count?: number };
export type Pages<P extends Page = Page> = InfiniteData<P, string>;

/**
 * Replaces rows in every loaded page, each found by the key it had before
 * the write (an edit may change the key itself), and drops the keys in drop.
 */
export function replaceRows<P extends Page>(d: Pages<P> | undefined, keyOf: (r: unknown[]) => string, replace: Array<[was: string, row: unknown[]]>, drop?: Set<string>): Pages<P> | undefined {
  if (!d) return d;
  const byKey = new Map(replace);
  return { ...d, pages: d.pages.map((p) => ({ ...p, rows: (p.rows ?? []).filter((r) => !drop?.has(keyOf(r))).map((r) => byKey.get(keyOf(r)) ?? r) })) };
}

/** Puts new rows at the top of the first page and counts them. */
export function prependRows<P extends Page>(d: Pages<P> | undefined, fresh: unknown[][]): Pages<P> | undefined {
  if (!d) return d;
  return { ...d, pages: d.pages.map((p, i) => (i === 0 ? { ...p, rows: [...fresh, ...(p.rows ?? [])], count: p.count === undefined ? p.count : p.count + fresh.length } : p)) };
}

/**
 * Brackets writes to a table's rows. start cancels row reads in flight, so
 * an older answer can't land over a patch; when the last write in flight
 * ends, every cached view of the table (any filter or sort) is invalidated
 * and the one on screen refetches. Make one per table view.
 */
export function rowWrites(qc: QueryClient) {
  let inFlight = 0;
  return {
    start: async (table: QueryKey) => {
      inFlight++;
      await qc.cancelQueries({ queryKey: table });
    },
    end: (table: QueryKey) => {
      inFlight = Math.max(0, inFlight - 1);
      if (inFlight === 0) void qc.invalidateQueries({ queryKey: table });
    },
  };
}
