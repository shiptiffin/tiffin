/**
 * `@shiptiffin/sdk/next/cache-handler`: a Next.js 16 `cacheHandler` (ISR pages,
 * route handlers, `fetch` cache, `unstable_cache`) backed by Valkey, so every
 * instance of an app shares one cache and `revalidateTag` / `revalidatePath`
 * reach all of them.
 *
 * ```js
 * // next.config.mjs
 * export default {
 *   cacheHandler: fileURLToPath(new URL("./cache-handler.mjs", import.meta.url)),
 * };
 * // cache-handler.mjs
 * export { default } from "@shiptiffin/sdk/next/cache-handler";
 * ```
 */
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { mkdir, readdir, readFile, rename, rm, stat, statfs, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { join } from "node:path";
import { readBuildId, Store, warnOnce, type Item, type StoreOptions } from "./store";

/** What Next.js passes to get(). */
export interface GetContext {
  kind?: string;
  tags?: string[];
  softTags?: string[];
  [k: string]: unknown;
}

/** What Next.js passes to set(). */
export interface SetContext {
  tags?: string[];
  revalidate?: number | false;
  cacheControl?: { revalidate?: number | false; expire?: number };
  [k: string]: unknown;
}

/** One stored entry, as Next.js expects get() to return it. */
export interface Entry {
  lastModified: number;
  value: Record<string, unknown> | null;
  tags?: string[];
}

/** Options Next.js passes to the constructor (the parts we use). */
export interface HandlerOptions {
  serverDistDir?: string;
  /** Next.js's file-system interface, which its own file-system cache reads build output with. */
  fs?: unknown;
  [k: string]: unknown;
}

interface Meta {
  lastModified: number;
  tags: string[];
}

const TAGS_HEADER = "x-next-cache-tags";

/**
 * An entry's tags: those it was stored with and Next.js passes (fetch), and
 * for pages and route handlers the x-next-cache-tags header, which holds their
 * explicit tags and the implicit _N_T_ path tags revalidatePath uses. That is
 * where Next.js's file-system cache reads them too.
 */
function entryTags(item: Item<Meta>, ctx: GetContext): string[] {
  const tags = [...item.meta.tags, ...(ctx.tags ?? []), ...(ctx.softTags ?? [])];
  const header = ((item.value as Record<string, unknown> | null)?.headers as Record<string, unknown> | undefined)?.[TAGS_HEADER];
  if (typeof header === "string" && header) tags.push(...header.split(","));
  return tags;
}

const builds = new Map<string, string | undefined>();

/** The kinds `next build` prerenders to files: pages and GET route handlers. */
const PRERENDERED = new Set(["APP_PAGE", "APP_ROUTE", "PAGES"]);

type FileCache = { get(key: string, ctx: GetContext): Promise<Entry | null> };
type FileCacheClass = new (o: Record<string, unknown>) => FileCache;
let fileCacheClass: FileCacheClass | null | undefined;
const fileCaches = new Map<string, FileCache | null>();

/**
 * Next.js's own file-system cache over this build's output, read-only. With a
 * cacheHandler set, `next build` still writes every prerendered page and
 * route handler to .next/server (the store it rendered into is gone), and
 * Next.js's default handler serves those files until an entry replaces them.
 * Reading them with that same class keeps its semantics: which file belongs
 * to which key, PPR shells, segments, and the files' time as lastModified.
 */
function fileCache(o: HandlerOptions): FileCache | null {
  const dist = o.serverDistDir;
  if (!dist || !o.fs) return null;
  let fc = fileCaches.get(dist);
  if (fc !== undefined) return fc;
  if (fileCacheClass === undefined) {
    fileCacheClass = null;
    for (const from of [`${process.cwd()}/package.json`, import.meta.url]) {
      try {
        const m = createRequire(from)("next/dist/server/lib/incremental-cache/file-system-cache.js") as { default?: unknown };
        if (typeof m.default === "function") {
          fileCacheClass = m.default as FileCacheClass;
          break;
        }
      } catch {
        // not resolvable from here
      }
    }
  }
  fc = fileCacheClass ? new fileCacheClass({ fs: o.fs, serverDistDir: dist, flushToDisk: false, revalidatedTags: [], maxMemoryCacheSize: 0 }) : null;
  fileCaches.set(dist, fc);
  return fc;
}

/** An optimized image as Next.js's image optimizer stores it (kind IMAGE). */
interface ImageValue {
  kind: "IMAGE";
  etag: string;
  buffer: Buffer;
  extension: string;
  upstreamEtag: string;
  revalidate?: number;
}

/** Next.js's image cache keys (base64url hashes) and the parts of its file names. */
const IMAGE_KEY = /^[A-Za-z0-9_-]{1,128}$/;
const IMAGE_PART = /^[A-Za-z0-9_-]{1,256}$/;

/**
 * Optimized images (kind IMAGE) on disk, never in Valkey: they are large,
 * depend only on the source and its parameters (not the deploy), and the box
 * mounts .next/cache/images per app environment, shared by its instances and
 * kept across deploys. Next.js 16.2+ hands images to a cacheHandler only when
 * the app sets `images.customCacheHandler`; by default its own disk cache
 * keeps them in the same directory. Entries use Next.js's own layout,
 * `<key>/<maxAge>.<expireAt>.<etag>.<upstreamEtag>.<extension>`, so either
 * cache reads what the other wrote, and the directory is bounded by
 * `images.maximumDiskCacheSize` (least recently used out first), as Next.js
 * bounds it. Every instance of the app writes to the directory, so a write
 * reads it afresh when the last look is older than `rescanMs`: the cap then
 * counts the other instances' files too (it holds to within what they
 * write in that time).
 */
export class ImageDiskCache {
  private lru: Promise<Map<string, number>> | undefined;
  private scanned = 0;
  private bytes = 0;

  constructor(
    readonly dir: string,
    /** Byte cap; undefined: half the free disk, as Next.js does; 0: nothing is kept. */
    readonly maxBytes: number | undefined,
    /** How old the view of the directory may get before a write reads it again. */
    readonly rescanMs = 60_000,
  ) {}

  private entries(): Promise<Map<string, number>> {
    if (this.lru && Date.now() - this.scanned >= this.rescanMs) this.lru = undefined;
    if (!this.lru) this.scanned = Date.now();
    return (this.lru ??= (async () => {
      const found: { key: string; size: number; expireAt: number }[] = [];
      for (const key of await readdir(this.dir).catch(() => [] as string[])) {
        if (!IMAGE_KEY.test(key)) continue;
        const [file] = await readdir(join(this.dir, key)).catch(() => [] as string[]);
        const size = file ? await stat(join(this.dir, key, file)).then((st) => st.size, () => 0) : 0;
        if (file && size > 0) found.push({ key, size, expireAt: Number(file.split(".")[1]) || 0 });
      }
      found.sort((a, b) => a.expireAt - b.expireAt); // oldest first, as Next.js replays them
      const m = new Map<string, number>();
      for (const e of found) m.set(e.key, e.size);
      this.bytes = found.reduce((n, e) => n + e.size, 0);
      return m;
    })());
  }

  private async cap(): Promise<number> {
    if (this.maxBytes !== undefined) return this.maxBytes;
    await mkdir(this.dir, { recursive: true });
    const s = await statfs(this.dir);
    return Math.floor((s.bavail * s.bsize) / 2);
  }

  async get(key: string): Promise<Entry | null> {
    if (this.maxBytes === 0 || !IMAGE_KEY.test(key)) return null;
    try {
      const [file] = await readdir(join(this.dir, key));
      if (!file) return null;
      const [maxAge, expireAt, etag, upstreamEtag, extension] = file.split(".", 5);
      const buffer = await readFile(join(this.dir, key, file));
      if (!buffer.byteLength || !extension) return null;
      // Most recently used last (once the first write has read the directory).
      const lru = this.lru && (await this.lru);
      if (lru?.delete(key)) lru.set(key, buffer.byteLength);
      const value: ImageValue = { kind: "IMAGE", etag: etag!, buffer, extension, upstreamEtag: upstreamEtag!, revalidate: Number(maxAge) };
      // Next.js takes the entry's age from lastModified and its revalidate.
      return { lastModified: Number(expireAt) - Number(maxAge) * 1000, value: value as unknown as Record<string, unknown> };
    } catch {
      return null;
    }
  }

  async set(key: string, value: ImageValue | null, revalidate: number): Promise<void> {
    if (this.maxBytes === 0 || !IMAGE_KEY.test(key)) return;
    const at = join(this.dir, key);
    const lru = await this.entries();
    if (!value) {
      await rm(at, { recursive: true, force: true });
      this.bytes -= lru.get(key) ?? 0;
      lru.delete(key);
      return;
    }
    const size = value.buffer?.byteLength ?? 0;
    const maxAge = Math.max(0, Math.round(revalidate));
    if (!size || ![value.etag, value.upstreamEtag, value.extension].every((p) => IMAGE_PART.test(String(p)))) return;
    const max = await this.cap();
    if (size > max) return;
    // Written whole, then moved into place: another instance sharing the
    // directory never reads half a file. A dot-name is skipped by both scans.
    await mkdir(this.dir, { recursive: true });
    const tmp = join(this.dir, `.tmp-${randomBytes(6).toString("hex")}`);
    await writeFile(tmp, value.buffer);
    try {
      await rm(at, { recursive: true, force: true });
      await mkdir(at, { recursive: true });
      await rename(tmp, join(at, `${maxAge}.${Date.now() + maxAge * 1000}.${value.etag}.${value.upstreamEtag}.${value.extension}`));
    } finally {
      await rm(tmp, { force: true });
    }
    this.bytes += size - (lru.get(key) ?? 0);
    lru.delete(key);
    lru.set(key, size);
    for (const [old, n] of lru) {
      if (this.bytes <= max) break;
      lru.delete(old);
      this.bytes -= n;
      await rm(join(this.dir, old), { recursive: true, force: true });
    }
  }
}

const imageCaches = new Map<string, ImageDiskCache>();

/**
 * The image cache of the build in serverDistDir (<distDir>/server): its
 * <distDir>/cache/images, capped by the build's images.maximumDiskCacheSize.
 */
function imageCache(o: HandlerOptions): ImageDiskCache | null {
  const dist = o.serverDistDir;
  if (!dist) return null;
  const distDir = join(dist, "..");
  let c = imageCaches.get(distDir);
  if (!c) {
    let max: number | undefined;
    try {
      const rsf = JSON.parse(readFileSync(join(distDir, "required-server-files.json"), "utf8")) as { config?: { images?: { maximumDiskCacheSize?: number } } };
      max = rsf.config?.images?.maximumDiskCacheSize;
    } catch {
      // no build output here (tests, dev): Next.js's default cap
    }
    c = new ImageDiskCache(join(distDir, "cache", "images"), typeof max === "number" && max >= 0 ? max : undefined);
    imageCaches.set(distDir, c);
  }
  return c;
}

/**
 * Next.js makes one handler per request; they share one Store (connection,
 * in-memory copy, tag state). Each request reads the tag counter once.
 */
export class TiffinCacheHandler {
  readonly store: Store;
  private synced: Promise<void> | undefined;
  private readonly files: FileCache | null;
  private readonly images: ImageDiskCache | null;

  constructor(nextOptions: HandlerOptions = {}, storeOptions: StoreOptions = {}) {
    this.files = fileCache(nextOptions);
    this.images = imageCache(nextOptions);
    let o = storeOptions;
    const dist = nextOptions.serverDistDir;
    if (dist && o.buildId === undefined && !process.env.TIFFIN_DEPLOY && !process.env.NEXT_DEPLOYMENT_ID) {
      if (!builds.has(dist)) builds.set(dist, readBuildId(`${dist}/..`));
      const id = builds.get(dist);
      if (id) o = { ...o, buildId: id };
    }
    this.store = Store.open(o);
  }

  async get(key: string, ctx: GetContext = {}): Promise<Entry | null> {
    if (ctx.kind === "IMAGE") return (await this.images?.get(key)) ?? null;
    try {
      const s = this.store;
      // The tag counter and the entry in one round trip.
      let [, item] = await Promise.all([(this.synced ??= s.sync()), s.read<Meta>("e", key)]);
      item ??= await this.prerendered(key, ctx);
      if (!item || !s.trusted(item.meta.lastModified)) return null;
      let tags = entryTags(item, ctx);
      if (s.expired(tags, item.meta.lastModified) || s.stale(tags, item.meta.lastModified)) {
        // Outdated copy: another instance may have rendered it anew already.
        item = await s.read<Meta>("e", key, true);
        if (!item) return null;
        tags = entryTags(item, ctx);
      }
      const { lastModified } = item.meta;
      const value = item.value as Record<string, unknown>;
      if (s.expired(tags, lastModified)) return null;
      // A stale tag: Next.js sees it in its tag state and serves this entry
      // once while it regenerates. Without that state, regenerate now.
      if (!s.signalsStale && s.stale(tags, lastModified)) return null;
      return { lastModified, value, tags: item.meta.tags };
    } catch (err) {
      warnOnce(`cache get failed, treating as a miss: ${String(err)}`);
      return null;
    }
  }

  /**
   * What `next build` prerendered for key, when Valkey has nothing yet. It is
   * copied into Valkey with the build's time (unless an instance stored a
   * rendering meanwhile), so later requests and the other instances read it there.
   */
  private async prerendered(key: string, ctx: GetContext): Promise<Item<Meta> | undefined> {
    if (!this.files || !PRERENDERED.has(String(ctx.kind))) return undefined;
    const got = await this.files.get(key, ctx).catch(() => null);
    // Older than the tag history kept (see Store.trusted): rendered anew.
    if (!got?.value || !this.store.trusted(got.lastModified)) return undefined;
    const item: Item<Meta> = { meta: { lastModified: got.lastModified, tags: [] }, value: got.value, size: 0, until: 0 };
    const s = this.store;
    // A tag revalidated since the build makes it a miss: nothing to copy.
    if (!s.expired(entryTags(item, ctx), got.lastModified)) {
      await s.write("e", key, { ...item.meta }, got.value, s.maxTtl, true).catch((err) => warnOnce(`cache set failed (the response was still served): ${String(err)}`));
    }
    return item;
  }

  async set(key: string, data: Record<string, unknown> | null, ctx: SetContext = {}): Promise<void> {
    if (ctx.kind === "IMAGE" || data?.kind === "IMAGE") {
      const revalidate = ctx.cacheControl?.revalidate ?? (data?.revalidate as number | undefined);
      await this.images?.set(key, data as ImageValue | null, typeof revalidate === "number" ? revalidate : 0).catch((err) => warnOnce(`image cache write failed (the image was still served): ${String(err)}`));
      return;
    }
    try {
      if (data == null) {
        await this.store.del("e", key);
        return;
      }
      const tags = ctx.tags ?? (Array.isArray(data.tags) ? (data.tags as string[]) : []);
      const lastModified = Date.now();
      const revalidate = ctx.cacheControl?.revalidate ?? ctx.revalidate ?? (data.revalidate as number | false | undefined);
      const expire = ctx.cacheControl?.expire;
      const meta: Record<string, unknown> = { lastModified, tags };
      if (typeof revalidate === "number" && revalidate > 0) meta.fresh = lastModified + revalidate * 1000;
      await this.store.write("e", key, meta, data, typeof expire === "number" && expire > 0 ? expire : this.store.maxTtl);
    } catch (err) {
      warnOnce(`cache set failed (the response was still served): ${String(err)}`);
    }
  }

  async revalidateTag(tags: string | string[], durations?: { expire?: number }): Promise<void> {
    try {
      await this.store.updateTags(Array.isArray(tags) ? tags : [tags], durations);
    } catch (err) {
      warnOnce(`revalidating tags failed: ${String(err)}`);
    }
  }

  resetRequestCache(): void {}
}

export default TiffinCacheHandler;
