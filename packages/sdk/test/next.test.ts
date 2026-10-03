import { describe, expect, test } from "bun:test";
import { createUseCacheHandler, memoryRedis, TiffinCacheHandler, toRedisLike, type RedisLike } from "tiffin-sdk/next";

// Two "instances" share one Valkey: a real one when REDIS_URL is set, else an
// in-memory stand-in with the same commands.
function sharedClient(): RedisLike {
  const url = process.env.REDIS_URL;
  if (url) return new Bun.RedisClient(url) as unknown as RedisLike;
  return memoryRedis();
}

const opts = (client: RedisLike, prefix: string) => ({ client, prefix, buildId: "b1" });

describe("cacheHandler (ISR, route handlers, fetch)", () => {
  test("round-trips Buffers and Maps, shares entries across instances", async () => {
    const client = sharedClient();
    const prefix = `t${Date.now()}:`;
    const a = new TiffinCacheHandler({}, opts(client, prefix));
    const b = new TiffinCacheHandler({}, opts(client, prefix));
    const value = {
      kind: "APP_PAGE",
      html: "<h1>hi</h1>",
      rscData: Buffer.from("rsc-bytes"),
      segmentData: new Map([["/page", Buffer.from("seg")]]),
      headers: { "x-next-cache-tags": "posts" },
      status: 200,
    };
    await a.set("/blog", value, { tags: ["posts"] });
    const got = await b.get("/blog", { kind: "APP_PAGE" });
    expect(got).not.toBeNull();
    const v = got!.value as typeof value;
    expect(v.html).toBe("<h1>hi</h1>");
    expect(Buffer.isBuffer(v.rscData)).toBe(true);
    expect(v.rscData.toString()).toBe("rsc-bytes");
    expect(v.segmentData instanceof Map).toBe(true);
    expect(v.segmentData.get("/page")!.toString()).toBe("seg");
  });

  test("revalidateTag on one instance invalidates on the other", async () => {
    const client = sharedClient();
    const prefix = `t${Date.now()}r:`;
    const a = new TiffinCacheHandler({}, opts(client, prefix));
    const b = new TiffinCacheHandler({}, opts(client, prefix));
    await a.set("/posts", { kind: "APP_ROUTE", body: Buffer.from("[]"), status: 200 }, { tags: ["posts"] });
    await a.set("/about", { kind: "APP_ROUTE", body: Buffer.from("about"), status: 200 }, { tags: ["pages"] });
    expect(await b.get("/posts")).not.toBeNull();
    await Bun.sleep(2);
    await b.revalidateTag("posts");
    expect(await a.get("/posts")).toBeNull();
    expect(await a.get("/about")).not.toBeNull();
    // A fresh entry after the revalidation is a hit again.
    await Bun.sleep(2);
    await a.set("/posts", { kind: "APP_ROUTE", body: Buffer.from("[1]"), status: 200 }, { tags: ["posts"] });
    expect(await b.get("/posts")).not.toBeNull();
  });

  test("revalidatePath works through soft tags", async () => {
    const client = sharedClient();
    const prefix = `t${Date.now()}s:`;
    const a = new TiffinCacheHandler({}, opts(client, prefix));
    await a.set("/blog/x", { kind: "APP_PAGE", html: "x" }, { tags: [] });
    expect(await a.get("/blog/x", { softTags: ["_N_T_/blog/x"] })).not.toBeNull();
    await Bun.sleep(2);
    await a.revalidateTag(["_N_T_/blog/x"]);
    expect(await a.get("/blog/x", { softTags: ["_N_T_/blog/x"] })).toBeNull();
  });

  test("builds do not see each other's entries", async () => {
    const client = sharedClient();
    const prefix = `t${Date.now()}b:`;
    const old = new TiffinCacheHandler({}, { client, prefix, buildId: "old" });
    const cur = new TiffinCacheHandler({}, { client, prefix, buildId: "new" });
    await old.set("/", { kind: "APP_PAGE", html: "old" }, {});
    expect(await cur.get("/")).toBeNull();
  });

  test("a broken client is a miss, not an error", async () => {
    const broken: RedisLike = { send: async () => Promise.reject(new Error("ECONNREFUSED")) };
    const h = new TiffinCacheHandler({}, { client: broken, prefix: "x:", buildId: "b" });
    expect(await h.get("/")).toBeNull();
    await h.set("/", { kind: "APP_PAGE", html: "x" }, {}); // must not throw
  });
});

describe('"use cache" handlers', () => {
  const entry = (text: string, tags: string[], revalidate = 60) => ({
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
    revalidate,
  });
  const read = async (s: ReadableStream<Uint8Array>) => new Response(s).text();

  test("stores streams and shares tag invalidation across instances", async () => {
    const client = sharedClient();
    const prefix = `u${Date.now()}:`;
    const a = createUseCacheHandler(opts(client, prefix));
    const b = createUseCacheHandler(opts(client, prefix));
    await a.set("k1", Promise.resolve(entry("hello", ["posts"])));
    const got = await b.get("k1", []);
    expect(got).toBeDefined();
    expect(await read(got!.value)).toBe("hello");
    expect(got!.tags).toEqual(["posts"]);

    await Bun.sleep(2);
    await a.updateTags(["posts"]);
    await b.refreshTags();
    expect(await b.getExpiration(["posts"])).toBeGreaterThan(0);
    expect(await b.get("k1", [])).toBeUndefined();
  });

  test("expired entries are misses", async () => {
    const h = createUseCacheHandler(opts(memoryRedis(), "u:"));
    const e = entry("old", [], 1);
    e.timestamp = Date.now() - 5000;
    await h.set("k", Promise.resolve(e));
    expect(await h.get("k", [])).toBeUndefined();
  });

  test("a failed render is not stored", async () => {
    const h = createUseCacheHandler(opts(memoryRedis(), "u:"));
    await h.set("k", Promise.reject(new Error("render failed")));
    expect(await h.get("k", [])).toBeUndefined();
  });
});

describe("toRedisLike", () => {
  test("adapts ioredis and node-redis shapes", async () => {
    const calls: string[][] = [];
    const ioredis = { call: async (...a: string[]) => (calls.push(a), "OK") };
    const nodeRedis = { sendCommand: async (a: string[]) => (calls.push(a), "OK") };
    await toRedisLike(ioredis).send("GET", ["k"]);
    await toRedisLike(nodeRedis).send("DEL", ["k"]);
    expect(calls).toEqual([["GET", "k"], ["DEL", "k"]]);
    expect(() => toRedisLike({})).toThrow();
  });
});
