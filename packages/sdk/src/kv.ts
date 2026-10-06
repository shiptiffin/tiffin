/**
 * `@shiptiffin/sdk/kv`: the project's key-value store (Valkey), with no setup.
 *
 * ```ts
 * import { kv } from "@shiptiffin/sdk/kv";
 *
 * const store = kv();                                  // REDIS_URL + VALKEY_PREFIX
 * await store.set("user:1", { name: "Ada" }, { ex: 3600 });
 * const user = await store.get<{ name: string }>("user:1");
 * await store.incr("visits");
 * await store.zadd("scores", { score: 42, member: "ada" });
 * await store.zrange("scores", 0, 9, { rev: true, withScores: true });
 *
 * const rl = await store.rateLimit(`login:${ip}`, { limit: 5, window: "1 m" });
 * if (!rl.allowed) return new Response("Slow down", { status: 429, headers: { "retry-after": String(rl.retryAfter) } });
 *
 * const posts = await store.cached("posts:latest", 60, () => db.query.posts.findMany());
 * ```
 *
 * - Keys: every key gets the project's prefix (VALKEY_PREFIX, "p_<project>:")
 *   and keys that come back (scan, keys) lose it, so code only sees its own
 *   names. `store.key("x")` is the full name, for your own clients.
 * - Values: like @upstash/redis, strings are stored as is and anything else as
 *   JSON; reads parse JSON back (`get<T>()`). A string that is valid JSON
 *   ("42", "true") reads back parsed; `kv({ json: false })` returns raw strings.
 * - Connection: one per process, opened on first use. Bun's built-in
 *   RedisClient on Bun, the SDK's own client on Node (no dependencies).
 *   Calls in the same tick share one round trip; a dropped connection is
 *   reopened (with backoff); a call fails after `timeoutMs` (5 s).
 * - Errors are KVError with the server's code ("NOPERM", "OOM", ...) and a
 *   message that says what to do (e.g. when the project is over its memory limit).
 * - Moving from @upstash/redis or @vercel/kv: the method names and options
 *   match. Differences: `zrange(..., { withScores: true })` returns
 *   `{ member, score }[]`; `set` returns a boolean; `rateLimit` is built in.
 */
import { BunConn, ClientConn, KVError, RespConn, bunRedis, kvError, type Conn, type RedisLike } from "./kv/conn";
import { RATE_LIMIT_LUA, RATE_LIMIT_SHA, rateLimitResult, windowMs, type RateLimitOptions, type RateLimitResult } from "./kv/ratelimit";

export { KVError, type RedisLike, type RateLimitOptions, type RateLimitResult };
export type { Window } from "./kv/ratelimit";

export interface KVOptions {
  /** Connection URL (default REDIS_URL): redis://, rediss://, redis+unix://. */
  url?: string;
  /** Key prefix (default VALKEY_PREFIX). */
  prefix?: string;
  /** Store and parse JSON (default true). false: values are plain strings. */
  json?: boolean;
  /** A call fails after this long without a reply (default 5000). */
  timeoutMs?: number;
  /** Use this client instead of connecting: Bun.redis, or anything with send(command, args). */
  client?: RedisLike;
  /** Which connection to open: "bun" (Bun's RedisClient, the default on Bun) or "resp" (the SDK's own; the default elsewhere). */
  driver?: "bun" | "resp";
}

export interface SetOptions {
  /** Expire after this many seconds. */
  ex?: number;
  /** Expire after this many milliseconds. */
  px?: number;
  /** Expire at this Unix time (seconds). */
  exat?: number;
  /** Expire at this Unix time (milliseconds). */
  pxat?: number;
  /** Only set if the key does not exist. */
  nx?: boolean;
  /** Only set if the key exists. */
  xx?: boolean;
  /** Keep the key's current expiry. */
  keepTtl?: boolean;
}

export interface ScoreMember<T = unknown> {
  score: number;
  member: T;
}

export interface ZAddOptions {
  /** Only add new members. */
  nx?: boolean;
  /** Only update existing members. */
  xx?: boolean;
  /** Only update when the new score is greater. */
  gt?: boolean;
  /** Only update when the new score is less. */
  lt?: boolean;
  /** Return how many changed (added or updated), not only added. */
  ch?: boolean;
}

export interface ZRangeOptions {
  /** Highest score first. */
  rev?: boolean;
  /** start and stop are scores ("-inf", "+inf", "(5" for exclusive), not ranks. With rev, pass max then min. */
  byScore?: boolean;
  /** With byScore: skip offset, return at most count. */
  limit?: { offset: number; count: number };
}

export interface ScanOptions {
  /** Keys per round trip, a hint (default 100). */
  count?: number;
  /** Only keys of this type: "string", "hash", "list", "set", "zset". */
  type?: string;
}

export interface CachedOptions {
  /**
   * Seconds past `ttl` that the old value is still served while one caller
   * refreshes it (default: ttl). 0: everyone waits for the fresh value.
   */
  stale?: number;
}

type Decode<T> = (r: unknown) => T;

const num = (r: unknown): number => Number(r);
const bool = (r: unknown): boolean => Number(r) === 1;
const ok = (): void => undefined;

/** Hash replies: RESP3 maps arrive as objects, RESP2 ones as [field, value, ...]. */
function pairs(r: unknown): [string, unknown][] {
  if (r == null) return [];
  if (Array.isArray(r)) {
    if (r.length > 0 && Array.isArray(r[0])) return r as [string, unknown][];
    const out: [string, unknown][] = [];
    for (let i = 0; i + 1 < r.length; i += 2) out.push([String(r[i]), r[i + 1]]);
    return out;
  }
  if (r instanceof Map) return [...r.entries()] as [string, unknown][];
  return Object.entries(r as Record<string, unknown>);
}

/**
 * The data commands, shared by the store (runs them now) and pipelines
 * (queue them). Every method returns a promise of its decoded reply.
 */
export abstract class Commands {
  constructor(
    readonly prefix: string,
    /** Whether values are stored and read as JSON. */
    readonly json: boolean,
  ) {}

  protected abstract run<T>(args: string[], decode: Decode<T>): Promise<T>;

  /** The full key in Valkey: prefix + name (unchanged if it already has the prefix). */
  key(name: string): string {
    return name.startsWith(this.prefix) ? name : this.prefix + name;
  }

  protected keyList(names: (string | string[])[]): string[] {
    return names.flat().map((k) => this.key(k));
  }

  /** A value as stored: strings as is, anything else as JSON. */
  protected enc(v: unknown): string {
    if (typeof v === "string") return v;
    if (v === undefined || typeof v === "function" || typeof v === "symbol") throw new TypeError(`KV: cannot store ${typeof v}`);
    if (!this.json) {
      if (typeof v === "number" || typeof v === "boolean" || typeof v === "bigint") return String(v);
      throw new TypeError("KV: with json: false, values must be strings or numbers");
    }
    return JSON.stringify(v);
  }

  /** A stored value back: JSON parsed (as @upstash/redis does), else the string. */
  protected dec = (v: unknown): unknown => {
    if (v == null) return null;
    if (typeof v !== "string" || !this.json) return v;
    // Only text that starts like JSON is parsed: a plain string costs no exception.
    if (!/^[[{"tfn\-0-9]/.test(v)) return v;
    try {
      const p: unknown = JSON.parse(v);
      // "1.50" or a 20-digit id would lose digits as a number: keep the string.
      return typeof p === "number" && String(p) !== v ? v : p;
    } catch {
      return v;
    }
  };

  protected list = <T>(r: unknown): T[] => (Array.isArray(r) ? r.map(this.dec) : []) as T[];

  /** True when Valkey answers. */
  ping(): Promise<boolean> {
    return this.run(["PING"], (r) => r === "PONG");
  }

  // Strings

  /** The value, or null when the key does not exist. */
  get<T = unknown>(key: string): Promise<T | null> {
    return this.run(["GET", this.key(key)], this.dec as Decode<T | null>);
  }

  /**
   * Set a value (objects as JSON). Returns false when `nx`/`xx` stopped it.
   * @example await store.set("session:abc", { userId: 1 }, { ex: 3600 })
   */
  set(key: string, value: unknown, opts: SetOptions = {}): Promise<boolean> {
    const args = ["SET", this.key(key), this.enc(value)];
    if (opts.ex !== undefined) args.push("EX", String(Math.max(1, Math.ceil(opts.ex))));
    else if (opts.px !== undefined) args.push("PX", String(Math.max(1, Math.ceil(opts.px))));
    else if (opts.exat !== undefined) args.push("EXAT", String(Math.trunc(opts.exat)));
    else if (opts.pxat !== undefined) args.push("PXAT", String(Math.trunc(opts.pxat)));
    else if (opts.keepTtl) args.push("KEEPTTL");
    if (opts.nx) args.push("NX");
    else if (opts.xx) args.push("XX");
    return this.run(args, (r) => r != null);
  }

  /** Get the value and delete the key. */
  getdel<T = unknown>(key: string): Promise<T | null> {
    return this.run(["GETDEL", this.key(key)], this.dec as Decode<T | null>);
  }

  /** Values of several keys, null for missing ones. Takes names or one array. */
  mget<T extends unknown[] = unknown[]>(...keys: (string | string[])[]): Promise<{ [I in keyof T]: T[I] | null }> {
    return this.run(["MGET", ...this.keyList(keys)], (r) => this.list(r) as { [I in keyof T]: T[I] | null });
  }

  /** Set several keys at once. */
  mset(values: Record<string, unknown>): Promise<void> {
    const args = ["MSET"];
    for (const [k, v] of Object.entries(values)) args.push(this.key(k), this.enc(v));
    return this.run(args, ok);
  }

  /** Delete keys; returns how many existed. */
  del(...keys: (string | string[])[]): Promise<number> {
    const ks = this.keyList(keys);
    return ks.length === 0 ? this.run(["PING"], () => 0) : this.run(["DEL", ...ks], num);
  }

  /** How many of the keys exist. */
  exists(...keys: (string | string[])[]): Promise<number> {
    return this.run(["EXISTS", ...this.keyList(keys)], num);
  }

  /** Expire the key after `seconds`. False when it does not exist. */
  expire(key: string, seconds: number): Promise<boolean> {
    return this.run(["EXPIRE", this.key(key), String(Math.max(1, Math.ceil(seconds)))], bool);
  }

  /** Seconds left: -1 no expiry, -2 no such key. */
  ttl(key: string): Promise<number> {
    return this.run(["TTL", this.key(key)], num);
  }

  /** Remove the expiry. False when there was none (or no key). */
  persist(key: string): Promise<boolean> {
    return this.run(["PERSIST", this.key(key)], bool);
  }

  /** Add 1; returns the new value (a missing key counts as 0). */
  incr(key: string): Promise<number> {
    return this.run(["INCR", this.key(key)], num);
  }

  incrby(key: string, by: number): Promise<number> {
    return this.run(["INCRBY", this.key(key), String(Math.trunc(by))], num);
  }

  incrbyfloat(key: string, by: number): Promise<number> {
    return this.run(["INCRBYFLOAT", this.key(key), String(by)], num);
  }

  decr(key: string): Promise<number> {
    return this.run(["DECR", this.key(key)], num);
  }

  decrby(key: string, by: number): Promise<number> {
    return this.run(["DECRBY", this.key(key), String(Math.trunc(by))], num);
  }

  // Hashes

  /**
   * Set fields; returns how many were new.
   * @example await store.hset("job:1", { status: "running", progress: 0.4 })
   */
  hset(key: string, fields: Record<string, unknown>): Promise<number> {
    const args = ["HSET", this.key(key)];
    for (const [f, v] of Object.entries(fields)) args.push(f, this.enc(v));
    return this.run(args, num);
  }

  hget<T = unknown>(key: string, field: string): Promise<T | null> {
    return this.run(["HGET", this.key(key), field], this.dec as Decode<T | null>);
  }

  /** All fields, or null when the hash does not exist. */
  hgetall<T extends Record<string, unknown> = Record<string, unknown>>(key: string): Promise<T | null> {
    return this.run(["HGETALL", this.key(key)], (r) => {
      const ps = pairs(r);
      return ps.length === 0 ? null : (Object.fromEntries(ps.map(([f, v]) => [f, this.dec(v)])) as T);
    });
  }

  hdel(key: string, ...fields: string[]): Promise<number> {
    return this.run(["HDEL", this.key(key), ...fields], num);
  }

  hincrby(key: string, field: string, by: number): Promise<number> {
    return this.run(["HINCRBY", this.key(key), field, String(Math.trunc(by))], num);
  }

  // Lists

  /** Add to the front; returns the new length. */
  lpush(key: string, ...values: unknown[]): Promise<number> {
    return this.run(["LPUSH", this.key(key), ...values.map((v) => this.enc(v))], num);
  }

  /** Add to the end; returns the new length. */
  rpush(key: string, ...values: unknown[]): Promise<number> {
    return this.run(["RPUSH", this.key(key), ...values.map((v) => this.enc(v))], num);
  }

  /** Items from start to stop, inclusive; -1 is the last. */
  lrange<T = unknown>(key: string, start: number, stop: number): Promise<T[]> {
    return this.run(["LRANGE", this.key(key), String(start), String(stop)], this.list<T>);
  }

  /** Keep only start..stop, e.g. ltrim(key, 0, 99) after lpush for the latest 100. */
  ltrim(key: string, start: number, stop: number): Promise<void> {
    return this.run(["LTRIM", this.key(key), String(start), String(stop)], ok);
  }

  llen(key: string): Promise<number> {
    return this.run(["LLEN", this.key(key)], num);
  }

  /** Remove and return the first item (or the first `count`). */
  lpop<T = unknown>(key: string): Promise<T | null>;
  lpop<T = unknown>(key: string, count: number): Promise<T[] | null>;
  lpop<T = unknown>(key: string, count?: number): Promise<T | T[] | null> {
    return this.pop("LPOP", key, count) as Promise<T | T[] | null>;
  }

  /** Remove and return the last item (or the last `count`). */
  rpop<T = unknown>(key: string): Promise<T | null>;
  rpop<T = unknown>(key: string, count: number): Promise<T[] | null>;
  rpop<T = unknown>(key: string, count?: number): Promise<T | T[] | null> {
    return this.pop("RPOP", key, count) as Promise<T | T[] | null>;
  }

  private pop(cmd: string, key: string, count?: number): Promise<unknown> {
    if (count === undefined) return this.run([cmd, this.key(key)], this.dec);
    return this.run([cmd, this.key(key), String(count)], (r) => (r == null ? null : this.list(r)));
  }

  // Sets

  sadd(key: string, ...members: unknown[]): Promise<number> {
    return this.run(["SADD", this.key(key), ...members.map((v) => this.enc(v))], num);
  }

  srem(key: string, ...members: unknown[]): Promise<number> {
    return this.run(["SREM", this.key(key), ...members.map((v) => this.enc(v))], num);
  }

  smembers<T = unknown>(key: string): Promise<T[]> {
    return this.run(["SMEMBERS", this.key(key)], this.list<T>);
  }

  sismember(key: string, member: unknown): Promise<boolean> {
    return this.run(["SISMEMBER", this.key(key), this.enc(member)], (r) => r === true || Number(r) === 1);
  }

  // Sorted sets

  /**
   * Add members with scores; returns how many were added.
   * @example await store.zadd("leaderboard", { score: 120, member: "ada" }, { score: 80, member: "bob" })
   */
  zadd<T = unknown>(key: string, ...items: (ScoreMember<T> | ZAddOptions)[]): Promise<number> {
    const args = ["ZADD", this.key(key)];
    const opts = (items.find((i) => !("score" in i)) ?? {}) as ZAddOptions;
    if (opts.nx) args.push("NX");
    else if (opts.xx) args.push("XX");
    if (opts.gt) args.push("GT");
    else if (opts.lt) args.push("LT");
    if (opts.ch) args.push("CH");
    for (const i of items) if ("score" in i) args.push(String(i.score), this.enc(i.member));
    return this.run(args, num);
  }

  /**
   * Members from start to stop (ranks, or scores with byScore).
   * @example await store.zrange("leaderboard", 0, 9, { rev: true, withScores: true }) // top 10
   */
  zrange<T = unknown>(key: string, start: number | string, stop: number | string, opts: ZRangeOptions & { withScores: true }): Promise<ScoreMember<T>[]>;
  zrange<T = unknown>(key: string, start: number | string, stop: number | string, opts?: ZRangeOptions & { withScores?: false }): Promise<T[]>;
  zrange<T = unknown>(key: string, start: number | string, stop: number | string, opts: ZRangeOptions & { withScores?: boolean } = {}): Promise<T[] | ScoreMember<T>[]> {
    const args = ["ZRANGE", this.key(key), String(start), String(stop)];
    if (opts.byScore) args.push("BYSCORE");
    if (opts.rev) args.push("REV");
    if (opts.limit) args.push("LIMIT", String(opts.limit.offset), String(opts.limit.count));
    if (!opts.withScores) return this.run(args, this.list<T>);
    args.push("WITHSCORES");
    return this.run(args, (r) => {
      const a = Array.isArray(r) ? r : [];
      if (a.length > 0 && Array.isArray(a[0])) return (a as [unknown, unknown][]).map(([m, s]) => ({ member: this.dec(m) as T, score: Number(s) }));
      const out: ScoreMember<T>[] = [];
      for (let i = 0; i + 1 < a.length; i += 2) out.push({ member: this.dec(a[i]) as T, score: Number(a[i + 1]) });
      return out;
    });
  }

  /** A member's rank (0 = lowest score; with rev, 0 = highest), or null. */
  zrank(key: string, member: unknown, opts: { rev?: boolean } = {}): Promise<number | null> {
    return this.run([opts.rev ? "ZREVRANK" : "ZRANK", this.key(key), this.enc(member)], (r) => (r == null ? null : Number(r)));
  }

  zscore(key: string, member: unknown): Promise<number | null> {
    return this.run(["ZSCORE", this.key(key), this.enc(member)], (r) => (r == null ? null : Number(r)));
  }

  /** Add `by` to a member's score (adding the member at `by`); returns the new score. */
  zincrby(key: string, by: number, member: unknown): Promise<number> {
    return this.run(["ZINCRBY", this.key(key), String(by), this.enc(member)], num);
  }

  zrem(key: string, ...members: unknown[]): Promise<number> {
    return this.run(["ZREM", this.key(key), ...members.map((v) => this.enc(v))], num);
  }

  zcard(key: string): Promise<number> {
    return this.run(["ZCARD", this.key(key)], num);
  }

  // Pub/sub

  /** Publish to a channel (prefixed like keys); returns how many subscribers got it. */
  publish(channel: string, message: unknown): Promise<number> {
    return this.run(["PUBLISH", this.key(channel), this.enc(message)], num);
  }
}

interface Queued {
  args: string[];
  decode: Decode<unknown>;
  resolve(v: unknown): void;
  reject(e: unknown): void;
}

/**
 * Commands sent together in one round trip by exec(); with multi(), as one
 * transaction (all or nothing, nothing in between). Each call returns a
 * promise of its own result, settled by exec(), which also returns them all.
 *
 * ```ts
 * const p = store.pipeline();
 * const views = p.incr("views");
 * p.expire("views", 3600);
 * const [n] = await p.exec<[number, boolean]>();   // or: await views
 * ```
 */
export class Pipeline extends Commands {
  private queue: Queued[] = [];
  constructor(
    private readonly conn: Conn,
    prefix: string,
    json: boolean,
    private readonly tx: boolean,
  ) {
    super(prefix, json);
  }

  protected run<T>(args: string[], decode: Decode<T>): Promise<T> {
    const p = new Promise<T>((resolve, reject) => {
      this.queue.push({ args, decode, resolve: resolve as (v: unknown) => void, reject });
    });
    // A failed command whose promise nobody awaits must not crash the
    // process: its error also comes out of exec().
    p.catch(() => {});
    return p;
  }

  /** How many commands are queued. */
  get length(): number {
    return this.queue.length;
  }

  /** Send the queued commands; the results in order. Throws the first error (after settling every promise). */
  async exec<T extends unknown[] = unknown[]>(): Promise<T> {
    const q = this.queue;
    this.queue = [];
    if (q.length === 0) return [] as unknown as T;
    let replies: unknown[];
    try {
      if (this.tx) {
        const all = await this.conn.batch([["MULTI"], ...q.map((c) => c.args), ["EXEC"]]);
        const exec = all[all.length - 1];
        const queued = all.slice(1, -1).find((r) => r instanceof Error);
        if (exec instanceof Error || !Array.isArray(exec)) throw kvError(queued ?? exec ?? new Error("EXECABORT the transaction was aborted"), "EXEC");
        replies = exec;
      } else {
        replies = await this.conn.batch(q.map((c) => c.args));
      }
    } catch (e) {
      for (const c of q) c.reject(e);
      throw e;
    }
    let first: unknown;
    const out = q.map((c, i) => {
      const r = replies[i];
      if (r instanceof Error) {
        const err = kvError(r, c.args[0]);
        first ??= err;
        c.reject(err);
        return err;
      }
      const v = c.decode(r);
      c.resolve(v);
      return v;
    });
    if (first) throw first;
    return out as T;
  }
}

/** Key-value store bound to one prefix. Get the project's with `kv()`. */
export class KV extends Commands {
  private restScan: { url: string; token: string } | undefined;

  constructor(
    private readonly conn: Conn,
    prefix = "",
    json = true,
  ) {
    super(prefix, json);
  }

  protected run<T>(args: string[], decode: Decode<T>): Promise<T> {
    return this.conn.send(args[0]!, args.slice(1)).then(decode);
  }

  /** The client underneath: Bun's RedisClient, the SDK's RESP client, or the one you passed. */
  get client(): unknown {
    return this.conn.raw;
  }

  /**
   * Any command, as is: no prefix added, reply not decoded. Use `key()` for key names.
   * @example await store.command("HLEN", store.key("job:1"))
   */
  command<T = unknown>(cmd: string, ...args: (string | number)[]): Promise<T> {
    return this.conn.send(cmd, args.map(String)) as Promise<T>;
  }

  /** Queue commands and send them in one round trip with exec(). */
  pipeline(): Pipeline {
    return new Pipeline(this.conn, this.prefix, this.json, false);
  }

  /** Like pipeline(), but exec() runs them as one transaction (MULTI/EXEC). */
  multi(): Pipeline {
    return new Pipeline(this.conn, this.prefix, this.json, true);
  }

  /** The same store without JSON: values are plain strings. */
  raw(): KV {
    const k = new KV(this.conn, this.prefix, false);
    k.restScan = this.restScan;
    return k;
  }

  /**
   * The project's keys matching a glob pattern ("user:*"), prefix removed,
   * a page at a time. On a Tiffin box apps may not run SCAN themselves (it
   * would name other projects' keys), so it goes through the box's KV
   * endpoint, which lists only this project's keys.
   * @example for await (const key of store.scan("session:*")) await store.del(key)
   */
  async *scan(match = "*", opts: ScanOptions = {}): AsyncGenerator<string> {
    let cursor = "0";
    do {
      const [next, keys] = await this.scanPage(cursor, match, opts);
      cursor = next;
      yield* keys;
    } while (cursor !== "0");
  }

  /** Every key matching the pattern (a scan collected into an array). */
  async keys(match = "*"): Promise<string[]> {
    const out: string[] = [];
    for await (const k of this.scan(match, { count: 1000 })) out.push(k);
    return out;
  }

  private async scanPage(cursor: string, match: string, opts: ScanOptions): Promise<[string, string[]]> {
    const tail = ["COUNT", String(opts.count ?? 100), ...(opts.type ? ["TYPE", opts.type] : [])];
    if (!this.restScan) {
      try {
        const r = (await this.conn.send("SCAN", [cursor, "MATCH", globEscape(this.prefix) + match, ...tail])) as [unknown, unknown[]];
        return [String(r[0]), r[1].map((k) => String(k).slice(this.prefix.length))];
      } catch (e) {
        const rest = boxREST(this.prefix);
        if (!(e instanceof KVError && e.code === "NOPERM" && rest)) throw e;
        this.restScan = rest;
      }
    }
    const res = await fetch(this.restScan.url, {
      method: "POST",
      headers: { authorization: `Bearer ${this.restScan.token}`, "content-type": "application/json" },
      body: JSON.stringify(["SCAN", cursor, "MATCH", match, ...tail]),
      signal: AbortSignal.timeout(10_000),
    });
    const body = (await res.json().catch(() => ({}))) as { result?: [string, string[]]; error?: string };
    if (!res.ok || !body.result) throw kvError(new Error(body.error ?? `KV endpoint answered ${res.status}`), "SCAN");
    return [String(body.result[0]), body.result[1]];
  }

  /**
   * Allow at most `limit` calls per `window` for `key` (say a user id or IP).
   * Atomic in Valkey (one Lua script), so parallel requests can't slip past,
   * and app instances share it. Refused calls don't count.
   * @example
   * const rl = await store.rateLimit(`api:${userId}`, { limit: 100, window: "1 m" });
   * if (!rl.allowed) return new Response("Too many requests", { status: 429, headers: { "retry-after": String(rl.retryAfter) } });
   */
  async rateLimit(key: string, opts: RateLimitOptions): Promise<RateLimitResult> {
    const limit = Math.max(0, Math.trunc(opts.limit));
    const w = windowMs(opts.window);
    const cost = Math.max(1, Math.trunc(opts.cost ?? 1));
    const fixed = opts.algorithm === "fixed";
    const args = ["1", this.key(`ratelimit:${key}`), String(limit), String(w), String(cost), fixed ? "fixed" : "sliding"];
    let r: unknown;
    try {
      r = await this.conn.send("EVALSHA", [RATE_LIMIT_SHA, ...args]);
    } catch (e) {
      if (!(e instanceof KVError && e.code === "NOSCRIPT")) throw e;
      r = await this.conn.send("EVAL", [RATE_LIMIT_LUA, ...args]);
    }
    return rateLimitResult(r as unknown[], limit, w, cost, fixed);
  }

  /**
   * The value of `fn()`, kept for `ttl` seconds. When it expires, one caller
   * refreshes it (a short lock) while the others get the old value, for up to
   * `stale` more seconds; on a cold start the others wait for that one
   * instead of all running `fn`. Values must be JSON.
   * @example const stats = await store.cached("stats", 300, () => computeStats())
   */
  async cached<T>(key: string, ttl: number, fn: () => Promise<T> | T, opts: CachedOptions = {}): Promise<T> {
    const stale = Math.max(0, opts.stale ?? ttl);
    const k = `cached:${key}`;
    const lock = `${k}:lock`;
    const lockSec = Math.max(5, Math.min(60, Math.ceil(ttl)));
    const read = async () => {
      const raw = await this.conn.send("GET", [this.key(k)]);
      return raw == null ? undefined : (JSON.parse(String(raw)) as { v: T; t: number });
    };
    const refresh = async () => {
      try {
        const v = await fn();
        await this.conn.send("SET", [this.key(k), JSON.stringify({ v, t: Date.now() + ttl * 1000 }), "PX", String(Math.ceil((ttl + stale) * 1000))]);
        return v;
      } finally {
        await this.conn.send("DEL", [this.key(lock)]).catch(() => {});
      }
    };
    const take = async () => (await this.conn.send("SET", [this.key(lock), "1", "NX", "EX", String(lockSec)])) != null;

    let hit = await read();
    if (hit && hit.t > Date.now()) return hit.v;
    if (hit) {
      // Stale: one caller refreshes in the background, everyone gets the old value now.
      if (await take()) void refresh().catch(() => {});
      return hit.v;
    }
    // Cold: one caller computes it; the others wait for its value (or take
    // over if it failed and let the lock go).
    for (const deadline = Date.now() + lockSec * 1000; ; ) {
      if (await take()) return refresh();
      if (Date.now() >= deadline) return fn();
      await new Promise((r) => setTimeout(r, 50));
      hit = await read();
      if (hit) return hit.v;
    }
  }

  /** Close the connection (scripts don't need to: an idle connection lets the process exit). */
  close(): void {
    this.conn.close();
  }
}

/** The box's KV endpoint, when its env is the box's own (its token names this project). */
function boxREST(prefix: string): { url: string; token: string } | undefined {
  const env = typeof process === "undefined" ? {} : process.env;
  const url = env.KV_REST_API_URL ?? env.UPSTASH_REDIS_REST_URL;
  const token = env.KV_REST_API_TOKEN ?? env.UPSTASH_REDIS_REST_TOKEN;
  const project = /^p_(.+):$/.exec(prefix)?.[1];
  if (!url || !token || !project || !token.startsWith(`tvk_${project.replace(/_/g, "-")}_`)) return undefined;
  return { url, token };
}

function globEscape(s: string): string {
  return s.replace(/[*?[\]\\]/g, "\\$&");
}

let shared: KV | undefined;

/**
 * The project's KV store. Without options: REDIS_URL and VALKEY_PREFIX from
 * the environment (the box sets both for apps with `services.valkey`), one
 * shared connection per process. With options: a new store.
 *
 * @example
 * import { kv } from "@shiptiffin/sdk/kv";
 * await kv().set("hello", "world", { ex: 60 });
 */
export function kv(opts?: KVOptions): KV {
  if (!opts && shared) return shared;
  const o = opts ?? {};
  const env = typeof process === "undefined" ? {} : process.env;
  const prefix = o.prefix ?? env.VALKEY_PREFIX ?? "";
  const json = o.json ?? true;
  let conn: Conn;
  if (o.client) conn = new ClientConn(o.client);
  else {
    const url = o.url ?? env.REDIS_URL;
    if (!url) throw new KVError("KV: REDIS_URL is not set. Add services.valkey to tiffin.config.ts (the box sets it for your apps), or pass { url }.", "CONFIG");
    const timeout = o.timeoutMs ?? 5000;
    const Bun = bunRedis();
    conn = Bun && o.driver !== "resp" ? new BunConn(url, timeout, Bun) : new RespConn(url, timeout);
  }
  const store = new KV(conn, prefix, json);
  if (!opts) shared = store;
  return store;
}
