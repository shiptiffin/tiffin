// End to end: builds test/fixture (an Astro site with on-demand routes and no
// adapter) with tiffin(), runs its server under Node and Bun, and checks the
// health route, proxy trust, sessions and the route cache in Valkey, and
// graceful shutdown.
//
// Valkey: TEST_REDIS_URL (an admin connection; a project user is made on it),
// else a throwaway valkey-server / redis-server on a free port if one is on
// PATH. Without either (or with TEST_NO_REDIS=1), the Valkey-backed tests are
// skipped and the fixture is built without REDIS_URL.
//
// Slow (two Astro builds): run with `bun test test/fixture.test.ts`.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { kv, type KV } from "@shiptiffin/sdk/kv";

const pkg = join(import.meta.dir, "..");
const fixture = join(import.meta.dir, "fixture");
const astroBin = join(pkg, "node_modules", ".bin", "astro");
const PREFIX = "p_fixture:";
const HOSTS = "web-fixture.box.example.test fixture.example.test";
const TIFFIN_URL = "https://web-fixture.box.example.test";

function freePort(): Promise<number> {
  return new Promise((resolve) => {
    const s = createServer().listen(0, "127.0.0.1", () => {
      const p = (s.address() as { port: number }).port;
      s.close(() => resolve(p));
    });
  });
}

const which = (b: string) => spawnSync("sh", ["-c", `command -v ${b}`]).stdout.toString().trim();
/** Whether the Valkey-backed tests can run (decided before the tests are declared). */
const VALKEY = !process.env.TEST_NO_REDIS && Boolean(process.env.TEST_REDIS_URL || which("valkey-server") || which("redis-server"));

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function waitFor(url: string, ms = 15_000): Promise<void> {
  const end = Date.now() + ms;
  for (;;) {
    try {
      await fetch(url);
      return;
    } catch {
      if (Date.now() > end) throw new Error(`${url} did not answer`);
      await sleep(100);
    }
  }
}

let redis: { url: string; admin: KV; stop: () => void } | undefined;

async function startValkey(): Promise<typeof redis> {
  if (!VALKEY) return undefined;
  let adminUrl = process.env.TEST_REDIS_URL;
  let child: ChildProcess | undefined;
  let dir: string | undefined;
  if (!adminUrl) {
    const bin = which("valkey-server") || which("redis-server");
    if (!bin) return undefined;
    const port = await freePort();
    dir = mkdtempSync(join(tmpdir(), "tiffin-astro-valkey-"));
    child = spawn(bin, ["--port", String(port), "--bind", "127.0.0.1", "--save", "", "--appendonly", "no", "--dir", dir], { stdio: "ignore" });
    adminUrl = `redis://127.0.0.1:${port}`;
  }
  const admin = kv({ url: adminUrl, prefix: "", json: false, driver: "resp" });
  for (let i = 0; ; i++) {
    try {
      await admin.ping();
      break;
    } catch (e) {
      if (i > 50) throw e;
      await sleep(100);
    }
  }
  // A project user like the box makes (internal/mod/valkey ACLRules).
  await admin.command("ACL", "SETUSER", "p_fixture", "reset", "on", ">fixture-pw", `~${PREFIX}*`, `&${PREFIX}*`, "+@all", "-@admin", "-@dangerous", "-scan", "-randomkey", "-select");
  const u = new URL(adminUrl);
  u.username = "p_fixture";
  u.password = "fixture-pw";
  return {
    url: u.toString(),
    admin,
    stop: () => {
      admin.close();
      child?.kill();
      if (dir) rmSync(dir, { recursive: true, force: true });
    },
  };
}

function build(env: Record<string, string>): string {
  const r = spawnSync(astroBin, ["build"], { cwd: fixture, env: { ...process.env, ...env, ASTRO_TELEMETRY_DISABLED: "1" }, encoding: "utf8" });
  const log = r.stdout + r.stderr;
  if (r.status !== 0) throw new Error(`fixture build failed:\n${log}`);
  return log;
}

interface Server {
  base: string;
  proc: ChildProcess;
  out: () => string;
  exited: Promise<number | null>;
}

async function serve(runtime: string, env: Record<string, string>): Promise<Server> {
  const port = await freePort();
  let out = "";
  const proc = spawn(runtime, ["dist/server/entry.mjs"], { cwd: fixture, env: { ...process.env, ...env, PORT: String(port), HOST: "127.0.0.1" }, stdio: ["ignore", "pipe", "pipe"] });
  proc.stdout!.on("data", (d) => (out += d));
  proc.stderr!.on("data", (d) => (out += d));
  const exited = new Promise<number | null>((r) => proc.on("exit", (code) => r(code)));
  const base = `http://127.0.0.1:${port}`;
  await waitFor(`${base}/_health`);
  return { base, proc, out: () => out, exited };
}

let buildLog = "";
let runEnv: Record<string, string> = {};

beforeAll(async () => {
  const b = spawnSync("bun", ["run", "build"], { cwd: pkg, encoding: "utf8" });
  if (b.status !== 0) throw new Error(b.stderr);
  redis = await startValkey();
  const env: Record<string, string> = { TIFFIN_URL, TIFFIN_HOSTS: HOSTS, TIFFIN_APP: "web" };
  if (redis) Object.assign(env, { REDIS_URL: redis.url, VALKEY_PREFIX: PREFIX });
  runEnv = env;
  buildLog = build(env);
  console.log(redis ? `fixture: Valkey at ${redis.url.replace(/:[^:@/]*@/, ":***@")}` : "fixture: no Valkey available, Valkey tests skipped");
}, 120_000);

afterAll(() => redis?.stop());

describe("build", () => {
  test("adds @astrojs/node and wraps its entry", () => {
    expect(buildLog).toContain("adapter: @astrojs/node (standalone)");
    expect(existsSync(join(fixture, "dist/server/astro-entry.mjs"))).toBe(true);
    expect(readFileSync(join(fixture, "dist/server/entry.mjs"), "utf8")).toStartWith("// Written by @shiptiffin/astro");
    expect(existsSync(join(fixture, "dist/client/index.html"))).toBe(true); // static pages stay prerendered
  });
});

for (const runtime of ["node", "bun"]) {
  describe(`server on ${runtime}`, () => {
    let s: Server;
    beforeAll(async () => {
      s = await serve(runtime, runEnv);
    }, 30_000);
    afterAll(() => {
      s?.proc.kill("SIGKILL");
    });

    test("health route", async () => {
      const r = await fetch(`${s.base}/_health`);
      expect(r.status).toBe(200);
      expect(r.headers.get("cache-control")).toBe("no-store");
      expect(await r.json()).toMatchObject({ ok: true, app: "web" });
    });

    test("trusts X-Forwarded-Host only for the app's hosts", async () => {
      const ok = await (await fetch(`${s.base}/url`, { headers: { "x-forwarded-host": "fixture.example.test", "x-forwarded-proto": "https" } })).json();
      expect(ok).toEqual({ url: "https://fixture.example.test/url", site: `${TIFFIN_URL}/` });
      const bad = await (await fetch(`${s.base}/url`, { headers: { "x-forwarded-host": "evil.example", "x-forwarded-proto": "https" } })).json();
      expect(new URL(bad.url).hostname).not.toBe("evil.example");
    });

    test.skipIf(!VALKEY)("session round trip in Valkey", async () => {
      const first = await fetch(`${s.base}/session`);
      const cookie = (first.headers.get("set-cookie") ?? "").split(";")[0]!;
      const a = await first.json();
      expect(a.count).toBe(1);
      const b = await (await fetch(`${s.base}/session`, { headers: { cookie } })).json();
      expect(b).toEqual({ count: 2, id: a.id });
      const stored = await redis!.admin.get(`${PREFIX}sess:${a.id}`);
      expect(typeof stored).toBe("string");
      const ttl = Number(await redis!.admin.command("TTL", `${PREFIX}sess:${a.id}`));
      expect(ttl).toBeGreaterThan(29 * 24 * 3600);
    });

    test.skipIf(!VALKEY)("route cache in Valkey: shared, survives a restart, invalidates", async () => {
      // Entries are keyed by host, as the edge sends it (each test server has its own port).
      const fwd = { headers: { "x-forwarded-host": "fixture.example.test", "x-forwarded-proto": "https" } };
      await fetch(`${s.base}/invalidate`, { method: "POST", headers: { "content-type": "application/json" } });
      await sleep(5);
      const r1 = await fetch(`${s.base}/cached`, fwd);
      expect(r1.headers.get("x-astro-cache")).toBe("MISS");
      const body1 = await r1.json();
      await sleep(100); // the store happens after the response
      const r2 = await fetch(`${s.base}/cached`, fwd);
      expect(r2.headers.get("x-astro-cache")).toBe("HIT");
      expect(await r2.json()).toEqual(body1);
      expect(r2.headers.get("cdn-cache-control")).toBeNull(); // Astro strips the internal headers

      // Another process (a restart, or a second instance) gets the same entry.
      const other = await serve(runtime, runEnv);
      try {
        const r3 = await fetch(`${other.base}/cached`, fwd);
        expect(r3.headers.get("x-astro-cache")).toBe("HIT");
        expect(await r3.json()).toEqual(body1);
        // Invalidated from the other instance: this one misses.
        await fetch(`${other.base}/invalidate`, { method: "POST", headers: { "content-type": "application/json" } });
      } finally {
        other.proc.kill("SIGKILL");
      }
      await sleep(5);
      const r4 = await fetch(`${s.base}/cached`, fwd);
      expect(r4.headers.get("x-astro-cache")).toBe("MISS");
      expect((await r4.json()).at).not.toBe(body1.at);
      const keys = (await redis!.admin.command<string[]>("KEYS", `${PREFIX}astro-cache:web:prod:*`)).sort();
      expect(keys.some((k) => k.includes(":e:"))).toBe(true);
      expect(keys).toContain(`${PREFIX}astro-cache:web:prod:t:fixture`);
    });

    test("SIGTERM: in-flight requests finish, new connections are refused, exit 0", async () => {
      const d = await serve(runtime, runEnv);
      const slow = fetch(`${d.base}/slow?ms=1200`).then(async (r) => [r.status, await r.text()] as const);
      await sleep(300);
      d.proc.kill("SIGTERM");
      await sleep(200);
      await expect(fetch(`${d.base}/_health`)).rejects.toThrow();
      expect(await slow).toEqual([200, "slow done"]);
      expect(await d.exited).toBe(0);
      expect(d.out()).toContain("SIGTERM: finishing in-flight requests");
    }, 15_000);
  });
}
