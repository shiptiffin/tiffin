import { afterEach, describe, expect, test } from "bun:test";
import { reportWebVitals, routeOf, VitalsQueue } from "../src/vitals";

describe("routeOf", () => {
  test("turns parameter values into their names", () => {
    expect(routeOf("/products/42", { id: "42" })).toBe("/products/[id]");
    expect(routeOf("/blog/hello%20world/comments", { slug: "hello world" })).toBe("/blog/[slug]/comments");
    expect(routeOf("/docs/a/b/c", { path: ["a", "b", "c"] })).toBe("/docs/[...path]");
    expect(routeOf("/pricing", {})).toBe("/pricing");
    expect(routeOf("", null)).toBe("/");
  });
});

describe("VitalsQueue", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  test("sends a page's metrics in one beacon, under the path they belong to", async () => {
    const sent: { url: string; body: unknown }[] = [];
    globalThis.fetch = (async (url: string, init: RequestInit) => {
      sent.push({ url, body: JSON.parse(String(init.body)) });
      return new Response(null, { status: 204 });
    }) as unknown as typeof fetch;
    const q = new VitalsQueue("/_tiffin/vitals", "/products/[id]");
    q.add("LCP", 1840.04);
    q.add("CLS", 0.012345);
    q.add("FID", 12); // not one of ours
    q.add("INP", Number.NaN);
    q.setPath("/products/[id]"); // same route: nothing sent
    expect(sent).toHaveLength(0);
    q.setPath("/cart"); // a navigation sends what the last page had
    q.add("INP", 96);
    q.flush();
    q.flush(); // nothing new
    expect(sent).toEqual([
      { url: "/_tiffin/vitals", body: { path: "/products/[id]", metrics: { LCP: 1840, CLS: 0.0123 } } },
      { url: "/_tiffin/vitals", body: { path: "/cart", metrics: { INP: 96 } } },
    ]);
  });

  test("sends nothing when the browser sends Global Privacy Control", () => {
    let sent = 0;
    globalThis.fetch = (async () => {
      sent++;
      return new Response(null, { status: 204 });
    }) as unknown as typeof fetch;
    const nav = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    Object.defineProperty(globalThis, "navigator", { value: { globalPrivacyControl: true }, configurable: true });
    try {
      const q = new VitalsQueue("/_tiffin/vitals", "/");
      q.add("LCP", 1000);
      q.flush();
    } finally {
      if (nav) Object.defineProperty(globalThis, "navigator", nav);
      else delete (globalThis as { navigator?: unknown }).navigator;
    }
    expect(sent).toBe(0);
  });

  test("reportWebVitals does nothing outside a browser", () => {
    const stop = reportWebVitals();
    expect(typeof stop).toBe("function");
    stop();
  });
});
