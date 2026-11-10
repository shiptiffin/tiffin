/**
 * `@shiptiffin/astro/cache-provider`: Astro route caching (`Astro.cache`,
 * `routeRules`) stored in the box's Valkey, so every instance of the app
 * shares one cache and it outlives restarts and deploys.
 *
 * A runtime provider (`onRequest`), like Astro's own `memoryCache()`, with the
 * same rules: GET only; responses carry `X-Astro-Cache: HIT | STALE | MISS`;
 * a response with Set-Cookie, `Vary: Cookie` or `Vary: *` is not stored;
 * query strings are sorted and common tracking parameters dropped from the key;
 * other `Vary` headers key the entry per value.
 *
 * Keys (under VALKEY_PREFIX, then `namespace`, default "astro-cache:"):
 *   <ns><app>:<env>:e:<sha256 of the request key>   the entry (JSON, body base64), expires after maxAge + swr
 *   <ns><app>:<env>:v:<sha256 of the URL key>       the Vary header names learned for that URL
 *   <ns><app>:<env>:t:<tag>                          when the tag was last invalidated (ms)
 *   <ns><app>:<env>:l:<sha256 of the request key>   a short lock: one instance revalidates a stale entry
 * <app> is TIFFIN_APP and <env> "prod" or "pr-<preview>", so previews never
 * share entries or invalidations with production. Deploys share them: a new
 * release serves entries the old one stored until they expire or are invalidated.
 *
 * Invalidation is by time, so it reaches every instance without listing keys
 * (the box does not let apps SCAN): `invalidate({ tags })` records "now" for
 * each tag; `invalidate({ path })` does the same for the tag `astro-path:<path>`
 * that every entry carries. An entry is served only when it was rendered
 * (render start, not end, so a render racing an invalidation loses) after every
 * one of its tags was last invalidated. Tag records expire after `maxTtl`, and
 * no entry is kept longer than that, so an expired record can't let an old
 * entry back in.
 *
 * Valkey errors and slow replies (over `timeoutMs`) count as a miss: the page
 * renders as if there were no cache, a warning is logged (at most every 30 s)
 * and Valkey is skipped for 5 s. Stores happen after the response is sent.
 * `invalidate()` is the exception: it throws when Valkey can't record it,
 * because a silent failure would keep serving the content it meant to remove.
 */
import { createHash } from "node:crypto";
import { boxStore, appScope, type Store } from "./store";

export interface ValkeyCacheConfig {
  /** Key namespace after VALKEY_PREFIX (default "astro-cache:"). */
  namespace?: string;
  /** Longest an entry or a tag invalidation is kept, in seconds (default 7 days). Entries with a longer maxAge + swr are cut to it. */
  maxTtl?: number;
  /** Largest body stored, in bytes (default 1 MB). Bigger responses are served but not cached. */
  maxBodyBytes?: number;
  /** A Valkey read slower than this is a miss (default 1000 ms). */
  timeoutMs?: number;
  /** Query parameters: same meaning as Astro's memoryCache `query` option. */
  query?: { include?: string[]; exclude?: string[]; sort?: boolean };
}

/** Astro's provider contract (astro/dist/core/cache/types.d.ts, CacheProvider). */
export interface CacheProvider {
  name: string;
  onRequest(
    context: { request: Request; url: URL; waitUntil?: (p: Promise<unknown>) => void; logger?: { info(m: string): void; warn(m: string): void; error(m: string): void } },
    next: () => Promise<Response>,
  ): Promise<Response>;
  invalidate(options: { path?: string; tags?: string | string[] }): Promise<void>;
}

interface Entry {
  /** status */
  s: number;
  /** headers */
  h: [string, string][];
  /** body, base64 */
  b: string;
  /** render start (ms) */
  t: number;
  /** maxAge, swr (s) */
  m: number;
  w: number;
  /** tags, including the path tag */
  g: string[];
  /** Vary header names and the request's values for them */
  v?: string[];
  vv?: Record<string, string>;
}

// The same list as Astro's memoryCache (astro/dist/core/cache/memory-provider.js).
const DEFAULT_EXCLUDED_PARAMS = [
  "utm_*", "fbclid", "gclid", "gbraid", "wbraid", "dclid", "msclkid", "twclid", "li_fat_id", "mc_cid", "mc_eid", "_ga", "_gl",
  "_hsenc", "_hsmi", "_ke", "oly_anon_id", "oly_enc_id", "rb_clickid", "s_cid", "vero_id", "wickedid", "yclid", "__s", "ref",
];

const PATH_TAG = "astro-path:";
const REROUTABLE = new Set([404, 500]);

function globRe(p: string): RegExp {
  return new RegExp(`^${p.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".")}$`, "i");
}

/** `?a=1&b=2` as the cache key sees it (sorted, filtered), or "". */
export function queryKey(url: URL, q: ValkeyCacheConfig["query"] = {}): string {
  if (q.include && q.exclude) throw new Error("@shiptiffin/astro cache: set query.include or query.exclude, not both");
  const params = new URLSearchParams(url.searchParams);
  if (q.include) {
    const allow = new Set(q.include);
    for (const k of [...params.keys()]) if (!allow.has(k)) params.delete(k);
  } else {
    const ex = (q.exclude ?? DEFAULT_EXCLUDED_PARAMS).map(globRe);
    for (const k of [...params.keys()]) if (ex.some((r) => r.test(k))) params.delete(k);
  }
  if (q.sort !== false) params.sort();
  const s = params.toString();
  return s ? `?${s}` : "";
}

/** max-age and stale-while-revalidate from CDN-Cache-Control (set by Astro.cache / routeRules). */
export function parseCdnCacheControl(h: string | null): { maxAge: number; swr: number } {
  let maxAge = 0;
  let swr = 0;
  for (const part of (h ?? "").split(",")) {
    const t = part.trim().toLowerCase();
    if (t.startsWith("max-age=")) maxAge = Number.parseInt(t.slice(8), 10) || 0;
    else if (t.startsWith("stale-while-revalidate=")) swr = Number.parseInt(t.slice(23), 10) || 0;
  }
  return { maxAge, swr };
}

const sha = (s: string) => createHash("sha256").update(s).digest("hex");

function varyNames(res: Response): string[] | "uncacheable" | undefined {
  const v = res.headers.get("vary");
  if (!v) return undefined;
  const names = v.split(",").map((x) => x.trim().toLowerCase()).filter((x) => x && x !== "set-cookie");
  if (names.includes("*") || names.includes("cookie")) return "uncacheable";
  return names.length ? names : undefined;
}

const varySuffix = (req: Request, names: string[]) => names.map((n) => `\0${n}=${req.headers.get(n) ?? ""}`).join("");

function hasSetCookie(res: Response): boolean {
  if (res.headers.has("set-cookie")) return true;
  // Astro keeps cookies set through Astro.cookies on the response under a symbol until it sends it.
  // (astro/dist/core/cookies/response.js); memoryCache checks it the same way.
  const c = Reflect.get(res, Symbol.for("astro.cookies")) as { headers?: () => Iterator<unknown> } | undefined;
  if (!c?.headers) return false;
  try {
    return !c.headers().next().done;
  } catch {
    return true;
  }
}

function toBase64(buf: ArrayBuffer): string {
  return Buffer.from(buf).toString("base64");
}

/** A provider over the given store. `store` undefined: every request passes through (nothing cached). */
export function createValkeyCache(store: Store | undefined, config: ValkeyCacheConfig = {}, opts: { scope?: string; now?: () => number } = {}): CacheProvider {
  const ns = (config.namespace ?? "astro-cache:") + (opts.scope ?? appScope());
  const maxTtl = Math.max(1, Math.floor(config.maxTtl ?? 7 * 24 * 3600));
  const maxBody = config.maxBodyBytes ?? 1024 * 1024;
  const timeoutMs = config.timeoutMs ?? 1000;
  const now = opts.now ?? Date.now;
  const query = config.query;
  if (query?.include && query.exclude) throw new Error("@shiptiffin/astro cache: set query.include or query.exclude, not both");

  let downUntil = 0;
  let lastWarn = 0;
  let warnedNoStore = false;
  type Log = Parameters<CacheProvider["onRequest"]>[0]["logger"];
  const warn = (log: Log, msg: string) => {
    if (now() - lastWarn < 30_000) return;
    lastWarn = now();
    (log ?? console).warn(`[@shiptiffin/astro] cache: ${msg}`);
  };
  const fail = (log: Log, e: unknown) => {
    downUntil = now() + 5000;
    warn(log, `Valkey unavailable, serving without the cache for 5 s (${(e as Error)?.message ?? e})`);
  };
  /** A read with a deadline. */
  const read = <T>(p: Promise<T>): Promise<T> =>
    new Promise<T>((resolve, reject) => {
      const t = setTimeout(() => reject(new Error(`no reply in ${timeoutMs} ms`)), timeoutMs);
      p.then(
        (v) => (clearTimeout(t), resolve(v)),
        (e) => (clearTimeout(t), reject(e)),
      );
    });
  const k = (kind: string, id: string) => `${ns}${kind}:${id}`;

  /** The entry's newest invalidation is older than its render: still valid. */
  async function valid(entry: Entry): Promise<boolean> {
    if (!store || entry.g.length === 0) return true;
    const times = await read(store.mget(...entry.g.map((t) => k("t", t))));
    return times.every((x) => x == null || Number(x) < entry.t);
  }

  function respond(entry: Entry, state: "HIT" | "STALE"): Response {
    const h = new Headers(entry.h);
    h.set("X-Astro-Cache", state);
    const body = entry.s === 204 || entry.s === 304 ? null : Buffer.from(entry.b, "base64");
    return new Response(body, { status: entry.s, headers: h });
  }

  /** Stores a rendered response (consumes it). Never throws. */
  async function store_(res: Response, req: Request, url: URL, primary: string, start: number, log: Log): Promise<void> {
    if (!store) return;
    try {
      const { maxAge, swr } = parseCdnCacheControl(res.headers.get("cdn-cache-control"));
      const vary = varyNames(res);
      if (maxAge <= 0 || vary === "uncacheable") return;
      const len = Number(res.headers.get("content-length") ?? 0);
      if (len > maxBody) return;
      const buf = await res.arrayBuffer();
      if (buf.byteLength > maxBody) return;
      const tags = (res.headers.get("cache-tag") ?? "").split(",").map((t) => t.trim()).filter(Boolean);
      tags.push(PATH_TAG + url.pathname + queryKey(url, query));
      const headers: [string, string][] = [];
      res.headers.forEach((v, name) => {
        if (name !== "set-cookie" && name !== "x-astro-cache") headers.push([name, v]);
      });
      const entry: Entry = { s: res.status, h: headers, b: toBase64(buf), t: start, m: maxAge, w: swr, g: [...new Set(tags)] };
      let id = sha(primary);
      if (vary) {
        entry.v = vary;
        entry.vv = Object.fromEntries(vary.map((n) => [n, req.headers.get(n) ?? ""]));
        id = sha(primary + varySuffix(req, vary));
      }
      const ex = Math.min(maxAge + swr, maxTtl);
      if (vary) await store.set(k("v", sha(primary)), JSON.stringify(vary), { ex: maxTtl });
      else await store.del(k("v", sha(primary)));
      await store.set(k("e", id), JSON.stringify(entry), { ex });
    } catch (e) {
      fail(log, e);
    }
  }

  const background = (ctx: Parameters<CacheProvider["onRequest"]>[0], p: Promise<unknown>) => {
    const q = p.catch(() => {});
    if (ctx.waitUntil) ctx.waitUntil(q);
  };

  return {
    name: "tiffin-valkey",
    async onRequest(ctx, next) {
      const req = ctx.request;
      if (req.method !== "GET") return next();
      if (!store) {
        if (!warnedNoStore) {
          warnedNoStore = true;
          (ctx.logger ?? console).warn("[@shiptiffin/astro] cache: REDIS_URL is not set, so responses are not cached.");
        }
        return next();
      }
      const url = new URL(req.url);
      const primary = `${url.host}${url.pathname}${queryKey(url, query)}`;
      const pid = sha(primary);
      let entry: Entry | undefined;
      let id = pid;
      let fresh = false;
      let stale = false;
      if (now() >= downUntil) {
        try {
          const [v, e] = await read(store.mget(k("v", pid), k("e", pid)));
          let raw = e;
          if (v != null) {
            const names = JSON.parse(String(v)) as string[];
            id = sha(primary + varySuffix(req, names));
            raw = await read(store.get(k("e", id)));
          }
          if (raw != null) {
            const cand = JSON.parse(String(raw)) as Entry;
            const varyOk = !cand.v || cand.v.every((n) => (req.headers.get(n) ?? "") === cand.vv?.[n]);
            if (varyOk && (await valid(cand))) {
              const age = (now() - cand.t) / 1000;
              fresh = age <= cand.m;
              stale = !fresh && age <= cand.m + cand.w;
              if (fresh || stale) entry = cand;
            }
          }
        } catch (e) {
          fail(ctx.logger, e);
          entry = undefined;
        }
      }
      if (entry && fresh) return respond(entry, "HIT");
      if (entry && stale) {
        // One instance revalidates; everyone gets the stale copy meanwhile.
        const lock = k("l", id);
        background(
          ctx,
          (async () => {
            if (!(await store.set(lock, "1", { ex: 30, nx: true }))) return;
            try {
              const start = now();
              const res = await next();
              if (REROUTABLE.has(res.status) && res.body === null) return;
              if (hasSetCookie(res)) return;
              await store_(res, req, url, primary, start, ctx.logger);
            } finally {
              await store.del(lock).catch(() => {});
            }
          })().catch((e) => warn(ctx.logger, `background revalidation of ${url.pathname} failed: ${(e as Error)?.message ?? e}`)),
        );
        return respond(entry, "STALE");
      }

      const start = now();
      const res = await next();
      const { maxAge } = parseCdnCacheControl(res.headers.get("cdn-cache-control"));
      if (maxAge <= 0) return res;
      // Astro replaces these with the error page: a cached copy would skip that.
      if (REROUTABLE.has(res.status) && res.body === null) return res;
      if (hasSetCookie(res)) {
        warn(ctx.logger, `not caching ${url.pathname}${url.search}: the response sets a cookie`);
        return res;
      }
      if (varyNames(res) === "uncacheable") return res;
      if (now() < downUntil) return res;
      const copy = res.clone();
      background(ctx, store_(copy, req, url, primary, start, ctx.logger));
      try {
        res.headers.set("X-Astro-Cache", "MISS");
      } catch {
        // immutable headers: leave them
      }
      return res;
    },
    async invalidate(options) {
      if (!store) return; // nothing is cached without Valkey
      const tags = options.tags === undefined ? [] : Array.isArray(options.tags) ? [...options.tags] : [options.tags];
      if (options.path) tags.push(PATH_TAG + options.path);
      if (tags.length === 0) return;
      const t = String(now());
      // MSET can't set an expiry per key; a pipeline of SETs is one round trip with the SDK.
      await Promise.all(tags.map((tag) => store.set(k("t", tag), t, { ex: maxTtl })));
    },
  };
}

/** Astro's provider factory: the box's Valkey (REDIS_URL, VALKEY_PREFIX), read at runtime. */
export default function tiffinCache(config?: ValkeyCacheConfig): CacheProvider {
  return createValkeyCache(boxStore(), config);
}
