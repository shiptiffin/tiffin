// Row writes against the cached pages of a table: rows keep their identity
// when an edit changes the key, and every cached view is refetched after.
import { describe, expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { prependRows, replaceRows, rowWrites, type Pages } from "@/routes/data/rows-cache";

const keyOf = (r: unknown[]) => String(r[0]);
const pages = (...rows: unknown[][][]): Pages => ({ pages: rows.map((r, i) => ({ rows: r, count: i === 0 ? 3 : undefined })), pageParams: rows.map((_, i) => (i ? `c${i}` : "")) });
const keys = (d?: Pages) => d?.pages.flatMap((p) => (p.rows ?? []).map(keyOf));

describe("replaceRows finds each row by the key it had before the write", () => {
  test("changing a text primary key a → b keeps the row, under its new key", () => {
    const d = pages([["a", "x"], ["c", "y"]], [["d", "z"]]);
    // The optimistic row, found by its old key.
    const optimistic = replaceRows(d, keyOf, [["a", ["b", "x"]]]);
    expect(keys(optimistic)).toEqual(["b", "c", "d"]);
    // The server's row, found by the key the optimistic one now has.
    const saved = replaceRows(optimistic, keyOf, [["b", ["b", "x2"]]]);
    expect(saved?.pages[0].rows?.[0]).toEqual(["b", "x2"]);
    // Rolling back, likewise.
    expect(keys(replaceRows(optimistic, keyOf, [["b", ["a", "x"]]]))).toEqual(["a", "c", "d"]);
  });

  test("drops rows by key in every page", () => {
    expect(keys(replaceRows(pages([["a"], ["c"]], [["d"]]), keyOf, [], new Set(["c", "d"])))).toEqual(["a"]);
  });

  test("prepends new rows to the first page and counts them", () => {
    const d = prependRows(pages([["a"]], [["b"]]), [["n"]]);
    expect(keys(d)).toEqual(["n", "a", "b"]);
    expect(d?.pages[0].count).toBe(4);
  });
});

describe("rowWrites refetches every view of the table once writes settle", () => {
  const table = ["pg-rows", "shop", "", "public", "books"];
  const filtered = [...table, "price.lt.10", ""];
  const plain = [...table, "", ""];
  const other = ["pg-rows", "shop", "", "public", "authors", "", ""];

  test("after the last write in flight, filtered and sorted views are invalidated; other tables aren't", async () => {
    const qc = new QueryClient();
    for (const k of [filtered, plain, other]) qc.setQueryData(k, pages([["a"]]));
    const w = rowWrites(qc);
    await w.start(table);
    await w.start(table);
    w.end(table);
    expect(qc.getQueryState(filtered)?.isInvalidated).toBe(false); // one write still saving
    w.end(table);
    expect(qc.getQueryState(filtered)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(plain)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(other)?.isInvalidated).toBe(false);
  });

  test("a read in flight when a write starts can't land over the patch", async () => {
    const qc = new QueryClient();
    qc.setQueryData(plain, "old");
    const late = qc.fetchQuery({ queryKey: plain, queryFn: () => new Promise<string>((r) => setTimeout(() => r("late"), 30)), staleTime: 0 }).catch(() => "cancelled");
    const w = rowWrites(qc);
    await w.start(table);
    qc.setQueryData(plain, "patched");
    expect(await late).not.toBe("late"); // cancelled: it settles with what was cached before
    await Bun.sleep(50);
    expect(qc.getQueryData<string>(plain)).toBe("patched");
  });
});
