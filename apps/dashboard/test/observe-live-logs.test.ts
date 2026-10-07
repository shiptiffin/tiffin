import { describe, expect, test } from "bun:test";
import { toLine, type Row } from "@/components/logs-query";
import { appendLive, drainSince, logItems, type LiveTail, type LogPage } from "@/components/observe-data";

const T0 = Date.parse("2026-10-07T12:00:00Z");
const iso = (ms: number) => new Date(ms).toISOString();
const row = (ms: number, n: number): Row => ({ _time: iso(ms), _msg: `line ${n}`, app: "web" });

/**
 * A log store answering like /v1/observe/logs/query: rows in [start, end]
 * (both inclusive, end defaulting to now), newest first, cut at limit.
 */
function store(rows: Row[], limit: number) {
  const calls: Array<{ start: string; end?: string }> = [];
  const page = (start: string) => async (end?: string): Promise<LogPage> => {
    calls.push({ start, end });
    const s = Date.parse(start);
    const e = end ? Date.parse(end) : Infinity;
    const hit = rows.filter((r) => Date.parse(String(r._time)) >= s && Date.parse(String(r._time)) <= e);
    hit.sort((a, b) => Date.parse(String(b._time)) - Date.parse(String(a._time)));
    return { rows: hit.slice(0, limit), truncated: hit.length > limit };
  };
  return { page, calls };
}

describe("drainSince", () => {
  test("reads a burst bigger than one page in full", async () => {
    const checkpoint = toLine(row(T0, 0));
    const burst = Array.from({ length: 1300 }, (_, i) => row(T0 + 1 + i, i + 1));
    const s = store([row(T0, 0), ...burst], 500);
    const got = await drainSince(s.page(checkpoint.iso), checkpoint, 4);
    // One page used to be all a poll read: 800 of these were skipped for good.
    expect(got.gap).toBeUndefined();
    const keys = new Set(got.lines.map((l) => l.key));
    for (const r of burst) expect(keys.has(toLine(r).key)).toBe(true);
    expect(got.lines.map((l) => l.t)).toEqual([...got.lines.map((l) => l.t)].sort((a, b) => a - b));
    expect(s.calls.length).toBe(3);
  });

  test("stops at its page cap and says what it couldn't reach", async () => {
    const checkpoint = toLine(row(T0, 0));
    const burst = Array.from({ length: 2600 }, (_, i) => row(T0 + 1 + i, i + 1));
    const s = store([row(T0, 0), ...burst], 500);
    const got = await drainSince(s.page(checkpoint.iso), checkpoint, 4);
    expect(s.calls.length).toBe(4);
    expect(got.gap?.from).toBe(checkpoint.iso);
    const oldest = Math.min(...got.lines.map((l) => l.t));
    expect(got.gap?.to).toBe(iso(oldest));
    // Every line newer than the gap is in hand.
    const keys = new Set(got.lines.map((l) => l.key));
    for (const r of burst.filter((r) => Date.parse(String(r._time)) >= oldest)) expect(keys.has(toLine(r).key)).toBe(true);
  });

  test("marks a gap when a whole page shares one instant", async () => {
    const checkpoint = toLine(row(T0, 0));
    const same = Array.from({ length: 30 }, (_, i) => row(T0 + 5, i + 1));
    const s = store([row(T0, 0), row(T0 + 1, 99), ...same], 10);
    const got = await drainSince(s.page(checkpoint.iso), checkpoint, 4);
    expect(got.gap).toEqual({ from: checkpoint.iso, to: iso(T0 + 5) });
    expect(s.calls.length).toBe(2);
  });

  test("a quiet poll is one request", async () => {
    const checkpoint = toLine(row(T0, 0));
    const s = store([row(T0, 0), row(T0 + 10, 1)], 500);
    const got = await drainSince(s.page(checkpoint.iso), checkpoint, 4);
    expect(got.gap).toBeUndefined();
    expect(got.lines).toHaveLength(2);
    expect(s.calls.length).toBe(1);
  });
});

describe("appendLive", () => {
  const tail = (n: number): LiveTail => ({ lines: Array.from({ length: n }, (_, i) => toLine(row(T0 + i, i))), fresh: new Set(), gaps: [] });

  test("keeps at most cap lines, newest last, and marks the new ones fresh", () => {
    const prev = tail(5);
    const next = appendLive(prev, [toLine(row(T0 + 4, 4)), toLine(row(T0 + 10, 10)), toLine(row(T0 + 11, 11))], undefined, 6);
    expect(next.lines).toHaveLength(6);
    expect(next.lines[5].msg).toBe("line 11");
    expect([...next.fresh]).toEqual([toLine(row(T0 + 10, 10)).key, toLine(row(T0 + 11, 11)).key]);
  });

  test("keeps a gap after its line, and drops it once that line is trimmed", () => {
    const prev = tail(3);
    const anchor = prev.lines[2];
    const gap = { after: anchor.key, from: anchor.iso, to: iso(T0 + 50), skipped: 120 };
    const a = appendLive(prev, [toLine(row(T0 + 50, 50))], gap, 10);
    expect(a.gaps).toEqual([gap]);
    expect(logItems(a.lines, a.gaps).map((x) => x.type)).toEqual(["line", "line", "line", "gap", "line"]);
    const b = appendLive(a, Array.from({ length: 10 }, (_, i) => toLine(row(T0 + 100 + i, 100 + i))), undefined, 10);
    expect(b.gaps).toEqual([]);
  });

  test("a poll with nothing new keeps the same lines", () => {
    const prev = tail(3);
    const next = appendLive(prev, [prev.lines[2]], undefined, 10);
    expect(next.lines).toBe(prev.lines);
    expect(next.fresh.size).toBe(0);
  });
});

describe("logItems", () => {
  test("numbers the lines and gives every row its own key", () => {
    const lines = [toLine(row(T0, 0)), toLine(row(T0 + 1, 1))];
    const items = logItems(lines, [{ after: lines[0].key, from: lines[0].iso, to: lines[1].iso }]);
    expect(items.map((x) => x.type)).toEqual(["line", "gap", "line"]);
    expect(items.flatMap((x) => (x.type === "line" ? [x.pos] : []))).toEqual([1, 2]);
    expect(new Set(items.map((x) => x.key)).size).toBe(3);
  });
});
