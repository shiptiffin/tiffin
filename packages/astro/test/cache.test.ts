import { describe, expect, test } from "bun:test";
import { createValkeyCache, parseCdnCacheControl, queryKey, type CacheProvider } from "../src/cache-provider";
import { fakeStore, type FakeRedis } from "./fake-redis";
import type { Store } from "../src/store";

let clock = 1_000_000;
const now = () => clock;

function setup(config = {}, shared?: { fake: FakeRedis; store: Store }) {
  const s = shared ?? fakeStore("p_shop:");
  const cache = createValkeyCache(s.store, config, { scope: "web:prod:", now });
  return { ...s, cache };
}

/** A route: counts renders, sets cache headers like Astro.cache.set() does. */
function route(opts: { maxAge?: number; swr?: number; tags?: string[]; headers?: Record<string, string>; status?: number; body?: string | null } = {}) {
  const r = {
    renders: 0,
    next: async () => {
      r.renders++;
      const h = new Headers(opts.headers);
      if (opts.maxAge !== undefined) h.set("CDN-Cache-Control", `max-age=${opts.maxAge}${opts.swr ? `, stale-while-revalidate=${opts.swr}` : ""}`);
      if (opts.tags) h.set("Cache-Tag", opts.tags.join(", "));
      const body = opts.body === null ? null : (opts.body ?? `render ${r.renders}`);
      return new Response(body, { status: opts.status ?? 200, headers: h });
    },
  };
  return r;
}

async function get(cache: CacheProvider, next: () => Promise<Response>, url = "https://shop.example/p", init: RequestInit = {}) {
  const pending: Promise<unknown>[] = [];
  const request = new Request(url, init);
  const res = await cache.onRequest({ request, url: new URL(url), waitUntil: (p) => void pending.push(p) }, next);
  const text = await res.text();
  await Promise.all(pending);
  return { status: res.status, state: res.headers.get("x-astro-cache"), text, headers: res.headers };
}

describe("helpers", () => {
  test("query keys are sorted and drop tracking parameters", () => {
    expect(queryKey(new URL("https://x/p?b=2&utm_source=a&a=1&fbclid=z"))).toBe("?a=1&b=2");
    expect(queryKey(new URL("https://x/p?b=2&a=1"), { include: ["a"] })).toBe("?a=1");
    expect(queryKey(new URL("https://x/p?b=2&a=1"), { sort: false, exclude: [] })).toBe("?b=2&a=1");
  });
  test("CDN-Cache-Control", () => {
    expect(parseCdnCacheControl("max-age=60, stale-while-revalidate=30")).toEqual({ maxAge: 60, swr: 30 });
    expect(parseCdnCacheControl(null)).toEqual({ maxAge: 0, swr: 0 });
  });
});

describe("Valkey route cache", () => {
  test("MISS then HIT, stored under the box prefix with an expiry", async () => {
    const { cache, fake } = setup();
    const r = route({ maxAge: 60, tags: ["products"] });
    expect(await get(cache, r.next)).toMatchObject({ state: "MISS", text: "render 1" });
    expect(await get(cache, r.next)).toMatchObject({ state: "HIT", text: "render 1" });
    expect(r.renders).toBe(1);
    const keys = [...fake.data.keys()];
    expect(keys.every((k) => k.startsWith("p_shop:astro-cache:web:prod:"))).toBe(true);
    const e = [...fake.data.entries()].find(([k]) => k.includes(":e:"))!;
    expect(e[1].exp).toBeGreaterThan(0);
  });
  test("responses without cache directives pass through and store nothing", async () => {
    const { cache, fake } = setup();
    const r = route();
    expect((await get(cache, r.next)).state).toBeNull();
    expect(await get(cache, r.next)).toMatchObject({ text: "render 2" });
    expect([...fake.data.keys()].filter((k) => k.includes(":e:"))).toEqual([]);
  });
  test("expires after maxAge; serves stale within swr while one revalidation runs", async () => {
    const { cache } = setup();
    const r = route({ maxAge: 10, swr: 20 });
    await get(cache, r.next);
    clock += 11_000;
    const stale = await get(cache, r.next);
    expect(stale).toMatchObject({ state: "STALE", text: "render 1" });
    expect(r.renders).toBe(2); // background revalidation ran (awaited through waitUntil)
    expect(await get(cache, r.next)).toMatchObject({ state: "HIT", text: "render 2" });
    clock += 31_000;
    expect(await get(cache, r.next)).toMatchObject({ state: "MISS", text: "render 3" });
  });
  test("tag invalidation reaches another instance", async () => {
    const a = setup();
    const b = setup({}, { fake: a.fake, store: a.store }); // second instance, same Valkey
    const r = route({ maxAge: 600, tags: ["products", "home"] });
    await get(a.cache, r.next);
    expect((await get(b.cache, r.next)).state).toBe("HIT");
    clock += 1;
    await b.cache.invalidate({ tags: "products" });
    clock += 1;
    expect(await get(a.cache, r.next)).toMatchObject({ state: "MISS", text: "render 2" });
    clock += 1;
    expect((await get(b.cache, r.next)).state).toBe("HIT");
  });
  test("an unrelated tag leaves the entry alone", async () => {
    const { cache } = setup();
    const r = route({ maxAge: 600, tags: ["a"] });
    await get(cache, r.next);
    clock += 1;
    await cache.invalidate({ tags: ["b"] });
    expect((await get(cache, r.next)).state).toBe("HIT");
  });
  test("path invalidation is exact (path + normalised query), on every host", async () => {
    const { cache } = setup();
    const r = route({ maxAge: 600 });
    await get(cache, r.next, "https://shop.example/p");
    await get(cache, r.next, "https://www.shop.example/p");
    await get(cache, r.next, "https://shop.example/p?page=2");
    await get(cache, r.next, "https://shop.example/p/other");
    clock += 1;
    await cache.invalidate({ path: "/p" });
    expect((await get(cache, r.next, "https://shop.example/p")).state).toBe("MISS");
    expect((await get(cache, r.next, "https://www.shop.example/p")).state).toBe("MISS");
    expect((await get(cache, r.next, "https://shop.example/p?page=2")).state).toBe("HIT");
    expect((await get(cache, r.next, "https://shop.example/p/other")).state).toBe("HIT");
  });
  test("a render that started before an invalidation is not served after it", async () => {
    const { cache } = setup();
    let release!: () => void;
    const slow = async () => {
      await new Promise<void>((r) => (release = r));
      return new Response("old data", { headers: { "CDN-Cache-Control": "max-age=600", "Cache-Tag": "t" } });
    };
    const inflight = get(cache, slow); // render starts at `clock`
    await new Promise((r) => setTimeout(r, 5));
    await cache.invalidate({ tags: "t" }); // same instant: counts as after the render start
    clock += 5;
    release();
    expect((await inflight).state).toBe("MISS");
    const fresh = route({ maxAge: 600, tags: ["t"] });
    expect(await get(cache, fresh.next)).toMatchObject({ state: "MISS", text: "render 1" });
    expect((await get(cache, fresh.next)).state).toBe("HIT");
  });
  test("Vary headers key the entry per value", async () => {
    const { cache } = setup();
    let n = 0;
    const next = async () => new Response(`lang ${++n}`, { headers: { "CDN-Cache-Control": "max-age=60", Vary: "Accept-Language" } });
    const en = { headers: { "accept-language": "en" } };
    const fr = { headers: { "accept-language": "fr" } };
    expect((await get(cache, next, undefined, en)).text).toBe("lang 1");
    expect((await get(cache, next, undefined, fr)).text).toBe("lang 2");
    expect(await get(cache, next, undefined, en)).toMatchObject({ state: "HIT", text: "lang 1" });
    expect(await get(cache, next, undefined, fr)).toMatchObject({ state: "HIT", text: "lang 2" });
  });
  test("never stores Set-Cookie, Vary: Cookie, errors rerouted to the error page, or non-GET", async () => {
    const { cache, fake } = setup();
    await get(cache, route({ maxAge: 60, headers: { "set-cookie": "a=1" } }).next, "https://s/a");
    await get(cache, route({ maxAge: 60, headers: { vary: "Cookie" } }).next, "https://s/b");
    await get(cache, route({ maxAge: 60, status: 404, body: null }).next, "https://s/c");
    const post = route({ maxAge: 60 });
    await get(cache, post.next, "https://s/d", { method: "POST" });
    expect([...fake.data.keys()].filter((k) => k.includes(":e:"))).toEqual([]);
  });
  test("bodies over maxBodyBytes are served, not stored", async () => {
    const { cache, fake } = setup({ maxBodyBytes: 4 });
    const r = route({ maxAge: 60, body: "too long" });
    expect((await get(cache, r.next)).text).toBe("too long");
    expect([...fake.data.keys()].filter((k) => k.includes(":e:"))).toEqual([]);
  });
  test("binary bodies and headers survive", async () => {
    const { cache } = setup();
    const bytes = new Uint8Array([0, 255, 1, 254, 10, 13]);
    const next = async () => new Response(bytes, { headers: { "CDN-Cache-Control": "max-age=60", "content-type": "application/octet-stream", "x-a": "b" } });
    await get(cache, next);
    const pending: Promise<unknown>[] = [];
    const res = await cache.onRequest({ request: new Request("https://shop.example/p"), url: new URL("https://shop.example/p"), waitUntil: (p) => void pending.push(p) }, next);
    expect(res.headers.get("x-astro-cache")).toBe("HIT");
    expect(res.headers.get("x-a")).toBe("b");
    expect(new Uint8Array(await res.arrayBuffer())).toEqual(bytes);
  });
  test("Valkey down: a miss, never an error, then skipped for a while", async () => {
    const { cache, fake } = setup();
    const r = route({ maxAge: 60 });
    fake.down = true;
    expect(await get(cache, r.next)).toMatchObject({ status: 200, text: "render 1" });
    expect(await get(cache, r.next)).toMatchObject({ status: 200, text: "render 2" });
    const calls = fake.log.length;
    fake.down = false;
    await get(cache, r.next);
    expect(fake.log.length).toBe(calls); // still skipped (5 s)
    clock += 6000;
    await get(cache, r.next);
    expect(fake.log.length).toBeGreaterThan(calls);
  });
  test("a slow Valkey is a miss after timeoutMs", async () => {
    const { cache, fake } = setup({ timeoutMs: 20 });
    fake.delayMs = 200;
    const r = route({ maxAge: 60 });
    const t = performance.now();
    expect(await get(cache, r.next)).toMatchObject({ status: 200, text: "render 1" });
    expect(performance.now() - t).toBeLessThan(150);
  });
  test("invalidate() reports a Valkey failure", async () => {
    const { cache, fake } = setup();
    fake.down = true;
    await expect(cache.invalidate({ tags: "x" })).rejects.toThrow();
  });
  test("without a store: passes through, invalidate is a no-op", async () => {
    const cache = createValkeyCache(undefined, {}, { scope: "w:p:", now });
    const r = route({ maxAge: 60 });
    await get(cache, r.next);
    expect(await get(cache, r.next)).toMatchObject({ text: "render 2" });
    await cache.invalidate({ tags: "x" });
  });
  test("previews and production don't share entries", async () => {
    const prod = setup();
    const preview = { cache: createValkeyCache(prod.store, {}, { scope: "web:pr-7:", now }) };
    const r = route({ maxAge: 60 });
    await get(prod.cache, r.next);
    expect((await get(preview.cache, r.next)).state).toBe("MISS");
  });
  test("tag records and entries never outlive maxTtl", async () => {
    const { cache, fake } = setup({ maxTtl: 100 });
    await get(cache, route({ maxAge: 10_000, tags: ["t"] }).next);
    await cache.invalidate({ tags: "t" });
    for (const [k, v] of fake.data) if (k.includes(":e:") || k.includes(":t:")) expect(v.exp - Date.now()).toBeLessThanOrEqual(100_000);
  });
});
