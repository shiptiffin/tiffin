/**
 * The storage behind `@shiptiffin/sdk/next`'s cache handlers: entries and tag
 * revalidations in Valkey, shared by every instance of the app, with a
 * bounded in-process copy in front.
 *
 * Keys (all under VALKEY_PREFIX, the project's ACL prefix):
 *   <prefix>next:<app>:<env>:<deploy>:e:<key>   incremental cache entries (ISR, route handlers, fetch)
 *   <prefix>next:<app>:<env>:<deploy>:u:<key>   "use cache" entries
 *   <prefix>next:<app>:<env>:tags               hash: s:<tag> / x:<tag> → when the tag went stale / expires (ms)
 *   <prefix>next:<app>:<env>:tagv               counter, bumped on every revalidation
 *
 * <env> is "prod" or "pr-<preview>", <deploy> the deploy (TIFFIN_DEPLOY). Next.js
 * gives every build the same BUILD_ID once deploymentId is set, so entries are
 * keyed by deploy: a new release or a preview never serves pages another one
 * rendered, and a rollback finds its own entries again. Tags are shared by all
 * deploys of an environment, so a revalidation outlives a redeploy.
 *
 * Entries are stored as bytes (a JSON header plus raw buffers), not base64 JSON.
 * Each request reads the tag counter (one small GET, pipelined with the entry
 * read); the tag hash is fetched only when the counter moved.
 */
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { ReplyError, RespClient } from "../resp.js";
/** Wraps an ioredis client (`call`) or a node-redis v4+ client (`sendCommand`). */
export function toRedisLike(client) {
    const c = client;
    if (typeof c.send === "function")
        return client;
    // One wrapper per client, so the handlers keep sharing one Store for it.
    let w = wrappers.get(c);
    if (w)
        return w;
    if (typeof c.call === "function") {
        w = { send: (cmd, args) => c.call.call(client, cmd, ...args) };
    }
    else if (typeof c.sendCommand === "function") {
        w = { send: (cmd, args) => c.sendCommand.call(client, [cmd, ...args]) };
    }
    else {
        throw new TypeError("@shiptiffin/sdk/next: pass a Bun RedisClient, an ioredis client or a node-redis client");
    }
    wrappers.set(c, w);
    return w;
}
const wrappers = new WeakMap();
/**
 * Deletes tag fields (ARGV: field, value, ...) of the hash KEYS[1] that still
 * hold the value they were read with: a field another instance rewrote
 * meanwhile (a new revalidation) stays.
 */
export const PRUNE_SCRIPT = `local n = 0
for i = 1, #ARGV, 2 do
  if redis.call('HGET', KEYS[1], ARGV[i]) == ARGV[i + 1] then n = n + redis.call('HDEL', KEYS[1], ARGV[i]) end
end
return n`;
/**
 * An in-memory stand-in supporting exactly the commands the handlers send.
 * Used when no REDIS_URL is set (local dev, `next build`): caching then
 * works per process, like Next.js's default.
 */
export function memoryRedis() {
    const strings = new Map();
    const hashes = new Map();
    const live = (k) => {
        const e = strings.get(k);
        if (e && e.exp && Date.now() > e.exp) {
            strings.delete(k);
            return undefined;
        }
        return e;
    };
    const str = (a) => (typeof a === "string" ? a : Buffer.from(a ?? []).toString());
    const send = async (cmd, a) => {
        const set = cmd.toUpperCase() === "SET";
        const args = a.map((x, i) => (set && i === 1 ? "" : str(x))); // a SET value stays bytes
        switch (cmd.toUpperCase()) {
            case "GET":
                return live(args[0])?.v ?? null;
            case "MGET":
                return args.map((k) => live(k)?.v ?? null);
            case "SET": {
                let exp = 0;
                const i = args.findIndex((x) => x.toUpperCase() === "EX");
                if (i >= 0)
                    exp = Date.now() + Number(args[i + 1]) * 1000;
                if (args.some((x) => x.toUpperCase() === "NX") && live(args[0]))
                    return null;
                const v = a[1];
                strings.set(args[0], { v: typeof v === "string" ? v : Buffer.from(v), exp });
                return "OK";
            }
            case "DEL":
                return args.reduce((n, k) => n + (strings.delete(k) || hashes.delete(k) ? 1 : 0), 0);
            case "INCR": {
                const n = Number(str(live(args[0])?.v ?? "0")) + 1;
                strings.set(args[0], { v: String(n), exp: 0 });
                return n;
            }
            case "HSET": {
                const h = hashes.get(args[0]) ?? new Map();
                for (let i = 1; i + 1 < args.length; i += 2)
                    h.set(args[i], args[i + 1]);
                hashes.set(args[0], h);
                return (args.length - 1) / 2;
            }
            case "HDEL": {
                const h = hashes.get(args[0]);
                return args.slice(1).reduce((n, f) => n + (h?.delete(f) ? 1 : 0), 0);
            }
            case "HMGET": {
                const h = hashes.get(args[0]);
                return args.slice(1).map((f) => h?.get(f) ?? null);
            }
            case "HGETALL":
                return Object.fromEntries(hashes.get(args[0]) ?? []);
            case "EVAL": {
                if (args[0] !== PRUNE_SCRIPT || args[1] !== "1")
                    throw new Error("memoryRedis: unsupported script");
                const h = hashes.get(args[2]);
                let n = 0;
                for (let i = 3; i + 1 < args.length; i += 2)
                    if (h && h.get(args[i]) === args[i + 1] && h.delete(args[i]))
                        n++;
                return n;
            }
            default:
                throw new Error(`memoryRedis: unsupported command ${cmd}`);
        }
    };
    const client = { binary: true, send };
    return client;
}
let shared;
/**
 * The client the handlers use by default: a connection to REDIS_URL (Tiffin
 * sets it when the project has `services.valkey`) on Bun or Node, or an
 * in-memory store when REDIS_URL is unset and during `next build`.
 */
export function defaultClient() {
    if (shared)
        return shared;
    const url = process.env.REDIS_URL ?? process.env.VALKEY_URL;
    shared = url && process.env.NEXT_PHASE !== "phase-production-build" ? new RespClient(url) : memoryRedis();
    return shared;
}
const warned = new Set();
export function warnOnce(msg) {
    if (warned.has(msg))
        return;
    warned.add(msg);
    console.warn("[@shiptiffin/sdk/next] " + msg);
}
let options = {};
/** Configure both handlers, e.g. to pass your own client. Call before Next.js starts serving. */
export function configure(o) {
    options = { ...options, ...o };
    if (o.client)
        shared = toRedisLike(o.client);
}
let nextManifest;
/**
 * Next.js's process-wide tag state (`tags-manifest.external`). Its own cache
 * checks the tags of pages and route handlers against it after our get()
 * returns, which is how a stale (not expired) tag makes it serve the entry and
 * regenerate in the background.
 */
function findNextManifest() {
    if (nextManifest !== undefined)
        return nextManifest;
    nextManifest = null;
    for (const from of [`${process.cwd()}/package.json`, import.meta.url]) {
        try {
            const m = createRequire(from)("next/dist/server/lib/incremental-cache/tags-manifest.external.js");
            if (m.tagsManifest instanceof Map)
                return (nextManifest = m.tagsManifest);
        }
        catch {
            // not resolvable from here
        }
    }
    return nextManifest;
}
/** How long an in-memory copy is trusted at most, so instances converge after concurrent writes. */
const TRUST_MS = 10_000;
class Lru {
    max;
    map = new Map();
    bytes = 0;
    constructor(max) {
        this.max = max;
    }
    get(k) {
        const v = this.map.get(k);
        if (!v)
            return undefined;
        if (Date.now() > v.until) {
            this.delete(k);
            return undefined;
        }
        this.map.delete(k);
        this.map.set(k, v);
        return v;
    }
    set(k, v) {
        this.delete(k);
        if (v.size > this.max / 4)
            return;
        this.map.set(k, v);
        this.bytes += v.size;
        for (const [old, e] of this.map) {
            if (this.bytes <= this.max)
                break;
            this.map.delete(old);
            this.bytes -= e.size;
        }
    }
    delete(k) {
        const v = this.map.get(k);
        if (!v)
            return;
        this.map.delete(k);
        this.bytes -= v.size;
    }
}
const stores = new WeakMap();
let cwdBuildId = null;
/** Options with the defaults filled in from the environment. */
function resolve(o) {
    const opt = { ...options, ...o };
    if (cwdBuildId === null)
        cwdBuildId = readBuildId();
    return {
        ...opt,
        client: opt.client ? toRedisLike(opt.client) : defaultClient(),
        prefix: opt.prefix ?? `${process.env.VALKEY_PREFIX ?? ""}next:${process.env.TIFFIN_APP ?? "app"}:`,
        environment: opt.environment ?? (process.env.TIFFIN_PREVIEW ? `pr-${process.env.TIFFIN_PREVIEW}` : "prod"),
        buildId: opt.buildId ?? process.env.TIFFIN_DEPLOY ?? (process.env.NEXT_DEPLOYMENT_ID || undefined) ?? cwdBuildId ?? "dev",
    };
}
export class Store {
    client;
    prefix;
    environment;
    buildId;
    maxTtl;
    /** Tag states of this environment, as of `version`. */
    tagTimes = new Map();
    /** The tag counter value the map reflects (undefined: re-read the hash). */
    version;
    applied = 0;
    syncing;
    /** This process's revalidations: a sequence number, the last one Valkey confirmed, and per tag the latest. */
    seq = 0;
    acked = 0;
    own = new Map();
    lru;
    reads = new Map();
    writes = new Map();
    manifest;
    /** The store for these options, shared by every handler in the process. */
    static open(o = {}) {
        const opt = resolve(o);
        const id = [opt.prefix, opt.environment, opt.buildId, opt.maxTtlSeconds, opt.memoryBytes].join("|");
        let m = stores.get(opt.client);
        if (!m)
            stores.set(opt.client, (m = new Map()));
        let s = m.get(id);
        if (!s)
            m.set(id, (s = new Store(opt)));
        return s;
    }
    constructor(o = {}) {
        const opt = resolve(o);
        this.client = opt.client;
        this.prefix = opt.prefix;
        this.environment = opt.environment;
        this.buildId = opt.buildId;
        this.maxTtl = opt.maxTtlSeconds ?? 30 * 24 * 3600;
        this.lru = new Lru(opt.memoryBytes ?? 32 << 20);
        this.manifest = opt.tagsManifest !== undefined ? opt.tagsManifest : findNextManifest();
    }
    /** Whether Next.js itself can be told a tag is stale (otherwise stale counts as expired). */
    get signalsStale() {
        return this.manifest != null;
    }
    entryKey(kind, key) {
        return `${this.prefix}${this.environment}:${this.buildId}:${kind}:${key}`;
    }
    get tagsKey() {
        return `${this.prefix}${this.environment}:tags`;
    }
    get versionKey() {
        return `${this.prefix}${this.environment}:tagv`;
    }
    get binary() {
        return this.client.binary === true;
    }
    send(cmd, args) {
        if (this.binary)
            return this.client.send(cmd, args);
        return this.client.send(cmd, args);
    }
    pipeline(cmds) {
        const c = this.client;
        if (c.binary && c.pipeline) {
            return c.pipeline(cmds).then((rs) => {
                const err = rs.find((r) => r instanceof ReplyError);
                if (err)
                    throw err;
                return rs;
            });
        }
        return Promise.all(cmds.map(([cmd, ...args]) => this.send(cmd, args)));
    }
    /**
     * Reads an entry: the in-memory copy, else Valkey (one read per key at a
     * time). `fromValkey` skips the in-memory copy, for when it turned out to be
     * outdated by a revalidation (another instance may have rendered anew).
     */
    async read(kind, key, fromValkey = false) {
        const k = this.entryKey(kind, key);
        await this.writes.get(k);
        if (fromValkey)
            this.lru.delete(k);
        const hit = this.lru.get(k);
        if (hit)
            return hit;
        const joined = fromValkey ? undefined : this.reads.get(k);
        if (joined)
            return (await joined);
        const p = this.send("GET", [k])
            .then((raw) => {
            const buf = this.bytes(raw);
            if (!buf)
                return undefined;
            const { meta, value } = decode(buf);
            const item = { meta, value, size: buf.length, until: Math.min(Number(meta.fresh ?? Infinity), Date.now() + TRUST_MS) };
            this.lru.set(k, item);
            return item;
        })
            .finally(() => {
            if (this.reads.get(k) === p)
                this.reads.delete(k);
        });
        this.reads.set(k, p);
        return (await p);
    }
    /**
     * Stores an entry in Valkey. `meta.fresh` (ms) is when it goes stale; until then a local copy needs no check.
     * `ifAbsent` leaves an entry already there alone (SET NX).
     */
    async write(kind, key, meta, value, ttlSeconds, ifAbsent = false) {
        const k = this.entryKey(kind, key);
        this.lru.delete(k);
        const frame = encode(meta, value);
        const ttl = Math.max(1, Math.min(Math.floor(ttlSeconds) || this.maxTtl, this.maxTtl));
        const args = [k, this.binary ? frame : frame.toString("base64"), "EX", String(ttl)];
        if (ifAbsent)
            args.push("NX");
        const p = this.send("SET", args).then(() => { });
        // Reads of this key in this process wait for the write.
        const done = p.catch(() => { }).finally(() => {
            if (this.writes.get(k) === done)
                this.writes.delete(k);
        });
        this.writes.set(k, done);
        await p;
    }
    async del(kind, key) {
        const k = this.entryKey(kind, key);
        this.lru.delete(k);
        await this.send("DEL", [k]);
    }
    /**
     * Whether an entry made at `at` can be checked against the tag state: tag
     * fields older than the longest TTL are pruned, so an entry older than
     * that (a page `next build` prerendered long ago, or its copy) may have
     * been revalidated since without a trace. Such an entry counts as a miss.
     */
    trusted(at) {
        return at >= Date.now() - this.maxTtl * 1000;
    }
    /** Mirrors Next.js's areTagsExpired: a tag expired (by now) after the entry was made. */
    expired(tags, at) {
        const now = Date.now();
        for (const t of tags) {
            const x = this.tagTimes.get(t)?.expired;
            if (x !== undefined && x <= now && x > at)
                return true;
        }
        return false;
    }
    /** Mirrors Next.js's areTagsStale: a tag was marked stale after the entry was made. */
    stale(tags, at) {
        for (const t of tags) {
            const s = this.tagTimes.get(t)?.stale;
            if (s !== undefined && s > at)
                return true;
        }
        return false;
    }
    /**
     * Revalidates tags on every instance, the way Next.js's own handlers do:
     * without durations they expire now; with durations they are stale now
     * (served once more while regenerating) and expire after `expire` seconds.
     */
    async updateTags(tags, durations) {
        const uniq = [...new Set(tags)].filter(Boolean);
        if (uniq.length === 0)
            return;
        const now = Date.now();
        const seq = ++this.seq;
        const fields = [];
        for (const t of uniq) {
            const next = { ...this.tagTimes.get(t) };
            if (durations) {
                next.stale = now;
                fields.push(`s:${t}`, String(now));
                if (durations.expire !== undefined) {
                    next.expired = now + durations.expire * 1000;
                    fields.push(`x:${t}`, String(next.expired));
                }
            }
            else {
                next.expired = now;
                fields.push(`x:${t}`, String(now));
            }
            this.setTag(t, next); // read-your-writes in this process right away
            this.own.set(t, seq);
        }
        const before = this.version;
        const [, v] = await this.pipeline([
            ["HSET", this.tagsKey, ...fields],
            ["INCR", this.versionKey],
        ]);
        this.acked = Math.max(this.acked, seq);
        // Nobody else revalidated since our map was current: it still is.
        if (before !== undefined && this.version === before && Number(v) === before + 1)
            this.version = before + 1;
    }
    /**
     * Brings the tag map up to date: one GET of the counter, the hash only when
     * it moved. Calls in the same tick share the GET (it has not gone out yet);
     * later ones send their own, so a request sees every revalidation that
     * finished before it began.
     */
    sync() {
        if (this.syncing)
            return this.syncing;
        const p = this.doSync();
        this.syncing = p;
        queueMicrotask(() => {
            if (this.syncing === p)
                this.syncing = undefined;
        });
        return p;
    }
    async doSync() {
        const v = Number(this.text(await this.send("GET", [this.versionKey])) ?? 0);
        if (v === this.version || v < this.applied)
            return;
        // Revalidations from here that Valkey had confirmed are in the hash we read.
        const acked = this.acked;
        const seq = this.seq;
        const raw = await this.send("HGETALL", [this.tagsKey]);
        if (v < this.applied)
            return; // a newer read finished first
        this.applied = v;
        const old = Date.now() - this.maxTtl * 1000;
        const fresh = new Map();
        const prune = [];
        for (const [f, val] of pairs(raw)) {
            const field = this.text(f) ?? "";
            const n = Number(this.text(val));
            // Every entry made before this is gone (TTL), or no longer trusted (see
            // trusted), so the field no longer matters.
            if (!(n >= old)) {
                prune.push(field, this.text(val) ?? "");
                continue;
            }
            const tag = field.slice(2);
            const st = fresh.get(tag) ?? {};
            if (field.startsWith("s:"))
                st.stale = n;
            else if (field.startsWith("x:"))
                st.expired = n;
            fresh.set(tag, st);
        }
        // This process's own revalidations that may not be in what we read stay as they are.
        const mine = (t) => (this.own.get(t) ?? 0) > acked;
        for (const t of this.tagTimes.keys())
            if (!fresh.has(t) && !mine(t))
                this.tagTimes.delete(t);
        for (const [t, st] of fresh)
            if (!mine(t))
                this.setTag(t, st);
        for (const [t, s] of this.own)
            if (s <= acked)
                this.own.delete(t);
        this.version = acked === seq && this.seq === seq ? v : undefined;
        // Only if unchanged since read: another instance may have revalidated the tag again.
        if (prune.length > 0)
            this.send("EVAL", [PRUNE_SCRIPT, "1", this.tagsKey, ...prune]).catch(() => { });
    }
    setTag(tag, st) {
        this.tagTimes.set(tag, st);
        if (this.manifest)
            this.manifest.set(tag, { ...this.manifest.get(tag), ...st });
    }
    text(v) {
        if (v == null)
            return undefined;
        return typeof v === "string" ? v : Buffer.isBuffer(v) || v instanceof Uint8Array ? Buffer.from(v).toString() : String(v);
    }
    bytes(raw) {
        if (raw == null || raw === "")
            return undefined;
        if (typeof raw === "string")
            return Buffer.from(raw, this.binary ? "utf8" : "base64");
        if (raw instanceof Uint8Array)
            return Buffer.isBuffer(raw) ? raw : Buffer.from(raw.buffer, raw.byteOffset, raw.byteLength);
        return undefined;
    }
}
function pairs(raw) {
    if (Array.isArray(raw))
        return Array.from({ length: raw.length / 2 }, (_, i) => [raw[2 * i], raw[2 * i + 1]]);
    if (raw instanceof Map)
        return [...raw.entries()];
    return Object.entries((raw ?? {}));
}
/** Reads Next.js's BUILD_ID from a .next directory (default: ./.next). */
export function readBuildId(distDir = `${process.cwd()}/.next`) {
    try {
        return readFileSync(`${distDir}/BUILD_ID`, "utf8").trim() || undefined;
    }
    catch {
        return undefined;
    }
}
// Entry format: 1 version byte, u32 header length, the JSON header, then
// each binary part as u32 length + bytes. Buffers and long strings in the
// value become parts (marked in the JSON), Maps a marked list of entries, so
// page bodies are never base64'd or JSON-escaped.
const FORMAT = 1;
const LONG = 1024;
const B = "\u0000b";
const S = "\u0000s";
const M = "\u0000m";
function pack(v, parts) {
    if (typeof v === "string") {
        if (v.length < LONG)
            return v;
        parts.push(Buffer.from(v));
        return { [S]: parts.length - 1 };
    }
    if (v === null || typeof v !== "object")
        return v;
    if (v instanceof Uint8Array) {
        parts.push(Buffer.from(v.buffer, v.byteOffset, v.byteLength));
        return { [B]: parts.length - 1 };
    }
    if (v instanceof Map)
        return { [M]: [...v].map(([k, x]) => [k, pack(x, parts)]) };
    if (Array.isArray(v))
        return v.map((x) => pack(x, parts));
    if (typeof v.toJSON === "function")
        return v;
    const o = {};
    for (const [k, x] of Object.entries(v))
        if (x !== undefined && typeof x !== "function")
            o[k] = pack(x, parts);
    return o;
}
export function encode(meta, value) {
    const parts = [];
    const head = Buffer.from(JSON.stringify({ m: meta, v: pack(value, parts) }));
    let size = 5 + head.length;
    for (const p of parts)
        size += 4 + p.length;
    const out = Buffer.allocUnsafe(size);
    out[0] = FORMAT;
    out.writeUInt32LE(head.length, 1);
    head.copy(out, 5);
    let at = 5 + head.length;
    for (const p of parts) {
        out.writeUInt32LE(p.length, at);
        p.copy(out, at + 4);
        at += 4 + p.length;
    }
    return out;
}
export function decode(buf) {
    if (buf[0] !== FORMAT)
        throw new Error("unknown cache entry format");
    const n = buf.readUInt32LE(1);
    const parts = [];
    for (let at = 5 + n; at < buf.length;) {
        const len = buf.readUInt32LE(at);
        parts.push(buf.subarray(at + 4, at + 4 + len));
        at += 4 + len;
    }
    const doc = JSON.parse(buf.toString("utf8", 5, 5 + n), (_k, v) => {
        if (v && typeof v === "object" && !Array.isArray(v)) {
            if (B in v)
                return parts[v[B]];
            if (S in v)
                return parts[v[S]].toString();
            if (M in v)
                return new Map(v[M]);
        }
        return v;
    });
    return { meta: doc.m, value: doc.v };
}
