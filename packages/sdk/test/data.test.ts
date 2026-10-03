import { describe, expect, test } from "bun:test";
import { Collection, Database, db, type SqlClient } from "../src/db";
import { KV, kv, type RedisLike } from "../src/kv";

// ---- unit tests: recorded queries, no database ----

class Recorder implements SqlClient {
  calls: { query: string; params: unknown[] }[] = [];
  constructor(private readonly reply: (q: string) => unknown[] = () => []) {}
  async unsafe(query: string, params: unknown[] = []) {
    this.calls.push({ query, params });
    return this.reply(query);
  }
}

const row = (id: string, data: object) => ({ id, data, created_at: new Date(0), updated_at: new Date(0) });

describe("db (unit)", () => {
  test("creates the schema once, then parameterises everything", async () => {
    const r = new Recorder((q) => (q.startsWith("INSERT") ? [row("x1", { title: "a'; drop table x; --" })] : []));
    const posts = new Database(r).collection<{ title: string }>("posts");
    const doc = await posts.insert({ title: "a'; drop table x; --" });
    await posts.find({ where: { title: "z" }, orderBy: "title", desc: true, limit: 5000 });
    expect(r.calls.filter((c) => c.query.includes("CREATE TABLE")).length).toBe(1);
    const ins = r.calls.find((c) => c.query.startsWith("INSERT"))!;
    expect(ins.query).not.toContain("drop table");
    expect(ins.params[0]).toBe("posts");
    expect(JSON.parse(ins.params[2] as string)).toEqual({ title: "a'; drop table x; --" });
    const find = r.calls.at(-1)!;
    expect(find.query).toContain("data @> $2::text::jsonb");
    expect(find.query).toContain("ORDER BY data->'title' DESC");
    expect(find.params).toEqual(["posts", '{"title":"z"}', 1000]); // limit capped
    expect(doc).toEqual({ title: "a'; drop table x; --", id: "x1", createdAt: new Date(0).toISOString(), updatedAt: new Date(0).toISOString() });
  });

  test("rejects unsafe names", () => {
    const d = new Database(new Recorder());
    expect(() => d.collection("posts; drop")).toThrow(/invalid collection/);
    expect(() => d.collection("")).toThrow();
    expect(d.collection("blog.posts-v2").name).toBe("blog.posts-v2");
    expect(new Collection(d, "ok").find({ orderBy: "x'); --" as never })).rejects.toThrow(/invalid orderBy/);
  });

  test("id and timestamps are never stored inside data", async () => {
    const r = new Recorder(() => []);
    await new Database(r).collection("c").update("1", { id: "evil", createdAt: "x", n: 1 } as never);
    const upd = r.calls.at(-1)!;
    expect(JSON.parse(upd.params[2] as string)).toEqual({ n: 1 });
  });

  test("db() explains a missing DATABASE_URL", () => {
    const saved = process.env.DATABASE_URL;
    delete process.env.DATABASE_URL;
    expect(() => db()).toThrow(/DATABASE_URL is not set/);
    if (saved !== undefined) process.env.DATABASE_URL = saved;
  });
});

class FakeRedis implements RedisLike {
  data = new Map<string, string>();
  calls: string[][] = [];
  async send(cmd: string, args: string[]) {
    this.calls.push([cmd, ...args]);
    switch (cmd) {
      case "GET":
        return this.data.get(args[0]!) ?? null;
      case "SET":
        if (args.includes("NX") && this.data.has(args[0]!)) return null;
        this.data.set(args[0]!, args[1]!);
        return "OK";
      case "INCRBY": {
        const n = Number(this.data.get(args[0]!) ?? 0) + Number(args[1]);
        this.data.set(args[0]!, String(n));
        return n;
      }
      case "DEL":
        return args.filter((k) => this.data.delete(k)).length;
      case "EVAL": {
        const n = Number(this.data.get(args[2]!) ?? 0) + 1;
        this.data.set(args[2]!, String(n));
        return [n, Number(args[3])];
      }
    }
    return null;
  }
}

describe("kv (unit)", () => {
  test("prefixes every key", async () => {
    const f = new FakeRedis();
    const s = new KV(f, "p_shop:");
    await s.set("a", "1", { ttl: 1.2 });
    await s.setJSON("p_shop:b", { x: 1 });
    expect(f.calls[0]).toEqual(["SET", "p_shop:a", "1", "EX", "2"]);
    expect(f.calls[1]![1]).toBe("p_shop:b"); // already prefixed: not doubled
    expect(await s.getJSON<{ x: number }>("b")).toEqual({ x: 1 });
    expect(await s.incr("n", 5)).toBe(5);
    expect(await s.set("a", "2", { nx: true })).toBe(false);
    expect(await s.del("a", "missing")).toBe(1);
  });

  test("rate limit allows `limit` hits per window", async () => {
    const s = kv(new FakeRedis(), "p_x:");
    const results = [];
    for (let i = 0; i < 4; i++) results.push(await s.rateLimit("ip:1", { limit: 3, windowSec: 60 }));
    expect(results.map((r) => r.allowed)).toEqual([true, true, true, false]);
    expect(results[0]).toEqual({ allowed: true, remaining: 2, limit: 3, retryAfterSec: 60 });
  });
});

// ---- integration: real Postgres and Valkey on a box (or anywhere) ----
// TIFFIN_TEST_DATABASE_URL=postgresql://... TIFFIN_TEST_REDIS_URL=redis://... TIFFIN_TEST_VALKEY_PREFIX=p_x: bun test

const pgURL = process.env.TIFFIN_TEST_DATABASE_URL;
const redisURL = process.env.TIFFIN_TEST_REDIS_URL;

describe.skipIf(!pgURL)("db (Postgres)", () => {
  test("documents round trip", async () => {
    const sql = new Bun.SQL(pgURL!);
    const posts = db(sql).collection<{ title: string; author: string; tags?: string[]; views?: number }>(`t${Date.now()}`);
    const a = await posts.insert({ title: "Hello", author: "ada", tags: ["go", "pg"] });
    await posts.insertMany([
      { title: "Two", author: "bob" },
      { title: "Three", author: "ada", tags: ["pg"], views: 3 },
    ]);
    expect(a.id.length).toBeGreaterThan(10);
    expect((await posts.get(a.id))?.title).toBe("Hello");
    expect(await posts.get("missing")).toBeNull();
    expect((await posts.find({ where: { author: "ada" } })).map((d) => d.title)).toEqual(["Hello", "Three"]);
    expect((await posts.find({ where: { tags: ["pg"] }, orderBy: "title" })).map((d) => d.title)).toEqual(["Hello", "Three"]);
    expect(await posts.count({ author: "ada" })).toBe(2);
    const upd = await posts.update(a.id, { views: 7 });
    expect(upd).toMatchObject({ title: "Hello", views: 7 });
    expect((await posts.findOne({ views: 7 }))?.id).toBe(a.id);
    expect(await posts.delete(a.id)).toBe(true);
    expect(await posts.delete(a.id)).toBe(false);
    expect(await posts.deleteMany()).toBe(2);
    const plan = (await sql.unsafe(
      `EXPLAIN SELECT id FROM tiffin_docs WHERE collection = 'x' AND data @> '{"author":"ada"}'::jsonb`,
    )) as { "QUERY PLAN": string }[];
    expect(plan.length).toBeGreaterThan(0);
    await sql.close();
  });
});

describe.skipIf(!redisURL)("kv (Valkey)", () => {
  test("helpers work under the project's ACL user", async () => {
    const client = new Bun.RedisClient(redisURL!);
    const s = kv(client, process.env.TIFFIN_TEST_VALKEY_PREFIX ?? "");
    const k = `t${Date.now()}`;
    expect(await s.set(k, "v", { ttl: 30 })).toBe(true);
    expect(await s.get(k)).toBe("v");
    expect(await s.ttl(k)).toBeGreaterThan(0);
    expect(await s.incr(k + ":n")).toBe(1);
    const hits = [];
    for (let i = 0; i < 3; i++) hits.push((await s.rateLimit(k, { limit: 2, windowSec: 10 })).allowed);
    expect(hits).toEqual([true, true, false]);
    expect(await s.cached(k + ":c", 30, async () => ({ n: 1 }))).toEqual({ n: 1 });
    expect(await s.del(k, k + ":n", k + ":c", "ratelimit:" + k)).toBe(4);
    // Outside the prefix the ACL refuses.
    if (process.env.TIFFIN_TEST_VALKEY_PREFIX) {
      expect(client.send("GET", ["someone-else:key"])).rejects.toThrow(/NOPERM/);
    }
    client.close();
  });
});
