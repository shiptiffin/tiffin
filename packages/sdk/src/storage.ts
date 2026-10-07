/**
 * `@shiptiffin/sdk/storage`: the project's buckets from server code (Bun or Node).
 *
 * ```ts
 * import { upload, presign, publicUrl, signedUrl, createUpload, uploadRoute, onUploadCompleted } from "@shiptiffin/sdk/storage";
 *
 * await upload("media", "avatars/42.png", file, { contentType: "image/png" });
 * presign("media", "avatars/42.png", { expiresIn: 600 });   // S3 download link
 * publicUrl("assets", "hero.jpg", { width: 1200 });          // public bucket, resized to webp
 * signedUrl("media", "avatars/42.png", { width: 256 });      // private bucket, time-limited
 *
 * // Browser uploads: hand the browser a ticket, it sends the bytes to the box.
 * export const POST = uploadRoute({ bucket: "media", maxSize: 50 << 20, allowedTypes: ["image/*"] }); // app/api/upload/route.ts
 * const ticket = await createUpload({ bucket: "media", key: "videos/a.mp4", contentType: "video/mp4", size }); // or in a Server Action
 *
 * // Know when an upload lands (topic storage.object.created in tiffin.config.ts):
 * const uploaded = onUploadCompleted(async (e) => { await makeThumbnail(e.bucket, e.key); });
 * ```
 *
 * The box gives every app S3_ENDPOINT, S3_REGION, S3_ACCESS_KEY_ID,
 * S3_SECRET_ACCESS_KEY and S3_BUCKET_<NAME> (plus AWS_* twins), so `Bun.s3`
 * and the AWS SDKs work with no setup. These helpers add what they do not
 * know: bucket names from tiffin.config.ts, URLs on the public endpoints
 * (S3_PUBLIC_ENDPOINT, TIFFIN_FILES_URL) that browsers can reach, upload
 * tickets and image transforms. They sign requests themselves (SigV4), so
 * they need nothing but fetch and node:crypto.
 */
import { createHash, createHmac, randomUUID } from "node:crypto";
import { defineHandler, type HandlerOptions, type Job } from "./queue";
import { presignUrl, signRequest, type S3Creds } from "./s3sign";
import type { UploadTicket } from "./client/upload";

export type { UploadTicket, UploadResult, UploadProgress, UploadFileOptions } from "./client/upload";

/** Where settings come from; defaults to process.env. */
export type Env = Record<string, string | undefined>;

export interface StorageOptions {
  env?: Env;
}

function envOf(o?: StorageOptions): Env {
  return o?.env ?? process.env;
}

function need(env: Env, name: string): string {
  const v = env[name];
  if (!v) {
    throw new Error(`${name} is not set: is services.storage in tiffin.config.ts, and is this running on the box (or with \`tiffin storage credentials\` exported)?`);
  }
  return v;
}

/** "user-uploads" → "USER_UPLOADS". */
function envSuffix(name: string): string {
  return name.toUpperCase().replaceAll("-", "_");
}

/** The S3 bucket name for a bucket in tiffin.config.ts ("media" → "shop-media"). */
export function bucketName(name: string, o?: StorageOptions): string {
  const env = envOf(o);
  const v = env[`S3_BUCKET_${envSuffix(name)}`];
  if (!v) {
    const known = Object.keys(env)
      .filter((k) => k.startsWith("S3_BUCKET_"))
      .map((k) => k.slice("S3_BUCKET_".length).toLowerCase().replaceAll("_", "-"));
    throw new Error(`no bucket "${name}" in this project${known.length ? ` (buckets: ${known.join(", ")})` : ""}; add it under services.storage.buckets`);
  }
  return v;
}

/** Whether a bucket is public (readable at its publicUrl without a signature). */
export function isPublic(name: string, o?: StorageOptions): boolean {
  return (envOf(o).TIFFIN_PUBLIC_BUCKETS ?? "").split(",").includes(name);
}

function creds(env: Env): S3Creds {
  return { accessKeyId: need(env, "S3_ACCESS_KEY_ID"), secretAccessKey: need(env, "S3_SECRET_ACCESS_KEY"), region: env.S3_REGION ?? "us-east-1" };
}

/**
 * Bun's S3Client where Bun's types are loaded, else unknown: the package's
 * declarations name no Bun type, so a Node app type-checks them without
 * installing @types/bun.
 */
export type BunS3Client = typeof globalThis extends { Bun: { S3Client: new (...args: never[]) => infer C } } ? C : unknown;

/** A Bun.S3Client for one of the project's buckets, on the box-internal endpoint (Bun only). */
export function bucket(name: string, o?: StorageOptions): BunS3Client {
  const env = envOf(o);
  if (typeof Bun === "undefined") throw new Error("bucket() returns a Bun.S3Client; on Node use upload()/presign() or the AWS SDK with the S3_* env");
  const c = creds(env);
  return new Bun.S3Client({ accessKeyId: c.accessKeyId, secretAccessKey: c.secretAccessKey, region: c.region, endpoint: need(env, "S3_ENDPOINT"), bucket: bucketName(name, o) });
}

export interface UploadOptions extends StorageOptions {
  /** MIME type; default the Blob's type, else guessed from the key's extension. */
  contentType?: string;
}

export interface Uploaded {
  bucket: string;
  key: string;
  size: number;
  etag: string;
  /** The public URL, for public buckets. */
  url?: string;
}

/** What upload() stores. */
export type Body = string | ArrayBuffer | ArrayBufferView | Blob;

const extTypes: Record<string, string> = {
  png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif", svg: "image/svg+xml",
  ico: "image/x-icon", pdf: "application/pdf", txt: "text/plain; charset=utf-8", html: "text/html; charset=utf-8", css: "text/css",
  js: "text/javascript", json: "application/json", csv: "text/csv", xml: "application/xml", zip: "application/zip",
  mp4: "video/mp4", webm: "video/webm", mov: "video/quicktime", mp3: "audio/mpeg", wav: "audio/wav", ogg: "audio/ogg",
  woff2: "font/woff2", wasm: "application/wasm",
};

function guessType(key: string, data: Body): string | undefined {
  if (data instanceof Blob && data.type) return data.type;
  const dot = key.lastIndexOf(".");
  return dot > key.lastIndexOf("/") ? extTypes[key.slice(dot + 1).toLowerCase()] : undefined;
}

function checkKey(key: string): void {
  if (!key || key.startsWith("/")) throw new Error("key must be a non-empty path without a leading /");
}

/** Stores data at key in a bucket (one PUT, up to 5 GiB). */
export async function upload(name: string, key: string, data: Body, o?: UploadOptions): Promise<Uploaded> {
  checkKey(key);
  const env = envOf(o);
  const type = o?.contentType ?? guessType(key, data);
  const body: Blob = data instanceof Blob ? data : new Blob([typeof data === "string" ? data : (data as BlobPart)]);
  const req = signRequest({ method: "PUT", endpoint: need(env, "S3_ENDPOINT"), bucket: bucketName(name, o), key, creds: creds(env), headers: type ? { "content-type": type } : {} });
  const res = await fetch(req.url, { method: "PUT", headers: req.headers, body });
  if (!res.ok) throw new StorageError(res.status, await res.text());
  const out: Uploaded = { bucket: name, key, size: body.size, etag: (res.headers.get("etag") ?? "").replaceAll('"', "") };
  if (isPublic(name, o)) out.url = publicUrl(name, key, o);
  return out;
}

/** An S3 error from the box (code such as QuotaExceeded, EntityTooLarge). */
export class StorageError extends Error {
  override name = "StorageError";
  readonly code: string;
  constructor(
    readonly status: number,
    body: string,
  ) {
    const code = /<Code>([^<]*)<\/Code>/.exec(body)?.[1] ?? `HTTP${status}`;
    super(`${code}: ${/<Message>([^<]*)<\/Message>/.exec(body)?.[1] ?? (body.trim().slice(0, 200) || `HTTP ${status}`)}`);
    this.code = code;
  }
}

export interface PresignOptions extends StorageOptions {
  /** GET (download, the default) or PUT (upload). */
  method?: "GET" | "PUT";
  /** Seconds the URL is valid (default 3600, max 604800). */
  expiresIn?: number;
  /** PUT: the Content-Type the upload must send (signed into the URL). */
  contentType?: string;
  /** PUT: the largest file the URL accepts, in bytes (signed into the URL). */
  maxSize?: number;
}

/** The query parameter of an upload URL that caps its size (checked by the box). */
const MAX_SIZE_PARAM = "x-tiffin-max-size";

function publicEndpoint(env: Env): string {
  return env.S3_PUBLIC_ENDPOINT ?? need(env, "S3_ENDPOINT");
}

/**
 * A presigned URL on the public S3 endpoint, for browsers and other
 * machines. It is computed locally: no request is made.
 */
export function presign(name: string, key: string, o?: PresignOptions): string {
  const env = envOf(o);
  const query: Record<string, string> = {};
  const headers: Record<string, string> = {};
  if (o?.maxSize) query[MAX_SIZE_PARAM] = String(o.maxSize);
  if (o?.contentType) headers["content-type"] = o.contentType;
  return presignUrl({ method: o?.method ?? "GET", endpoint: publicEndpoint(env), bucket: bucketName(name, o), key, creds: creds(env), expiresIn: o?.expiresIn ?? 3600, query, headers });
}

/** Encodes a key for a URL path, keeping its slashes. */
export function encodeKey(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

export interface ImageOptions {
  /** Resize to this width (one of 16 … 3840, the next/image sizes; others are rounded up). Never enlarges. */
  width?: number;
  /** 50, 75 (default), 90 or 100. */
  quality?: number;
  /** "webp" (default when resizing), "avif" or "original". */
  format?: "webp" | "avif" | "original";
}

/** Widths and qualities the box transforms to (internal/mod/storage/image.go). */
export const IMAGE_WIDTHS = [16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840] as const;
export const IMAGE_QUALITIES = [50, 75, 90, 100] as const;

/** Adds w, q and f for an image transform (nothing when im is empty). */
export function imageParams(u: URL, im?: ImageOptions): URL {
  if (!im || (im.width === undefined && im.quality === undefined && im.format === undefined)) return u;
  if (im.width !== undefined) u.searchParams.set("w", String(IMAGE_WIDTHS.find((w) => w >= im.width!) ?? IMAGE_WIDTHS[IMAGE_WIDTHS.length - 1]));
  if (im.quality !== undefined) {
    const q = IMAGE_QUALITIES.reduce((a, b) => (Math.abs(b - im.quality!) < Math.abs(a - im.quality!) ? b : a));
    u.searchParams.set("q", String(q));
  }
  u.searchParams.set("f", im.format ?? "webp");
  return u;
}

/** The public URL of an object in a public bucket, optionally a resized image. */
export function publicUrl(name: string, key: string, o?: StorageOptions & ImageOptions): string {
  const env = envOf(o);
  if (!isPublic(name, o)) {
    throw new Error(`bucket "${name}" is private: use signedUrl() for a time-limited link, or set public: true in tiffin.config.ts`);
  }
  return imageParams(new URL(`${need(env, "TIFFIN_FILES_URL")}/${name}/${encodeKey(key)}`), o).toString();
}

export interface SignedUrlOptions extends StorageOptions, ImageOptions {
  /** Seconds the link works (default 3600). */
  expiresIn?: number;
}

/**
 * A time-limited link to an object on files.<domain>, for private buckets
 * (public ones work too). Resizing options can be added to it later, so it
 * works with the next/image loader.
 */
export function signedUrl(name: string, key: string, o?: SignedUrlOptions): string {
  const env = envOf(o);
  const base = need(env, "TIFFIN_FILES_URL");
  const project = new URL(base).pathname.split("/").filter(Boolean).pop() ?? "";
  const exp = Math.floor(Date.now() / 1000) + (o?.expiresIn ?? 3600);
  const k = createHmac("sha256", creds(env).secretAccessKey).update("tiffin-files-v1").digest();
  const sig = createHmac("sha256", k).update(`${project}/${name}/${key}\n${exp}`).digest("base64url");
  const u = new URL(`${base}/${name}/${encodeKey(key)}`);
  u.searchParams.set("exp", String(exp));
  u.searchParams.set("sig", sig);
  return imageParams(u, o).toString();
}

/**
 * A content-addressed key: "img/logo.png" + bytes → "img/logo.<16 hex>.png".
 * Public files whose key carries a hash are served with a one-year
 * immutable cache, because their bytes can never change.
 */
export async function contentKey(key: string, data: Body): Promise<string> {
  let bytes: Uint8Array;
  if (typeof data === "string") bytes = new TextEncoder().encode(data);
  else if (data instanceof Blob) bytes = new Uint8Array(await data.arrayBuffer());
  else if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
  else bytes = new Uint8Array(data.buffer as ArrayBuffer, data.byteOffset, data.byteLength);
  const hash = createHash("sha256").update(bytes).digest("hex").slice(0, 16);
  const slash = key.lastIndexOf("/");
  const dot = key.lastIndexOf(".");
  return dot > slash + 1 ? `${key.slice(0, dot)}.${hash}${key.slice(dot)}` : `${key}.${hash}`;
}

// ---- browser uploads ----

export interface CreateUploadOptions extends StorageOptions {
  /** Bucket name in tiffin.config.ts. */
  bucket: string;
  key: string;
  /** The type the browser must upload as (signed in). */
  contentType?: string;
  /** The file's exact size: signed in, and needed for multipart uploads. */
  size?: number;
  /** The largest file this ticket accepts, in bytes (the bucket's maxFileSize applies too). */
  maxSize?: number;
  /** Seconds the ticket works (default 3600). */
  expiresIn?: number;
  /** Files bigger than this go up in parts (default 64 MiB). */
  multipartThreshold?: number;
  /** Part size for multipart uploads (default 8 MiB, or more to stay within 1000 parts). */
  partSize?: number;
}

const MiB = 1 << 20;

/**
 * Makes an upload ticket: presigned URLs a browser (uploadFile from
 * @shiptiffin/sdk/client) sends a file to, straight to the box's storage.
 * Call it from a route handler or a Server Action after checking who is
 * asking. A file over multipartThreshold gets a multipart upload: the
 * upload is created now, the ticket carries one URL per part.
 */
export async function createUpload(o: CreateUploadOptions): Promise<UploadTicket> {
  checkKey(o.key);
  const env = envOf(o);
  const c = creds(env);
  const s3name = bucketName(o.bucket, o);
  const expiresIn = o.expiresIn ?? 3600;
  if (o.maxSize !== undefined && o.size !== undefined && o.size > o.maxSize) {
    throw new StorageError(413, `<Code>EntityTooLarge</Code><Message>the file is ${o.size} bytes; this upload takes up to ${o.maxSize}</Message>`);
  }
  const ticket: UploadTicket = { bucket: o.bucket, key: o.key, expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString() };
  if (o.contentType) ticket.contentType = o.contentType;
  if (o.size !== undefined) ticket.size = o.size;
  if (isPublic(o.bucket, o)) ticket.publicUrl = publicUrl(o.bucket, o.key, o);
  const cap: Record<string, string> = {};
  const max = o.maxSize ?? o.size;
  if (max !== undefined) cap[MAX_SIZE_PARAM] = String(max);
  const sign = (method: string, query: Record<string, string>, headers: Record<string, string> = {}) =>
    presignUrl({ method, endpoint: publicEndpoint(env), bucket: s3name, key: o.key, creds: c, expiresIn, query, headers });

  if (o.size === undefined || o.size <= (o.multipartThreshold ?? 64 * MiB)) {
    const headers: Record<string, string> = {};
    if (o.contentType) headers["content-type"] = o.contentType;
    if (o.size !== undefined) headers["content-length"] = String(o.size);
    ticket.url = sign("PUT", cap, headers);
    ticket.headers = o.contentType ? { "Content-Type": o.contentType } : {};
    return ticket;
  }
  const size = o.size;
  const partSize = Math.max(o.partSize ?? 8 * MiB, 5 * MiB, Math.ceil(size / 1000));
  const req = signRequest({ method: "POST", endpoint: need(env, "S3_ENDPOINT"), bucket: s3name, key: o.key, creds: c, query: { uploads: "" },
    headers: o.contentType ? { "content-type": o.contentType } : {} });
  const res = await fetch(req.url, { method: "POST", headers: req.headers });
  const text = await res.text();
  if (!res.ok) throw new StorageError(res.status, text);
  const uploadId = /<UploadId>([^<]+)<\/UploadId>/.exec(text)?.[1];
  if (!uploadId) throw new StorageError(502, `<Code>BadResponse</Code><Message>no UploadId in ${text.slice(0, 200)}</Message>`);
  const parts: string[] = [];
  for (let i = 0, n = Math.ceil(size / partSize); i < n; i++) {
    const len = Math.min(partSize, size - i * partSize);
    parts.push(sign("PUT", { ...cap, partNumber: String(i + 1), uploadId }, { "content-length": String(len) }));
  }
  ticket.multipart = { uploadId, partSize, parts, complete: sign("POST", { ...cap, uploadId }), list: sign("GET", { uploadId }), abort: sign("DELETE", { uploadId }) };
  return ticket;
}

/** What a browser sends an upload route: the file it wants to upload. */
export interface UploadRequest {
  name: string;
  size: number;
  type: string;
  /** Anything the page passed as uploadFile's payload option. */
  payload?: unknown;
}

export interface UploadRouteOptions extends StorageOptions {
  /** Bucket name in tiffin.config.ts. */
  bucket: string;
  /** Largest file accepted, in bytes. */
  maxSize?: number;
  /** MIME types accepted ("image/*" matches a family). Default: any. */
  allowedTypes?: string[];
  /** Where the file goes. Default: "uploads/<random id>/<file name>". */
  key?: (file: UploadRequest, req: Request) => string | Promise<string>;
  /** Runs first: throw (or return false) to refuse, e.g. when nobody is signed in. */
  authorize?: (file: UploadRequest, req: Request) => unknown;
  /** Seconds the ticket works (default 3600). */
  expiresIn?: number;
}

function typeMatches(globs: string[] | undefined, type: string): boolean {
  if (!globs?.length) return true;
  const t = type.split(";")[0]!.trim().toLowerCase();
  return globs.some((g) => (g.endsWith("/*") ? t.startsWith(g.slice(0, -1).toLowerCase()) : t === g.toLowerCase()));
}

/** A file name safe as the last part of a key. */
function safeName(name: string): string {
  const base = name.split(/[\\/]/).pop() ?? "";
  // eslint-disable-next-line no-control-regex
  return base.replace(/[\u0000-\u001f\u007f]/g, "").replace(/^\.+/, "").slice(-200) || "file";
}

/**
 * A POST route handler that answers uploadFile() with a ticket, after its
 * checks: `export const POST = uploadRoute({ bucket: "media" })` in
 * app/api/upload/route.ts (Next.js), or any `(Request) => Response` server.
 */
export function uploadRoute(o: UploadRouteOptions): (req: Request) => Promise<Response> {
  const refuse = (status: number, code: string, error: string) => Response.json({ code, error }, { status });
  return async (req) => {
    let file: UploadRequest;
    try {
      file = (await req.json()) as UploadRequest;
    } catch {
      return refuse(400, "BadRequest", "send JSON { name, size, type }");
    }
    if (typeof file?.name !== "string" || typeof file.size !== "number" || !Number.isSafeInteger(file.size) || file.size < 0 || typeof file.type !== "string") {
      return refuse(400, "BadRequest", "send JSON { name, size, type }");
    }
    try {
      if (o.authorize && (await o.authorize(file, req)) === false) return refuse(403, "Forbidden", "this upload is not allowed");
    } catch (e) {
      return refuse(403, "Forbidden", e instanceof Error ? e.message : "this upload is not allowed");
    }
    if (o.maxSize !== undefined && file.size > o.maxSize) return refuse(413, "EntityTooLarge", `files can be up to ${o.maxSize} bytes; this one is ${file.size}`);
    if (!typeMatches(o.allowedTypes, file.type)) return refuse(415, "InvalidContentType", `${file.type || "this type"} is not accepted (${o.allowedTypes!.join(", ")})`);
    const key = o.key ? await o.key(file, req) : `uploads/${randomUUID()}/${safeName(file.name)}`;
    const opts: CreateUploadOptions = { bucket: o.bucket, key, size: file.size, contentType: file.type || "application/octet-stream" };
    if (o.env) opts.env = o.env;
    if (o.maxSize !== undefined) opts.maxSize = o.maxSize;
    if (o.expiresIn !== undefined) opts.expiresIn = o.expiresIn;
    try {
      return Response.json(await createUpload(opts));
    } catch (e) {
      if (e instanceof StorageError) return refuse(e.status, e.code, e.message);
      throw e;
    }
  };
}

// ---- upload events ----

/** The payload of a storage.object.created event. */
export interface ObjectCreated {
  event: "object.created";
  project: string;
  /** Bucket name in tiffin.config.ts. */
  bucket: string;
  key: string;
  size: number;
  contentType: string;
  etag: string;
  /** Public URL (public buckets only). */
  url?: string;
  at: string;
}

/** The queue topic the box publishes upload events to. */
export const OBJECT_CREATED_TOPIC = "storage.object.created";

/**
 * A queue handler for upload events. Declare the topic with a subscriber
 * queue in tiffin.config.ts, and serve this handler on that queue's path:
 *
 * ```ts
 * queues: { uploads: { app: "web" } },                          // POST /queues/uploads
 * topics: { "storage.object.created": { subscribers: ["uploads"] } },
 * ```
 */
export function onUploadCompleted(fn: (e: ObjectCreated, job: Job<ObjectCreated>) => unknown, opts?: HandlerOptions): (req: Request) => Promise<Response> {
  return defineHandler<ObjectCreated>((job) => fn(job.payload, job), opts);
}
