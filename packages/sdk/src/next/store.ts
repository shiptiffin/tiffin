/**
 * The storage behind `tiffin-sdk/next`'s cache handlers: entries and tag
 * revalidation timestamps in Valkey, shared by every instance of the app.
 *
 * Keys (all under VALKEY_PREFIX, the project's ACL prefix):
 *   <prefix>next:<app>:<build>:e:<key>   incremental cache entries (ISR, route handlers, fetch, images)
 *   <prefix>next:<app>:<build>:u:<key>   "use cache" entries
 *   <prefix>next:<app>:tags              hash: tag → last revalidation (ms since epoch)
 *
 * Entries are namespaced by the Next.js build ID, so a new deploy never
 * serves pages rendered against another build's assets, and a rollback finds
 * its own build's cache again. Tags are shared across builds.
 */

import { readFileSync } from "node:fs";

/** The subset of a Redis client the handlers use: Bun's RedisClient as is. */
export interface RedisLike {
  send(command: string, args: string[]): Promise<unknown>;
}

/** Wraps an ioredis client (`call`) or a node-redis v4+ client (`sendCommand`). */
export function toRedisLike(client: unknown): RedisLike {
  const c = client as Record<string, unknown>;
  if (typeof c.send === "function") return client as RedisLike;
  if (typeof c.call === "function") {
    return { send: (cmd, args) => (c.call as (...a: string[]) => Promise<unknown>).call(client, cmd, ...args) };
  }
  if (typeof c.sendCommand === "function") {
    return { send: (cmd, args) => (c.sendCommand as (a: string[]) => Promise<unknown>).call(client, [cmd, ...args]) };
  }
  throw new TypeError("tiffin-sdk/next: pass a Bun RedisClient, an ioredis client or a node-redis client");
}

/**
 * An in-memory stand-in supporting exactly the commands the handlers send.
 * Used when no REDIS_URL is set (local dev, `next build`): caching then
 * works per process, like Next.js's default.
 */
export function memoryRedis(): RedisLike {
  const strings = new Map<string, { v: string; exp: number }>();
  const hashes = new Map<string, Map<string, string>>();
  const live = (k: string) => {
    const e = strings.get(k);
    if (e && e.exp && Date.now() > e.exp) {
      strings.delete(k);
      return undefined;
    }
    return e;
  };
  return {
    async send(cmd: string, args: string[]): Promise<unknown> {
      switch (cmd.toUpperCase()) {
        case "GET":
          return live(args[0]!)?.v ?? null;
        case "SET": {
          let exp = 0;
          const i = args.findIndex((a) => a.toUpperCase() === "EX");
          if (i >= 0) exp = Date.now() + Number(args[i + 1]) * 1000;
          strings.set(args[0]!, { v: args[1]!, exp });
          return "OK";
        }
        case "DEL":
          return args.reduce((n, k) => n + (strings.delete(k) || hashes.delete(k) ? 1 : 0), 0);
        case "HSET": {
          const h = hashes.get(args[0]!) ?? new Map<string, string>();
          for (let i = 1; i + 1 < args.length; i += 2) h.set(args[i]!, args[i + 1]!);
          hashes.set(args[0]!, h);
          return (args.length - 1) / 2;
        }
        case "HMGET": {
          const h = hashes.get(args[0]!);
          return args.slice(1).map((f) => h?.get(f) ?? null);
        }
        case "HGETALL":
          return Object.fromEntries(hashes.get(args[0]!) ?? []);
        default:
          throw new Error(`memoryRedis: unsupported command ${cmd}`);
      }
    },
  };
}

type BunLike = { RedisClient: new (url?: string) => RedisLike };

let shared: RedisLike | undefined;

/**
 * The client the handlers use by default: Bun's built-in RedisClient on
 * REDIS_URL (Tiffin sets it when the project has `services.valkey`), or an
 * in-memory store when REDIS_URL is unset or the process is not Bun.
 */
export function defaultClient(): RedisLike {
  if (shared) return shared;
  const url = process.env.REDIS_URL ?? process.env.VALKEY_URL;
  const bun = (globalThis as { Bun?: BunLike }).Bun;
  if (url && bun?.RedisClient) {
    shared = new bun.RedisClient(url);
  } else {
    if (url && process.env.NEXT_PHASE !== "phase-production-build") {
      warnOnce("REDIS_URL is set but this is not Bun: pass a client (ioredis/node-redis) to configure(), or run with `bun --bun next start`. Using an in-memory cache.");
    }
    shared = memoryRedis();
  }
  return shared;
}

const warned = new Set<string>();
export function warnOnce(msg: string): void {
  if (warned.has(msg)) return;
  warned.add(msg);
  console.warn("[tiffin-sdk/next] " + msg);
}

/** Settings shared by both handlers. */
export interface StoreOptions {
  /** Defaults to defaultClient(). */
  client?: RedisLike;
  /** Key prefix. Default: VALKEY_PREFIX + "next:" + TIFFIN_APP + ":". */
  prefix?: string;
  /** Build ID used to namespace entries. Default: .next/BUILD_ID, else TIFFIN_DEPLOY. */
  buildId?: string;
  /** Upper bound for entry TTLs in seconds. Default 30 days. */
  maxTtlSeconds?: number;
}

let options: StoreOptions = {};

/** Configure both handlers, e.g. to pass your own client. Call before Next.js starts serving. */
export function configure(o: StoreOptions): void {
  options = { ...options, ...o };
  if (o.client) shared = toRedisLike(o.client);
}

export class Store {
  readonly client: RedisLike;
  readonly prefix: string;
  readonly buildId: string;
  readonly maxTtl: number;
  /** Local copy of tag timestamps, refreshed from Valkey. */
  readonly tagTimes = new Map<string, number>();

  constructor(o: StoreOptions = {}) {
    const opt = { ...options, ...o };
    this.client = opt.client ? toRedisLike(opt.client) : defaultClient();
    this.prefix = opt.prefix ?? `${process.env.VALKEY_PREFIX ?? ""}next:${process.env.TIFFIN_APP ?? "app"}:`;
    this.buildId = opt.buildId ?? readBuildId() ?? process.env.TIFFIN_DEPLOY ?? "dev";
    this.maxTtl = opt.maxTtlSeconds ?? 30 * 24 * 3600;
  }

  entryKey(kind: "e" | "u", key: string): string {
    return `${this.prefix}${this.buildId}:${kind}:${key}`;
  }

  get tagsKey(): string {
    return `${this.prefix}tags`;
  }

  async getJSON<T>(key: string): Promise<T | undefined> {
    const raw = await this.client.send("GET", [key]);
    if (typeof raw !== "string" || raw === "") return undefined;
    return JSON.parse(raw, revive) as T;
  }

  async setJSON(key: string, value: unknown, ttlSeconds: number): Promise<void> {
    const ttl = Math.max(1, Math.min(Math.floor(ttlSeconds) || this.maxTtl, this.maxTtl));
    await this.client.send("SET", [key, JSON.stringify(value, replace), "EX", String(ttl)]);
  }

  async del(key: string): Promise<void> {
    await this.client.send("DEL", [key]);
  }

  /** Latest revalidation time (ms) across tags, from Valkey. */
  async tagsExpiredAt(tags: string[]): Promise<number> {
    const uniq = [...new Set(tags)].filter(Boolean);
    if (uniq.length === 0) return 0;
    const vals = (await this.client.send("HMGET", [this.tagsKey, ...uniq])) as (string | null)[];
    let max = 0;
    vals?.forEach((v, i) => {
      const n = Number(v ?? 0);
      if (n > 0) this.tagTimes.set(uniq[i]!, n);
      if (n > max) max = n;
    });
    return max;
  }

  /** Marks tags revalidated now (every instance sees it). */
  async revalidate(tags: string[], at = Date.now()): Promise<void> {
    const uniq = [...new Set(tags)].filter(Boolean);
    if (uniq.length === 0) return;
    const args = [this.tagsKey];
    for (const t of uniq) {
      args.push(t, String(at));
      this.tagTimes.set(t, at);
    }
    await this.client.send("HSET", args);
  }

  /** Pulls every tag timestamp into the local map. */
  async refreshTags(): Promise<void> {
    const raw = await this.client.send("HGETALL", [this.tagsKey]);
    const pairs: [string, unknown][] = Array.isArray(raw)
      ? Array.from({ length: raw.length / 2 }, (_, i) => [String(raw[2 * i]), raw[2 * i + 1]])
      : Object.entries((raw ?? {}) as Record<string, unknown>);
    for (const [k, v] of pairs) this.tagTimes.set(k, Number(v));
  }
}

/** Reads Next.js's BUILD_ID from a .next directory (default: ./.next). */
export function readBuildId(distDir = `${process.cwd()}/.next`): string | undefined {
  try {
    return readFileSync(`${distDir}/BUILD_ID`, "utf8").trim() || undefined;
  } catch {
    return undefined;
  }
}

// JSON with Buffers/Uint8Arrays and Maps (Next.js cache values contain both).
function replace(this: Record<string, unknown>, key: string, value: unknown): unknown {
  const raw = this[key];
  if (raw instanceof Uint8Array) return { __t: "b", d: Buffer.from(raw).toString("base64") };
  if (raw instanceof Map) return { __t: "m", d: [...raw.entries()] };
  return value;
}

function revive(_key: string, value: unknown): unknown {
  if (value && typeof value === "object" && "__t" in value) {
    const v = value as { __t: string; d: unknown };
    if (v.__t === "b") return Buffer.from(v.d as string, "base64");
    if (v.__t === "m") return new Map(v.d as [string, unknown][]);
  }
  return value;
}
