/**
 * `tiffin-sdk/kv`: small helpers over the project's Valkey namespace, using
 * Bun's built-in Redis client.
 *
 * ```ts
 * import { kv } from "tiffin-sdk/kv";
 *
 * const store = kv();
 * await store.set("greeting", "hello", { ttl: 60 });   // seconds
 * await store.get("greeting");                         // "hello"
 * await store.setJSON("user:1", { name: "Ada" });
 * await store.getJSON<{ name: string }>("user:1");
 * await store.incr("visits");
 * const rl = await store.rateLimit(`login:${ip}`, { limit: 5, windowSec: 60 });
 * if (!rl.allowed) return new Response("slow down", { status: 429, headers: { "retry-after": String(rl.retryAfterSec) } });
 * ```
 *
 * Every key is prefixed with VALKEY_PREFIX ("p_<project>:"): the box gives
 * each project an ACL user that can only touch its own prefix, so these
 * helpers add it for you. With your own client, prefix keys yourself.
 * The connection defaults to `new Bun.RedisClient(process.env.REDIS_URL)`.
 */
// Fixed-window counter in one atomic step: INCR, set the window's expiry on
// the first hit, report the count and the time left.
const RATE_LIMIT_LUA = `local n = redis.call('INCR', KEYS[1])
if n == 1 or redis.call('PTTL', KEYS[1]) < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return {n, redis.call('PTTL', KEYS[1])}`;
/** Key/value helpers bound to one prefix. */
export class KV {
    client;
    prefix;
    constructor(client, prefix) {
        this.client = client;
        this.prefix = prefix;
    }
    /** The full key in Valkey: prefix + key. */
    key(k) {
        return k.startsWith(this.prefix) ? k : this.prefix + k;
    }
    async get(key) {
        const v = await this.client.send("GET", [this.key(key)]);
        return v == null ? null : String(v);
    }
    /** Set a string. Returns false when nx was set and the key existed. */
    async set(key, value, opts = {}) {
        const args = [this.key(key), value];
        if (opts.ttl !== undefined)
            args.push("EX", String(Math.max(1, Math.ceil(opts.ttl))));
        if (opts.nx)
            args.push("NX");
        const r = await this.client.send("SET", args);
        return r != null;
    }
    async getJSON(key) {
        const v = await this.get(key);
        return v == null ? null : JSON.parse(v);
    }
    async setJSON(key, value, opts = {}) {
        return this.set(key, JSON.stringify(value), opts);
    }
    /** Delete keys. Returns how many existed. */
    async del(...keys) {
        if (keys.length === 0)
            return 0;
        return Number(await this.client.send("DEL", keys.map((k) => this.key(k))));
    }
    /** Add `by` (default 1) and return the new value. */
    async incr(key, by = 1) {
        return Number(await this.client.send("INCRBY", [this.key(key), String(Math.trunc(by))]));
    }
    /** Set a key's expiry in seconds. Returns false when the key does not exist. */
    async expire(key, ttl) {
        return Number(await this.client.send("EXPIRE", [this.key(key), String(Math.max(1, Math.ceil(ttl)))])) === 1;
    }
    /** Seconds until the key expires: -1 never, -2 missing. */
    async ttl(key) {
        return Number(await this.client.send("TTL", [this.key(key)]));
    }
    /**
     * Fixed-window rate limit: at most `limit` hits per `windowSec` for `key`.
     * Atomic (one Lua script), so concurrent requests can't slip past.
     */
    async rateLimit(key, opts) {
        const limit = Math.max(1, Math.trunc(opts.limit));
        const windowMs = Math.max(1, Math.ceil(opts.windowSec * 1000));
        const r = (await this.client.send("EVAL", [RATE_LIMIT_LUA, "1", this.key("ratelimit:" + key), String(windowMs)]));
        const count = Number(r?.[0] ?? 0);
        const pttl = Math.max(0, Number(r?.[1] ?? windowMs));
        return { allowed: count <= limit, remaining: Math.max(0, limit - count), limit, retryAfterSec: Math.ceil(pttl / 1000) };
    }
    /** Remember the result of fn for ttl seconds (JSON-serialisable values). */
    async cached(key, ttl, fn) {
        const hit = await this.get(key);
        if (hit != null)
            return JSON.parse(hit);
        const value = await fn();
        await this.setJSON(key, value, { ttl });
        return value;
    }
}
let shared = null;
/**
 * The project's KV namespace. Without arguments it connects with
 * `new Bun.RedisClient(process.env.REDIS_URL)` once and uses VALKEY_PREFIX.
 */
export function kv(client, prefix = process.env.VALKEY_PREFIX ?? "") {
    if (client)
        return new KV(client, prefix);
    if (!shared) {
        const url = process.env.REDIS_URL;
        if (!url)
            throw new Error("REDIS_URL is not set: add services.valkey to tiffin.config.ts (the box sets it for your apps)");
        const B = globalThis.Bun;
        if (!B?.RedisClient)
            throw new Error("tiffin-sdk/kv needs Bun.redis; on another runtime pass a client with send(command, args)");
        shared = new KV(new B.RedisClient(url), prefix);
    }
    return shared;
}
