import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, utimesSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname } from "node:path";
import { createUseCacheHandler, memoryRedis, RespClient, TiffinCacheHandler, toRedisLike, type RedisLike, type StoreOptions, type TagState } from "../src/next";
import { startFakeValkey, type FakeValkey } from "./fake-valkey";

// Each "instance" of an app is its own connection (and so its own in-memory
// copy and tag state) to one shared Valkey: a real one when REDIS_URL is set,
// else a stand-in that speaks the protocol and logs the commands it gets.
let fake: FakeValkey;
const clients: RespClient[] = [];
beforeAll(async () => {
  fake = await startFakeValkey();
});
afterAll(async () => {
  for (const c of clients) c.close();
  await fake.close();
});

function instance(url = process.env.REDIS_URL ?? fake.url): RedisLike {
  const c = new RespClient(url, 500);
  clients.push(c);
  return c as unknown as RedisLike;
}

let n = 0;
/** Options for one app: its own key prefix, Next.js's tag state stubbed per instance. */
function app(extra: Partial<StoreOptions> = {}) {
  const prefix = `t${Date.now()}-${n++}:`;
  return (client: RedisLike, more: Partial<StoreOptions> = {}): StoreOptions => ({
    client,
    prefix,
    buildId: "d1",
    tagsManifest: new Map<string, TagState>(),
    ...extra,
    ...more,
  });
}

// A request: Next.js makes a fresh handler for each.
const req = (o: StoreOptions) => new TiffinCacheHandler({}, o);
const page = (html: string, tags = "") => ({
  kind: "APP_PAGE",
  html,
  rscData: Buffer.from(`rsc:${html}`),
  segmentData: new Map([["/_tree", Buffer.from("seg")]]),
  headers: tags ? { "x-next-cache-tags": tags } : {},
  status: 200,
});
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const since = (mark: number) => fake.log.slice(mark);

describe("cacheHandler (ISR, route handlers, fetch)", () => {
  test("round-trips bytes, Maps and long strings, shared by instances", async () => {
    const o = app();
    const a = instance();
    const b = instance();
    const bin = Buffer.from([0, 255, 254, 10, 13, 0x80, 0xc3]);
    const html = "<h1>" + "é".repeat(3000) + "</h1>";
    const value = { ...page(html), rscData: bin };
    await req(o(a)).set("/blog", value, { cacheControl: { revalidate: 60, expire: 3600 } });
    const got = await req(o(b)).get("/blog", { kind: "APP_PAGE" });
    const v = got!.value as typeof value;
    expect(v.html).toBe(html);
    expect(Buffer.compare(v.rscData, bin)).toBe(0);
    expect(v.segmentData).toBeInstanceOf(Map);
    expect(v.segmentData.get("/_tree")!.toString()).toBe("seg");
    expect(v.headers).toEqual({});
  });

  test("entries are per deploy and per environment; a rollback finds its own again", async () => {
    const o = app();
    const v1 = instance();
    await req(o(v1, { buildId: "d1" })).set("/", page("release 1"), {});
    // Next.js gives every build the same BUILD_ID once deploymentId is set; the deploy keys entries.
    expect(await req(o(instance(), { buildId: "d2" })).get("/", { kind: "APP_PAGE" })).toBeNull();
    expect(await req(o(instance(), { buildId: "d1", environment: "pr-7" })).get("/", { kind: "APP_PAGE" })).toBeNull();
    const back = await req(o(instance(), { buildId: "d1" })).get("/", { kind: "APP_PAGE" });
    expect((back!.value as { html: string }).html).toBe("release 1");
  });

  test("with deploymentId every build has one BUILD_ID: entries follow TIFFIN_DEPLOY and TIFFIN_PREVIEW", async () => {
    const dist = mkdtempSync(`${tmpdir()}/next-`);
    writeFileSync(`${dist}/BUILD_ID`, "build-TfctsWXpff2fKS"); // what Next.js writes when deploymentId is set
    const saved = { deploy: process.env.TIFFIN_DEPLOY, preview: process.env.TIFFIN_PREVIEW };
    const prefix = `t${Date.now()}-env:`;
    const at = (deploy: string, preview?: string) => {
      process.env.TIFFIN_DEPLOY = deploy;
      if (preview) process.env.TIFFIN_PREVIEW = preview;
      else delete process.env.TIFFIN_PREVIEW;
      return new TiffinCacheHandler({ serverDistDir: `${dist}/server` }, { client: instance(), prefix, tagsManifest: new Map() });
    };
    try {
      await at("d1").set("/", page("release 1"), {});
      expect(await at("d2").get("/", { kind: "APP_PAGE" })).toBeNull();
      expect(await at("d1", "pr-3").get("/", { kind: "APP_PAGE" })).toBeNull();
      expect(await at("d1").get("/", { kind: "APP_PAGE" })).not.toBeNull();
    } finally {
      for (const [k, v] of [["TIFFIN_DEPLOY", saved.deploy], ["TIFFIN_PREVIEW", saved.preview]] as const) {
        if (v === undefined) delete process.env[k];
        else process.env[k] = v;
      }
    }
  });

  test("a preview's revalidations do not touch production", async () => {
    const o = app();
    const prod = instance();
    await req(o(prod)).set("/", page("prod", "posts"), {});
    await sleep(2);
    await req(o(instance(), { environment: "pr-7" })).revalidateTag("posts");
    expect(await req(o(prod)).get("/", { kind: "APP_PAGE" })).not.toBeNull();
  });

  test("revalidateTag reaches pages and route handlers through their x-next-cache-tags", async () => {
    const o = app();
    const a = instance();
    const b = instance();
    await req(o(a)).set("/posts", page("posts", "_N_T_/layout,_N_T_/posts/page,_N_T_/posts,posts"), {});
    await req(o(a)).set("/api/feed", { kind: "APP_ROUTE", body: Buffer.from("[]"), status: 200, headers: { "x-next-cache-tags": "_N_T_/api/feed/route,feed" } }, {});
    await req(o(a)).set("/about", page("about", "_N_T_/layout,_N_T_/about/page,_N_T_/about"), {});
    // Warm a's in-memory copies: they must not outlive the revalidation.
    expect(await req(o(a)).get("/posts", { kind: "APP_PAGE" })).not.toBeNull();
    expect(await req(o(a)).get("/api/feed", { kind: "APP_ROUTE" })).not.toBeNull();
    await sleep(2);
    await req(o(b)).revalidateTag(["posts", "feed"]);
    expect(await req(o(a)).get("/posts", { kind: "APP_PAGE" })).toBeNull();
    expect(await req(o(a)).get("/api/feed", { kind: "APP_ROUTE" })).toBeNull();
    expect(await req(o(a)).get("/about", { kind: "APP_PAGE" })).not.toBeNull();
    // A page rendered after the revalidation is a hit again.
    await sleep(2);
    await req(o(a)).set("/posts", page("posts 2", "posts"), {});
    expect((await req(o(b)).get("/posts", { kind: "APP_PAGE" }))!.value).toMatchObject({ html: "posts 2" });
  });

  test("revalidatePath reaches ISR pages (implicit _N_T_ tags) on every instance", async () => {
    const o = app();
    const a = instance();
    const b = instance();
    await req(o(a)).set("/blog/x", page("x", "_N_T_/layout,_N_T_/blog/layout,_N_T_/blog/[slug]/page,_N_T_/blog/x"), {});
    expect(await req(o(b)).get("/blog/x", { kind: "APP_PAGE" })).not.toBeNull();
    await sleep(2);
    await req(o(a)).revalidateTag(["_N_T_/blog/x"]); // revalidatePath("/blog/x")
    expect(await req(o(a)).get("/blog/x", { kind: "APP_PAGE" })).toBeNull();
    expect(await req(o(b)).get("/blog/x", { kind: "APP_PAGE" })).toBeNull();
  });

  test("fetch / unstable_cache entries use ctx tags and soft tags", async () => {
    const o = app();
    const a = instance();
    const fetched = { kind: "FETCH", data: { headers: {}, body: "e30=", status: 200, url: "" }, revalidate: 60, tags: ["time"] };
    await req(o(a)).set("k", fetched, { fetchCache: true, tags: ["time"], revalidate: 60 });
    expect(await req(o(a)).get("k", { kind: "FETCH", tags: ["time"], softTags: ["_N_T_/"] })).not.toBeNull();
    await sleep(2);
    await req(o(instance())).revalidateTag("_N_T_/");
    expect(await req(o(a)).get("k", { kind: "FETCH", tags: ["time"], softTags: ["_N_T_/"] })).toBeNull();
  });

  test('revalidateTag(tag, "max") marks stale: the entry is still served and Next.js is told', async () => {
    const o = app();
    const manifest = new Map<string, TagState>();
    const a = instance();
    await req(o(a, { tagsManifest: manifest })).set("/p", page("p", "posts"), {});
    await sleep(2);
    await req(o(instance())).revalidateTag("posts", { expire: 365 * 24 * 3600 }); // "max"
    const got = await req(o(a, { tagsManifest: manifest })).get("/p", { kind: "APP_PAGE" });
    expect(got).not.toBeNull();
    // Next.js's areTagsStale(tags, lastModified): it serves this once and regenerates.
    expect(manifest.get("posts")!.stale!).toBeGreaterThan(got!.lastModified);
    expect(manifest.get("posts")!.expired!).toBeGreaterThan(Date.now());
    // Without Next.js's tag state to tell, a stale tag regenerates now.
    expect(await req(o(instance(), { tagsManifest: null })).get("/p", { kind: "APP_PAGE" })).toBeNull();
  });

  test("a stale tag with a short expire becomes a miss once it expires", async () => {
    const o = app();
    const a = instance();
    await req(o(a)).set("/p", page("p", "posts"), {});
    await sleep(2);
    await req(o(a)).revalidateTag("posts", { expire: 0.05 });
    expect(await req(o(a)).get("/p", { kind: "APP_PAGE" })).not.toBeNull();
    await sleep(70);
    expect(await req(o(a)).get("/p", { kind: "APP_PAGE" })).toBeNull();
  });

  test("a broken or silent Valkey is a miss, not an error", async () => {
    const broken: RedisLike = { send: async () => Promise.reject(new Error("ECONNREFUSED")) };
    const h = new TiffinCacheHandler({}, { client: broken, prefix: "x:", buildId: "b" });
    expect(await h.get("/")).toBeNull();
    await h.set("/", page("x"), {}); // must not throw
    await h.revalidateTag("x"); // must not throw

    const refused = new TiffinCacheHandler({}, { client: instance("redis://127.0.0.1:1") as RedisLike, prefix: "x:", buildId: "b" });
    expect(await refused.get("/")).toBeNull();

    const o = app();
    const quiet = instance(fake.url);
    fake.mute = true;
    try {
      const t = Date.now();
      expect(await req(o(quiet)).get("/", { kind: "APP_PAGE" })).toBeNull();
      expect(Date.now() - t).toBeLessThan(2000);
    } finally {
      fake.mute = false;
    }
  });
});

describe("pages prerendered by next build", () => {
  // As `next build` writes them with an adapter: the scoped cache key's path under .next/server.
  const key = "/route-cache/APP_PAGE/0f0f/$/blog/hello";
  const { nodeFs } = createRequire(import.meta.url)("next/dist/server/lib/node-fs-methods.js") as { nodeFs: unknown };
  const built = (tags: string) => {
    const dist = mkdtempSync(`${tmpdir()}/next-`) + "/server";
    const base = `${dist}${key}`;
    mkdirSync(dirname(base), { recursive: true });
    writeFileSync(`${base}.html`, "<h1>from the build</h1>");
    writeFileSync(`${base}.rsc`, "rsc");
    writeFileSync(`${base}.meta`, JSON.stringify({ headers: { "x-next-cache-tags": tags }, status: 200 }));
    const at = new Date(Date.now() - 60_000);
    utimesSync(`${base}.html`, at, at);
    return { serverDistDir: dist, fs: nodeFs, at: at.getTime() };
  };
  const html = (e: Awaited<ReturnType<TiffinCacheHandler["get"]>>) => (e?.value as { html?: string } | undefined)?.html;

  test("a miss serves the build's file with the build's time, and copies it to Valkey for every instance", async () => {
    const o = app();
    const files = built("_N_T_/blog/hello,posts");
    const got = await new TiffinCacheHandler(files, o(instance())).get(key, { kind: "APP_PAGE" });
    expect(html(got)).toBe("<h1>from the build</h1>");
    expect(Buffer.from((got!.value as { rscData: Uint8Array }).rscData).toString()).toBe("rsc");
    expect(got!.lastModified).toBe(files.at);
    // Another instance without the files finds it in Valkey, still with the build's time.
    const other = await req(o(instance())).get(key, { kind: "APP_PAGE" });
    expect(html(other)).toBe("<h1>from the build</h1>");
    expect(other!.lastModified).toBe(files.at);
    // fetch entries are never prerendered files.
    expect(await new TiffinCacheHandler(files, app()(instance())).get(key, { kind: "FETCH" })).toBeNull();
  });

  test("a rendering stored since wins, and a tag revalidated since the build makes it a miss", async () => {
    const o = app();
    const files = built("_N_T_/blog/hello,posts");
    await req(o(instance())).set(key, page("rendered"), {});
    expect(html(await new TiffinCacheHandler(files, o(instance())).get(key, { kind: "APP_PAGE" }))).toBe("rendered");

    const p = app();
    await req(p(instance())).revalidateTag("posts");
    expect(await new TiffinCacheHandler(files, p(instance())).get(key, { kind: "APP_PAGE" })).toBeNull();
    expect(await req(p(instance())).get(key, { kind: "APP_PAGE" })).toBeNull(); // nothing copied
  });
});

describe("optimized images (kind IMAGE)", () => {
  // Next.js's own image cache, which calls the handler when images.customCacheHandler is on.
  const { ImageOptimizerCache } = createRequire(import.meta.url)("next/dist/server/image-optimizer.js") as {
    ImageOptimizerCache: new (o: Record<string, unknown>) => {
      get(key: string): Promise<{ value: { buffer: Buffer; etag: string; extension: string }; isStale: boolean } | null>;
      set(key: string, v: Record<string, unknown>, o: { cacheControl: { revalidate: number } }): Promise<void>;
    };
  };
  const nextConfig = (custom: boolean, maxBytes?: number) => ({
    images: { minimumCacheTTL: 60, customCacheHandler: custom, maximumDiskCacheSize: maxBytes },
    experimental: { isrFlushToDisk: true },
  });
  const distDir = (maxBytes?: number) => {
    const d = mkdtempSync(`${tmpdir()}/next-img-`);
    mkdirSync(`${d}/server`);
    if (maxBytes !== undefined) writeFileSync(`${d}/required-server-files.json`, JSON.stringify({ config: { images: { maximumDiskCacheSize: maxBytes } } }));
    return d;
  };
  const image = (i: number, bytes = 50_000) => ({
    kind: "IMAGE",
    etag: `etag${i}`,
    upstreamEtag: `up${i}`,
    extension: "webp",
    buffer: Buffer.alloc(bytes, i % 256),
  });
  const key = (i: number) => `k${i}_${"x".repeat(40)}`;

  test("go to the build's .next/cache/images in Next.js's layout, never to Valkey, and survive a deploy", async () => {
    const dir = distDir(512 << 20);
    const o = app();
    const mark = fake.log.length;
    const via = new ImageOptimizerCache({ distDir: dir, nextConfig: nextConfig(true), cacheHandler: new TiffinCacheHandler({ serverDistDir: `${dir}/server` }, o(instance(fake.url))) });
    for (let i = 0; i < 100; i++) await via.set(key(i), image(i), { cacheControl: { revalidate: 60 } });
    // 100 images of 50 KB: not one command reached Valkey.
    expect(since(mark)).toEqual([]);
    const got = await via.get(key(7));
    expect(got!.isStale).toBe(false);
    expect(got!.value.etag).toBe("etag7");
    expect(Buffer.compare(got!.value.buffer, image(7).buffer)).toBe(0);
    // The next deploy (another key namespace in Valkey) finds them on the shared disk.
    const next = new TiffinCacheHandler({ serverDistDir: `${dir}/server` }, o(instance(fake.url), { buildId: "d2" }));
    expect((await next.get(key(42), { kind: "IMAGE" }))!.value).toMatchObject({ kind: "IMAGE", etag: "etag42", extension: "webp" });
    // Next.js's own disk cache (no customCacheHandler) reads the same entries, and we read its.
    const own = new ImageOptimizerCache({ distDir: dir, nextConfig: nextConfig(false, 512 << 20) });
    expect((await own.get(key(3)))!.value.etag).toBe("etag3");
    await own.set("fromnext", image(200), { cacheControl: { revalidate: 60 } });
    expect((await next.get("fromnext", { kind: "IMAGE" }))!.value).toMatchObject({ etag: "etag200" });
    expect(since(mark).filter((c) => !c.startsWith("GET") && !c.startsWith("MGET"))).toEqual([]);
  });

  test("bounded by images.maximumDiskCacheSize, least recently used out first; a delete removes the file", async () => {
    const dir = distDir(150_000); // three images
    const h = () => new TiffinCacheHandler({ serverDistDir: `${dir}/server` }, app()(memoryRedis()));
    for (let i = 0; i < 3; i++) await h().set(key(i), image(i), { kind: "IMAGE", cacheControl: { revalidate: 60 } });
    await h().get(key(0), { kind: "IMAGE" }); // used: 1 is now the oldest
    for (let i = 3; i < 5; i++) await h().set(key(i), image(i), { kind: "IMAGE", cacheControl: { revalidate: 60 } });
    const kept = await Promise.all([0, 1, 2, 3, 4].map(async (i) => (await h().get(key(i), { kind: "IMAGE" })) !== null));
    expect(kept).toEqual([true, false, false, true, true]);
    await h().set(key(3), null, { kind: "IMAGE" });
    expect(await h().get(key(3), { kind: "IMAGE" })).toBeNull();
    // Keys and names that are not Next.js's are refused rather than written outside the directory.
    await h().set("../escape", image(9), { kind: "IMAGE", cacheControl: { revalidate: 60 } });
    expect(await h().get("../escape", { kind: "IMAGE" })).toBeNull();
    // maximumDiskCacheSize 0: nothing kept, as in Next.js.
    const off = distDir(0);
    await new TiffinCacheHandler({ serverDistDir: `${off}/server` }, app()(memoryRedis())).set(key(1), image(1), { kind: "IMAGE", cacheControl: { revalidate: 60 } });
    expect(await new TiffinCacheHandler({ serverDistDir: `${off}/server` }, app()(memoryRedis())).get(key(1), { kind: "IMAGE" })).toBeNull();
  });
});

describe("hot path", () => {
  test("a repeat hit costs one small GET (the tag counter); the tag hash is read only when it moved", async () => {
    const o = app();
    const a = instance(fake.url);
    await req(o(a)).set("/", page("home", "_N_T_/,home"), { cacheControl: { revalidate: 3600 } });
    await req(o(a)).get("/", { kind: "APP_PAGE" });
    let mark = fake.log.length;
    // One request with two lookups.
    const r = req(o(a));
    expect(await r.get("/", { kind: "APP_PAGE" })).not.toBeNull();
    expect(await r.get("/", { kind: "APP_PAGE" })).not.toBeNull();
    expect(since(mark)).toEqual([`GET ${o(a).prefix}prod:tagv`]);

    await sleep(2);
    await req(o(instance(fake.url))).revalidateTag("other");
    mark = fake.log.length;
    expect(await req(o(a)).get("/", { kind: "APP_PAGE" })).not.toBeNull();
    expect(since(mark)).toEqual([`GET ${o(a).prefix}prod:tagv`, `HGETALL ${o(a).prefix}prod:tags`]);
  });

  test("concurrent misses read Valkey once", async () => {
    const o = app();
    await req(o(instance(fake.url))).set("/x", page("x"), {});
    const a = instance(fake.url);
    const mark = fake.log.length;
    const all = await Promise.all(Array.from({ length: 10 }, () => req(o(a)).get("/x", { kind: "APP_PAGE" })));
    expect(all.every((e) => e !== null)).toBe(true);
    expect(since(mark).filter((c) => c.endsWith(":e:/x"))).toHaveLength(1);
  });

  test("the in-memory copy is bounded by bytes, and past revalidate it asks Valkey again", async () => {
    const o = app({ memoryBytes: 8 << 10 });
    const w = instance(fake.url);
    for (let i = 0; i < 20; i++) await req(o(w)).set(`/p${i}`, page("x".repeat(500) + i), { cacheControl: { revalidate: 3600 } });
    const a = instance(fake.url);
    for (let i = 0; i < 20; i++) await req(o(a)).get(`/p${i}`, { kind: "APP_PAGE" });
    let mark = fake.log.length;
    await req(o(a)).get("/p19", { kind: "APP_PAGE" }); // recent: in memory
    await req(o(a)).get("/p0", { kind: "APP_PAGE" }); // evicted: from Valkey
    expect(since(mark).filter((c) => c.includes(":e:"))).toEqual([`GET ${o(a).prefix}prod:d1:e:/p0`]);

    await req(o(w)).set("/short", page("s"), { cacheControl: { revalidate: 0.05 } });
    await req(o(a)).get("/short", { kind: "APP_PAGE" });
    await sleep(70);
    mark = fake.log.length;
    await req(o(a)).get("/short", { kind: "APP_PAGE" });
    expect(since(mark).filter((c) => c.includes(":e:"))).toHaveLength(1);
  });

  test("tag fields older than the longest entry TTL are pruned", async () => {
    const o = app({ maxTtlSeconds: 60 });
    const a = instance(fake.url);
    const c = a as unknown as RespClient;
    const prefix = o(a).prefix!;
    await c.send("HSET", [`${prefix}prod:tags`, "x:old", String(Date.now() - 120_000), "x:new", String(Date.now())]);
    await c.send("INCR", [`${prefix}prod:tagv`]);
    await req(o(a)).get("/", { kind: "APP_PAGE" });
    await sleep(10);
    const left = (await c.send("HGETALL", [`${prefix}prod:tags`])) as Buffer[];
    expect(left.map(String)).toEqual(["x:new", expect.any(String)]);
  });
});

describe('"use cache" handlers', () => {
  const entry = (text: string, tags: string[], over: Partial<{ revalidate: number; expire: number; timestamp: number }> = {}) => ({
    value: new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new TextEncoder().encode(text));
        c.close();
      },
    }),
    tags,
    stale: 30,
    timestamp: Date.now(),
    expire: 3600,
    revalidate: 60,
    ...over,
  });
  const read = async (s: ReadableStream<Uint8Array>) => new Response(s).text();

  test("stores streams; updateTag is read-your-writes here and reaches other instances", async () => {
    const o = app();
    const a = createUseCacheHandler(o(instance()));
    const b = createUseCacheHandler(o(instance()));
    await a.set("k1", Promise.resolve(entry("hello", ["note"])));
    await b.refreshTags();
    const got = await b.get("k1", []);
    expect(await read(got!.value)).toBe("hello");
    expect(got!.tags).toEqual(["note"]);
    expect(got!.revalidate).toBe(60);

    await sleep(2);
    await a.updateTags(["note"]); // updateTag("note") in a Server Action
    expect(await a.get("k1", [])).toBeUndefined(); // no refresh needed on the instance that wrote
    await b.refreshTags();
    expect(await b.get("k1", [])).toBeUndefined();
    // The re-render's entry is a hit everywhere.
    await sleep(2);
    await a.set("k1", Promise.resolve(entry("hello 2", ["note"])));
    expect(await read((await b.get("k1", []))!.value)).toBe("hello 2");
  });

  test("implicit (path) tags are checked in get, so getExpiration defers to it", async () => {
    const o = app();
    const a = createUseCacheHandler(o(instance()));
    await a.set("k", Promise.resolve(entry("v", [])));
    expect(await a.getExpiration(["_N_T_/"])).toBe(Infinity);
    await sleep(2);
    await createUseCacheHandler(o(instance())).updateTags(["_N_T_/"]); // revalidatePath("/")
    await a.refreshTags();
    expect(await a.get("k", ["_N_T_/layout", "_N_T_/"])).toBeUndefined();
  });

  test('a stale tag ("max") serves the entry with revalidate -1 so Next.js refreshes it in the background', async () => {
    const o = app();
    const a = createUseCacheHandler(o(instance()));
    await a.set("k", Promise.resolve(entry("v", ["posts"])));
    await sleep(2);
    await a.updateTags(["posts"], { expire: 365 * 24 * 3600 });
    const got = await a.get("k", []);
    expect(got!.revalidate).toBe(-1);
    expect(await read(got!.value)).toBe("v");
  });

  test("past revalidate an entry is still served (stale-while-revalidate); past expire it is a miss", async () => {
    const h = createUseCacheHandler(app()(instance()));
    await h.set("old", Promise.resolve(entry("old", [], { revalidate: 1, expire: 3600, timestamp: Date.now() - 5000 })));
    expect(await h.get("old", [])).toBeDefined();
    await h.set("gone", Promise.resolve(entry("gone", [], { revalidate: 1, expire: 2, timestamp: Date.now() - 5000 })));
    expect(await h.get("gone", [])).toBeUndefined();
  });

  test("a failed render is not stored", async () => {
    const h = createUseCacheHandler(app()(instance()));
    await h.set("k", Promise.reject(new Error("render failed")));
    expect(await h.get("k", [])).toBeUndefined();
  });
});

describe("clients", () => {
  test("string-only clients (ioredis, node-redis, Bun's RedisClient) get base64 entries", async () => {
    const mem = memoryRedis();
    const stringOnly: RedisLike = { send: (cmd, args) => mem.send(cmd, args) };
    const o = app();
    await req(o(stringOnly)).set("/", page("hi"), {});
    const got = await req(o({ send: (cmd, args) => mem.send(cmd, args) })).get("/", { kind: "APP_PAGE" });
    expect((got!.value as { rscData: Buffer }).rscData.toString()).toBe("rsc:hi");
  });

  test("toRedisLike adapts ioredis and node-redis shapes", async () => {
    const calls: string[][] = [];
    const ioredis = { call: async (...a: string[]) => (calls.push(a), "OK") };
    const nodeRedis = { sendCommand: async (a: string[]) => (calls.push(a), "OK") };
    await toRedisLike(ioredis).send("GET", ["k"]);
    await toRedisLike(nodeRedis).send("DEL", ["k"]);
    expect(calls).toEqual([["GET", "k"], ["DEL", "k"]]);
    expect(toRedisLike(ioredis)).toBe(toRedisLike(ioredis)); // one Store per client, not per request
    expect(() => toRedisLike({})).toThrow();
  });

  test("RespClient: unix socket URLs, AUTH from the URL, pipelined replies in order", async () => {
    const c = instance(fake.url) as unknown as RespClient;
    const mark = fake.log.length;
    const rs = await c.pipeline([["SET", "p:a", Buffer.from([1, 2, 3])], ["GET", "p:a"], ["HGETALL", "p:none"], ["BOGUS"]]);
    expect(String(rs[0])).toBe("OK");
    expect([...(rs[1] as Buffer)]).toEqual([1, 2, 3]);
    expect(rs[2]).toEqual([]);
    expect(rs[3]).toBeInstanceOf(Error);
    expect(fake.log.slice(mark)).toEqual(["AUTH user", "SET p:a", "GET p:a", "HGETALL p:none", "BOGUS"]);
    const { target } = await import("../src/resp");
    expect(target("redis+unix://p_shop:a%2Fb%3Ac@/var/run/valkey/valkey.sock")).toMatchObject({ path: "/var/run/valkey/valkey.sock", user: "p_shop", pass: "a/b:c" });
  });

  test("Node: the handlers share the cache from a `node` process (no Bun)", async () => {
    const out = `${import.meta.dir}/../.node-test`;
    const built = await Bun.build({ entrypoints: [`${import.meta.dir}/../src/next/cache-handler.ts`], target: "node", format: "esm", outdir: out });
    expect(built.success).toBe(true);
    const o = app();
    const script = `
      const { default: H } = await import(${JSON.stringify(`${out}/cache-handler.js`)});
      const h = new H({}, { prefix: ${JSON.stringify(o(instance()).prefix)}, buildId: "d1" });
      await h.set("/from-node", { kind: "APP_PAGE", html: "node", rscData: Buffer.from([0, 255]), headers: {}, status: 200 }, {});
      console.log(typeof globalThis.Bun, h.store.client.constructor.name);
      process.exit(0);
    `;
    // Not spawnSync: the stand-in Valkey answers from this process.
    const p = Bun.spawn(["node", "--input-type=module", "-e", script], { env: { ...process.env, REDIS_URL: process.env.REDIS_URL ?? fake.url }, stdout: "pipe", stderr: "pipe" });
    const [stdout, stderr] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text(), p.exited]);
    await Bun.$`rm -rf ${out}`;
    expect(stderr).toBe("");
    expect(stdout.trim()).toBe("undefined RespClient");
    const got = await req(o(instance())).get("/from-node", { kind: "APP_PAGE" });
    expect([...(got!.value as { rscData: Buffer }).rscData]).toEqual([0, 255]);
  });
});
