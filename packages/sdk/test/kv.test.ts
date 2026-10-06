// tiffin-sdk/kv against a real Valkey (or Redis) server, with the box's ACL:
// a project user limited to "p_t:" keys and channels, no SCAN, plus an admin.
// Every test runs on both connections: Bun's RedisClient and the SDK's own
// RESP client (what Node uses). Skipped when no server binary is found: set
// VALKEY_SERVER=/path/to/valkey-server, or put valkey-server on PATH.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { KVError, kv, type KV } from "../src/kv";
import { RATE_LIMIT_SHA } from "../src/kv/ratelimit";

const bin = process.env.VALKEY_SERVER ?? Bun.which("valkey-server") ?? Bun.which("redis-server");
const dir = mkdtempSync(join(tmpdir(), "kv-test-"));
writeFileSync(
  join(dir, "users.acl"),
  [
    "user default off resetkeys resetchannels -@all",
    "user admin on >adminpw ~* &* +@all",
    "user p_t on >pw ~p_t:* &p_t:* +@all -@admin -@dangerous +info -scan -randomkey -select -move -swapdb",
  ].join("\n") + "\n",
);

async function freePort(): Promise<number> {
  const s = createServer();
  await new Promise<void>((r) => s.listen(0, "127.0.0.1", r));
  const port = (s.address() as { port: number }).port;
  await new Promise((r) => s.close(r));
  return port;
}

async function startServer(port: number) {
  const p = Bun.spawn([bin!, "--port", String(port), "--bind", "127.0.0.1", "--aclfile", join(dir, "users.acl"), "--save", "", "--appendonly", "no", "--dir", dir], {
    stdout: "ignore",
    stderr: "ignore",
  });
  const admin = kv({ url: `redis://admin:adminpw@127.0.0.1:${port}`, prefix: "", driver: "resp", timeoutMs: 500 });
  for (let i = 0; i < 100; i++) {
    if ((await admin.command("PING").catch(() => "")) === "PONG") break;
    await Bun.sleep(30);
  }
  return { proc: p, admin };
}

let port = 0;
let server: Awaited<ReturnType<typeof startServer>>;
const url = () => `redis://p_t:pw@127.0.0.1:${port}`;

beforeAll(async () => {
  if (!bin) return;
  port = await freePort();
  server = await startServer(port);
});
afterAll(() => {
  server?.admin.close();
  server?.proc.kill();
  rmSync(dir, { recursive: true, force: true });
});

const stores: KV[] = [];
afterAll(() => stores.forEach((s) => s.close()));

for (const driver of ["bun", "resp"] as const) {
  describe.skipIf(!bin)(`kv on ${driver}`, () => {
    let s: KV;
    beforeAll(async () => {
      await server.admin.command("FLUSHALL");
      s = kv({ url: url(), prefix: "p_t:", driver });
      stores.push(s);
    });

    test("strings and JSON round trips", async () => {
      expect(await s.set("str", "hello")).toBe(true);
      expect(await s.get<string>("str")).toBe("hello");
      await s.set("obj", { a: 1, b: [true, null, "x"] });
      expect(await s.get<object>("obj")).toEqual({ a: 1, b: [true, null, "x"] });
      await s.set("n", 42);
      expect(await s.get<any>("n")).toBe(42);
      await s.set("arr", [1, "2"]);
      expect(await s.get<any>("arr")).toEqual([1, "2"]);
      // Strings stay strings unless they are JSON (as with @upstash/redis).
      await s.set("money", "1.50");
      expect(await s.get<any>("money")).toBe("1.50");
      await s.set("id", "12345678901234567890");
      expect(await s.get<any>("id")).toBe("12345678901234567890");
      await s.set("unicode", "héllo ✓ 日本");
      expect(await s.get<any>("unicode")).toBe("héllo ✓ 日本");
      expect(await s.get<any>("missing")).toBeNull();
      expect(() => s.set("u", undefined)).toThrow(TypeError);
      // Raw: no JSON either way.
      const raw = s.raw();
      expect(await raw.get<any>("obj")).toBe('{"a":1,"b":[true,null,"x"]}');
      await raw.set("rawn", 7);
      expect(await raw.get<any>("rawn")).toBe("7");
    });

    test("keys are prefixed in Valkey, never in what the app sees", async () => {
      await s.set("user:1", { name: "Ada" });
      expect(await server.admin.command<any>("GET", "p_t:user:1")).toBe('{"name":"Ada"}');
      expect(s.key("user:1")).toBe("p_t:user:1");
      expect(s.key("p_t:user:1")).toBe("p_t:user:1");
      expect(await s.get<any>(s.key("user:1"))).toEqual({ name: "Ada" });
    });

    test("set options, expiry, getdel, mget/mset, del, exists, counters", async () => {
      expect(await s.set("nx", 1, { nx: true })).toBe(true);
      expect(await s.set("nx", 2, { nx: true })).toBe(false);
      expect(await s.set("xx-missing", 1, { xx: true })).toBe(false);
      expect(await s.set("nx", 3, { xx: true, ex: 100 })).toBe(true);
      expect(await s.ttl("nx")).toBeGreaterThan(90);
      await s.set("nx", 4, { keepTtl: true });
      expect(await s.ttl("nx")).toBeGreaterThan(90);
      expect(await s.persist("nx")).toBe(true);
      expect(await s.ttl("nx")).toBe(-1);
      expect(await s.expire("nx", 50)).toBe(true);
      expect(await s.expire("nope", 50)).toBe(false);
      expect(await s.ttl("nope")).toBe(-2);
      await s.set("px", "v", { px: 1500 });
      expect(await s.ttl("px")).toBeGreaterThanOrEqual(1);
      await s.set("exat", "v", { exat: Math.floor(Date.now() / 1000) + 1000 });
      expect(await s.ttl("exat")).toBeGreaterThan(900);

      expect(await s.getdel<any>("nx")).toBe(4);
      expect(await s.getdel<any>("nx")).toBeNull();

      await s.mset({ m1: "a", m2: { b: 2 } });
      expect(await s.mget("m1", "m2", "m3")).toEqual(["a", { b: 2 }, null]);
      expect(await s.mget(["m1", "m2"])).toEqual(["a", { b: 2 }]);
      expect(await s.exists("m1", "m2", "m3")).toBe(2);
      expect(await s.del("m1", "m2", "m3")).toBe(2);
      expect(await s.del()).toBe(0);

      expect(await s.incr("c")).toBe(1);
      expect(await s.incrby("c", 10)).toBe(11);
      expect(await s.decr("c")).toBe(10);
      expect(await s.decrby("c", 5)).toBe(5);
      expect(await s.incrbyfloat("c", 0.5)).toBe(5.5);
      expect(await s.get<any>("c")).toBe(5.5);
    });

    test("hashes", async () => {
      expect(await s.hset("h", { name: "Ada", age: 36, tags: ["x"] })).toBe(3);
      expect(await s.hget<any>("h", "age")).toBe(36);
      expect(await s.hget<any>("h", "nope")).toBeNull();
      expect(await s.hgetall("h")).toEqual({ name: "Ada", age: 36, tags: ["x"] });
      expect(await s.hgetall("missing")).toBeNull();
      expect(await s.hincrby("h", "age", 2)).toBe(38);
      expect(await s.hdel("h", "tags", "nope")).toBe(1);
    });

    test("lists", async () => {
      expect(await s.rpush("l", "a", { b: 1 }, 3)).toBe(3);
      expect(await s.lpush("l", "z")).toBe(4);
      expect(await s.lrange("l", 0, -1)).toEqual(["z", "a", { b: 1 }, 3]);
      expect(await s.llen("l")).toBe(4);
      expect(await s.lpop<any>("l")).toBe("z");
      expect(await s.rpop<any>("l")).toBe(3);
      await s.rpush("l", "c", "d");
      await s.ltrim("l", 0, 2);
      expect(await s.lrange("l", 0, -1)).toEqual(["a", { b: 1 }, "c"]);
      expect(await s.lpop<any>("l", 2)).toEqual(["a", { b: 1 }]);
      expect(await s.lpop<any>("missing")).toBeNull();
      expect(await s.lpop<any>("missing", 2)).toBeNull();
    });

    test("sets", async () => {
      expect(await s.sadd("s", "a", "b", "a")).toBe(2);
      expect((await s.smembers<string>("s")).sort()).toEqual(["a", "b"]);
      expect(await s.sismember("s", "a")).toBe(true);
      expect(await s.sismember("s", "c")).toBe(false);
      expect(await s.srem("s", "a")).toBe(1);
      expect(await s.smembers("missing")).toEqual([]);
    });

    test("sorted sets", async () => {
      expect(await s.zadd("z", { score: 10, member: "ada" }, { score: 5, member: "bob" }, { score: 7.5, member: "cy" })).toBe(3);
      expect(await s.zrange("z", 0, -1)).toEqual(["bob", "cy", "ada"]);
      expect(await s.zrange("z", 0, 1, { rev: true, withScores: true })).toEqual([
        { member: "ada", score: 10 },
        { member: "cy", score: 7.5 },
      ]);
      expect(await s.zrange("z", 6, "+inf", { byScore: true })).toEqual(["cy", "ada"]);
      expect(await s.zrange("z", "+inf", "-inf", { byScore: true, rev: true, limit: { offset: 1, count: 1 } })).toEqual(["cy"]);
      expect(await s.zrank("z", "ada")).toBe(2);
      expect(await s.zrank("z", "ada", { rev: true })).toBe(0);
      expect(await s.zrank("z", "nobody")).toBeNull();
      expect(await s.zincrby("z", 2.5, "bob")).toBe(7.5);
      expect(await s.zscore("z", "bob")).toBe(7.5);
      expect(await s.zscore("z", "nobody")).toBeNull();
      expect(await s.zadd("z", { nx: true }, { score: 1, member: "ada" })).toBe(0);
      expect(await s.zadd("z", { gt: true, ch: true }, { score: 20, member: "ada" })).toBe(1);
      expect(await s.zrem("z", "bob", "nobody")).toBe(1);
      expect(await s.zcard("z")).toBe(2);
      await s.zadd("zo", { score: 1, member: { id: 1 } });
      expect(await s.zrange("zo", 0, -1, { withScores: true })).toEqual([{ member: { id: 1 }, score: 1 }]);
    });

    test("pipeline: one round trip, results in order and per call", async () => {
      const p = s.pipeline();
      const a = p.set("p1", { v: 1 });
      const b = p.incr("p2");
      const c = p.get("p1");
      expect(p.length).toBe(3);
      expect(await p.exec()).toEqual([true, 1, { v: 1 }]);
      expect(await a).toBe(true);
      expect(await b).toBe(1);
      expect(await c).toEqual({ v: 1 });
      expect(await s.pipeline().exec()).toEqual([]);

      // One failure: exec throws it, the other calls still get their results.
      const q = s.pipeline();
      const ok = q.incr("p2");
      const bad = q.incr("p1"); // not a number
      q.lpush("p1", "x"); // wrong type, nobody awaits it
      await expect(q.exec()).rejects.toBeInstanceOf(KVError);
      expect(await ok).toBe(2);
      await expect(bad).rejects.toThrow(/not an integer/);
    });

    test("multi: a transaction", async () => {
      const m = s.multi();
      m.set("t1", 1);
      m.incr("t1");
      m.get("t1");
      expect(await m.exec()).toEqual([true, 2, 2]);
      // A command refused while queueing aborts the whole transaction.
      const bad = s.multi();
      const t2 = bad.set("t2", 1);
      bad.zadd("t3"); // no members: refused when queued
      await expect(bad.exec()).rejects.toThrow(/wrong number of arguments|EXECABORT/);
      await expect(t2).rejects.toBeInstanceOf(KVError);
      expect(await s.exists("t2")).toBe(0);
    });

    test("publish goes to the project's prefixed channel", async () => {
      expect(await s.publish("events", { kind: "x" })).toBe(0);
    });

    test("a key outside the prefix is refused with a plain error", async () => {
      const other = kv({ url: url(), prefix: "p_other:", driver });
      stores.push(other);
      const e = await other.set("a", 1).catch((x: unknown) => x);
      expect(e).toBeInstanceOf(KVError);
      expect((e as KVError).code).toBe("NOPERM");
      expect((e as KVError).message).toMatch(/outside this project's prefix/);
    });

    test("over the memory limit: writes refused with a plain error, reads still work", async () => {
      await s.set("before", "x");
      await server.admin.command("ACL", "SETUSER", "p_t", "-@write", "+del", "+unlink", "+expire", "+getdel");
      try {
        const e = (await s.set("k", "v").catch((x: unknown) => x)) as KVError;
        expect(e.code).toBe("NOPERM");
        expect(e.message).toMatch(/over this project's memory limit, so writes are refused/);
        const rl = (await s.rateLimit("x", { limit: 1, window: 1 }).catch((x: unknown) => x)) as KVError;
        expect(rl.message).toMatch(/memory limit/);
        expect(await s.get<any>("before")).toBe("x");
        expect(await s.del("before")).toBe(1);
      } finally {
        await server.admin.command("ACL", "SETUSER", "p_t", "+@all", "-@admin", "-@dangerous", "+info", "-scan", "-randomkey", "-select", "-move", "-swapdb");
      }
      await server.admin.command("CONFIG", "SET", "maxmemory", "1");
      try {
        const e = (await s.set("k", "v").catch((x: unknown) => x)) as KVError;
        expect(e.code).toBe("OOM");
        expect(e.message).toMatch(/KV is full/);
      } finally {
        await server.admin.command("CONFIG", "SET", "maxmemory", "0");
      }
    });

    test("scan: SCAN where allowed, the box's endpoint where not", async () => {
      // As an admin-like user (a local Redis without ACLs): plain SCAN, prefix stripped.
      const local = kv({ url: `redis://admin:adminpw@127.0.0.1:${port}`, prefix: "p_t:", driver });
      stores.push(local);
      await s.mset({ "scan:a": 1, "scan:b": 2, "scan:c": 3 });
      await server.admin.command("SET", "p_tt:scan:x", "1");
      expect((await local.keys("scan:*")).sort()).toEqual(["scan:a", "scan:b", "scan:c"]);
      const seen: string[] = [];
      for await (const k of local.scan("scan:*", { count: 1 })) seen.push(k);
      expect(seen.sort()).toEqual(["scan:a", "scan:b", "scan:c"]);

      // As the project's user: SCAN is refused; without the box's endpoint, say so.
      const e = (await s.keys("scan:*").catch((x: unknown) => x)) as KVError;
      expect(e.code).toBe("NOPERM");
      // With it (its token names this project), the endpoint lists the keys.
      const calls: unknown[] = [];
      const box = Bun.serve({
        port: 0,
        async fetch(req) {
          const body = await req.json();
          calls.push([req.headers.get("authorization"), body]);
          return Response.json({ result: ["0", ["scan:a", "scan:b"]] });
        },
      });
      const keep = { ...process.env };
      Object.assign(process.env, { KV_REST_API_URL: `http://127.0.0.1:${box.port}`, KV_REST_API_TOKEN: "tvk_t_abc" });
      try {
        const viaBox = kv({ url: url(), prefix: "p_t:", driver });
        stores.push(viaBox);
        expect(await viaBox.keys("scan:*")).toEqual(["scan:a", "scan:b"]);
        expect(calls).toEqual([["Bearer tvk_t_abc", ["SCAN", "0", "MATCH", "scan:*", "COUNT", "1000"]]]);
        // A token for another project (say an old Upstash one) is never used.
        process.env.KV_REST_API_TOKEN = "AXxxupstash";
        const notBox = kv({ url: url(), prefix: "p_t:", driver });
        stores.push(notBox);
        await expect(notBox.keys()).rejects.toThrow(/SCAN/i);
      } finally {
        process.env = keep;
        box.stop(true);
      }
    });

    test("rateLimit: exactly `limit` of 200 parallel calls pass", async () => {
      for (const algorithm of ["sliding", "fixed"] as const) {
        const results = await Promise.all(Array.from({ length: 200 }, () => s.rateLimit(`burst-${algorithm}`, { limit: 50, window: "1 m", algorithm })));
        const allowed = results.filter((r) => r.allowed);
        expect(allowed.length).toBe(50);
        expect(Math.min(...allowed.map((r) => r.remaining))).toBe(0);
        const refused = results.find((r) => !r.allowed)!;
        expect(refused.remaining).toBe(0);
        expect(refused.retryAfter).toBeGreaterThan(0);
        expect(refused.retryAfter).toBeLessThanOrEqual(120);
        expect(refused.reset).toBeGreaterThan(Date.now());
      }
      // The key lives under the project prefix.
      expect(await server.admin.command<any>("EXISTS", "p_t:ratelimit:burst-sliding")).toBe(1);
    });

    test("rateLimit: windows pass, costs count, retryAfter is honest", async () => {
      const w = { limit: 3, window: "400 ms" as const };
      for (let i = 0; i < 3; i++) expect((await s.rateLimit("win", w)).allowed).toBe(true);
      const no = await s.rateLimit("win", w);
      expect(no.allowed).toBe(false);
      expect(no.retryAfter).toBe(1);
      expect(no.reset - Date.now()).toBeLessThanOrEqual(800);
      await Bun.sleep(Math.max(0, no.reset - Date.now()) + 20);
      expect((await s.rateLimit("win", w)).allowed).toBe(true);
      await Bun.sleep(900); // two windows: all forgotten
      const fresh = await s.rateLimit("win", w);
      expect(fresh.allowed).toBe(true);
      expect(fresh.remaining).toBe(2);
      expect((await s.rateLimit("cost", { limit: 10, window: 60, cost: 7 })).remaining).toBe(3);
      expect((await s.rateLimit("cost", { limit: 10, window: 60, cost: 7 })).allowed).toBe(false);
      expect((await s.rateLimit("cost", { limit: 10, window: 60, cost: 3 })).allowed).toBe(true);
      expect(() => s.rateLimit("bad", { limit: 1, window: "soon" as "1 s" })).toThrow(/not like/);
    });

    test("rateLimit: EVALSHA, then EVAL when the server lost the script", async () => {
      await server.admin.command("SCRIPT", "FLUSH");
      expect(await server.admin.command<any>("SCRIPT", "EXISTS", RATE_LIMIT_SHA)).toEqual([0]);
      expect((await s.rateLimit("sha", { limit: 1, window: 10 })).allowed).toBe(true);
      expect(await server.admin.command<any>("SCRIPT", "EXISTS", RATE_LIMIT_SHA)).toEqual([1]);
      expect((await s.rateLimit("sha", { limit: 1, window: 10 })).allowed).toBe(false);
    });

    test("cached: one computation for many callers, stale value while refreshing", async () => {
      let calls = 0;
      const slow = async () => {
        calls++;
        await Bun.sleep(100);
        return { n: calls };
      };
      const all = await Promise.all(Array.from({ length: 20 }, () => s.cached("c1", 1, slow)));
      expect(calls).toBe(1);
      expect(all.every((v) => v.n === 1)).toBe(true);
      expect(await s.cached("c1", 1, slow)).toEqual({ n: 1 });
      await Bun.sleep(1100); // stale now
      const during = await Promise.all(Array.from({ length: 10 }, () => s.cached("c1", 1, slow)));
      expect(during.every((v) => v.n === 1)).toBe(true); // old value right away
      await Bun.sleep(200);
      expect(calls).toBe(2); // one refresh
      expect(await s.cached("c1", 1, slow)).toEqual({ n: 2 });
      // stale: 0 waits for the fresh value.
      await s.cached("c2", 1, async () => "a", { stale: 0 });
      expect(await s.ttl("cached:c2")).toBe(1);
    });

    test("reconnects after the server drops the connection", async () => {
      expect(await s.get<any>("str")).toBe("hello");
      await server.admin.command("CLIENT", "KILL", "USER", "p_t");
      let v: unknown;
      for (let i = 0; i < 20 && v !== "hello"; i++) v = await s.get("str").catch(() => Bun.sleep(100));
      expect(v).toBe("hello");
    });

    test("command and client: the raw escape hatch", async () => {
      expect(await s.command<any>("HLEN", s.key("h"))).toBe(2);
      expect(s.client).toBeDefined();
    });
  });

  describe.skipIf(!bin)(`kv on ${driver}: a server that goes away`, () => {
    test("fails fast while down, recovers when it is back", async () => {
      const p = await freePort();
      let srv = await startServer(p);
      const s = kv({ url: `redis://p_t:pw@127.0.0.1:${p}`, prefix: "p_t:", driver, timeoutMs: 1000 });
      stores.push(s);
      await s.set("k", "v");
      srv.admin.close();
      srv.proc.kill();
      await srv.proc.exited;
      const t0 = Date.now();
      const e = (await s.get("k").catch((x: unknown) => x)) as KVError;
      expect(e).toBeInstanceOf(KVError);
      expect(["CONNECTION", "TIMEOUT"]).toContain(e.code);
      expect(Date.now() - t0).toBeLessThan(2500);
      // Down long enough that Bun's client gives up for good: a new one is made.
      await Bun.sleep(2500);
      await expect(s.get("k")).rejects.toBeInstanceOf(KVError);
      srv = await startServer(p);
      let up = false;
      for (let i = 0; i < 80 && !up; i++) {
        up = await s.ping().catch(() => false);
        if (!up) await Bun.sleep(100);
      }
      expect(up).toBe(true);
      expect(await s.get<any>("k")).toBeNull(); // a fresh server
      srv.admin.close();
      srv.proc.kill();
    }, 20_000);

    test("a server that never answers: the call times out", async () => {
      const silent = createServer(() => {});
      await new Promise<void>((r) => silent.listen(0, "127.0.0.1", r));
      const s = kv({ url: `redis://127.0.0.1:${(silent.address() as { port: number }).port}`, prefix: "p_t:", driver, timeoutMs: 300 });
      const t0 = Date.now();
      const e = (await s.get("k").catch((x: unknown) => x)) as KVError;
      expect(e).toBeInstanceOf(KVError);
      expect(["TIMEOUT", "CONNECTION"]).toContain(e.code);
      expect(Date.now() - t0).toBeLessThan(1500);
      s.close();
      silent.close();
    });
  });
}

describe.skipIf(!bin)("kv: scripts exit when done", () => {
  test("an idle connection does not keep the process alive", async () => {
    for (const driver of ["bun", "resp"]) {
      const code = `import { kv } from ${JSON.stringify(join(import.meta.dir, "../src/kv.ts"))};
const s = kv({ url: ${JSON.stringify(url())}, prefix: "p_t:", driver: "${driver}" });
await s.set("exit", 1); console.log(await s.get("exit"));`;
      const file = join(dir, `exit-${driver}.ts`);
      writeFileSync(file, code);
      const t0 = Date.now();
      const p = Bun.spawn(["bun", file], { stdout: "pipe" });
      const timer = setTimeout(() => p.kill(), 5000);
      await p.exited;
      clearTimeout(timer);
      expect((await new Response(p.stdout).text()).trim()).toBe("1");
      expect(Date.now() - t0).toBeLessThan(4000);
    }
  });
});

test("kv() without REDIS_URL says what to do", () => {
  const keep = process.env.REDIS_URL;
  delete process.env.REDIS_URL;
  try {
    expect(() => kv({ prefix: "p_t:" })).toThrow(/REDIS_URL is not set/);
  } finally {
    if (keep !== undefined) process.env.REDIS_URL = keep;
  }
});
