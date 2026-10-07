import { describe, expect, test } from "bun:test";
import { foldPaths, routeOf } from "@/components/observe-data";

/** Nearest-rank quantile over raw observations. */
function quantile(xs: number[], q: number): number {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.max(0, Math.ceil(q * s.length) - 1))];
}

/** What the log store answers per concrete path: count and its own quantiles. */
function statsRow(path: string, obs: number[]) {
  return { path, requests: String(obs.length), failed: "0", p50: String(quantile(obs, 0.5)), p95: String(quantile(obs, 0.95)) };
}

describe("foldPaths", () => {
  test("never reports a folded route faster than its requests were", () => {
    const fast = Array.from({ length: 50 }, () => 10);
    const slow = Array.from({ length: 50 }, () => 1000);
    const [route] = foldPaths([statsRow("/orders/1", fast), statsRow("/orders/2", slow)]);
    const truth = { p50: quantile([...fast, ...slow], 0.5), p95: quantile([...fast, ...slow], 0.95) };
    expect(route.path).toBe("/orders/:id");
    expect(route.requests).toBe(100);
    expect(route.paths).toBe(2);
    expect(route.bound).toBe(true);
    // Averaging the per-path p95s by requests said 505ms; every one of the
    // slow address's requests took 1000ms, so the real p95 is 1000ms.
    expect(route.p95).toBeGreaterThanOrEqual(truth.p95);
    expect(route.p50).toBeGreaterThanOrEqual(truth.p50);
  });

  test("is an upper bound across uneven mixes", () => {
    const groups = [
      Array.from({ length: 97 }, (_, i) => 5 + (i % 7)),
      Array.from({ length: 3 }, () => 900),
      Array.from({ length: 40 }, (_, i) => 50 + i),
    ];
    const [route] = foldPaths(groups.map((g, i) => statsRow(`/item/${i + 1}`, g)));
    const all = groups.flat();
    expect(route.p95).toBeGreaterThanOrEqual(quantile(all, 0.95));
    expect(route.p50).toBeGreaterThanOrEqual(quantile(all, 0.5));
  });

  test("keeps a single address's times exact", () => {
    const rows = foldPaths([
      { path: "/health", requests: "10", failed: "1", p50: "3", p95: "8" },
      { path: "/orders/7", requests: "4", failed: "0", p50: "20", p95: "40" },
    ]);
    expect(rows.map((r) => r.path)).toEqual(["/health", "/orders/:id"]);
    expect(rows[0]).toMatchObject({ requests: 10, failed: 1, p50: 3, p95: 8, paths: 1, bound: false });
    expect(rows[1]).toMatchObject({ p50: 20, p95: 40, bound: false });
  });

  test("folds ids the same way routeOf does", () => {
    expect(routeOf("/u/3f2a9c1e-7b4d-4a1e-9f00-123456789abc/edit")).toBe("/u/:id/edit");
    expect(routeOf("/")).toBe("/");
  });
});
