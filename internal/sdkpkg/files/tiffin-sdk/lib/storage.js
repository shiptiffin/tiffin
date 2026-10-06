/**
 * `tiffin-sdk/storage`: the project's buckets from server code (Bun or Node).
 *
 * ```ts
 * import { upload, presign, publicUrl, signedUrl, createUpload, uploadRoute, onUploadCompleted } from "tiffin-sdk/storage";
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
import { defineHandler } from "./queue.js";
import { presignUrl, signRequest } from "./s3sign.js";
function envOf(o) {
    return o?.env ?? process.env;
}
function need(env, name) {
    const v = env[name];
    if (!v) {
        throw new Error(`${name} is not set: is services.storage in tiffin.config.ts, and is this running on the box (or with \`tiffin storage credentials\` exported)?`);
    }
    return v;
}
/** "user-uploads" → "USER_UPLOADS". */
function envSuffix(name) {
    return name.toUpperCase().replaceAll("-", "_");
}
/** The S3 bucket name for a bucket in tiffin.config.ts ("media" → "shop-media"). */
export function bucketName(name, o) {
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
export function isPublic(name, o) {
    return (envOf(o).TIFFIN_PUBLIC_BUCKETS ?? "").split(",").includes(name);
}
function creds(env) {
    return { accessKeyId: need(env, "S3_ACCESS_KEY_ID"), secretAccessKey: need(env, "S3_SECRET_ACCESS_KEY"), region: env.S3_REGION ?? "us-east-1" };
}
/** A Bun.S3Client for one of the project's buckets, on the box-internal endpoint (Bun only). */
export function bucket(name, o) {
    const env = envOf(o);
    if (typeof Bun === "undefined")
        throw new Error("bucket() returns a Bun.S3Client; on Node use upload()/presign() or the AWS SDK with the S3_* env");
    const c = creds(env);
    return new Bun.S3Client({ accessKeyId: c.accessKeyId, secretAccessKey: c.secretAccessKey, region: c.region, endpoint: need(env, "S3_ENDPOINT"), bucket: bucketName(name, o) });
}
const extTypes = {
    png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif", svg: "image/svg+xml",
    ico: "image/x-icon", pdf: "application/pdf", txt: "text/plain; charset=utf-8", html: "text/html; charset=utf-8", css: "text/css",
    js: "text/javascript", json: "application/json", csv: "text/csv", xml: "application/xml", zip: "application/zip",
    mp4: "video/mp4", webm: "video/webm", mov: "video/quicktime", mp3: "audio/mpeg", wav: "audio/wav", ogg: "audio/ogg",
    woff2: "font/woff2", wasm: "application/wasm",
};
function guessType(key, data) {
    if (data instanceof Blob && data.type)
        return data.type;
    const dot = key.lastIndexOf(".");
    return dot > key.lastIndexOf("/") ? extTypes[key.slice(dot + 1).toLowerCase()] : undefined;
}
function checkKey(key) {
    if (!key || key.startsWith("/"))
        throw new Error("key must be a non-empty path without a leading /");
}
/** Stores data at key in a bucket (one PUT, up to 5 GiB). */
export async function upload(name, key, data, o) {
    checkKey(key);
    const env = envOf(o);
    const type = o?.contentType ?? guessType(key, data);
    const body = data instanceof Blob ? data : new Blob([typeof data === "string" ? data : data]);
    const req = signRequest({ method: "PUT", endpoint: need(env, "S3_ENDPOINT"), bucket: bucketName(name, o), key, creds: creds(env), headers: type ? { "content-type": type } : {} });
    const res = await fetch(req.url, { method: "PUT", headers: req.headers, body });
    if (!res.ok)
        throw new StorageError(res.status, await res.text());
    const out = { bucket: name, key, size: body.size, etag: (res.headers.get("etag") ?? "").replaceAll('"', "") };
    if (isPublic(name, o))
        out.url = publicUrl(name, key, o);
    return out;
}
/** An S3 error from the box (code such as QuotaExceeded, EntityTooLarge). */
export class StorageError extends Error {
    status;
    name = "StorageError";
    code;
    constructor(status, body) {
        const code = /<Code>([^<]*)<\/Code>/.exec(body)?.[1] ?? `HTTP${status}`;
        super(`${code}: ${/<Message>([^<]*)<\/Message>/.exec(body)?.[1] ?? (body.trim().slice(0, 200) || `HTTP ${status}`)}`);
        this.status = status;
        this.code = code;
    }
}
/** The query parameter of an upload URL that caps its size (checked by the box). */
const MAX_SIZE_PARAM = "x-tiffin-max-size";
function publicEndpoint(env) {
    return env.S3_PUBLIC_ENDPOINT ?? need(env, "S3_ENDPOINT");
}
/**
 * A presigned URL on the public S3 endpoint, for browsers and other
 * machines. It is computed locally: no request is made.
 */
export function presign(name, key, o) {
    const env = envOf(o);
    const query = {};
    const headers = {};
    if (o?.maxSize)
        query[MAX_SIZE_PARAM] = String(o.maxSize);
    if (o?.contentType)
        headers["content-type"] = o.contentType;
    return presignUrl({ method: o?.method ?? "GET", endpoint: publicEndpoint(env), bucket: bucketName(name, o), key, creds: creds(env), expiresIn: o?.expiresIn ?? 3600, query, headers });
}
/** Encodes a key for a URL path, keeping its slashes. */
export function encodeKey(key) {
    return key.split("/").map(encodeURIComponent).join("/");
}
/** Widths and qualities the box transforms to (internal/mod/storage/image.go). */
export const IMAGE_WIDTHS = [16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840];
export const IMAGE_QUALITIES = [50, 75, 90, 100];
/** Adds w, q and f for an image transform (nothing when im is empty). */
export function imageParams(u, im) {
    if (!im || (im.width === undefined && im.quality === undefined && im.format === undefined))
        return u;
    if (im.width !== undefined)
        u.searchParams.set("w", String(IMAGE_WIDTHS.find((w) => w >= im.width) ?? IMAGE_WIDTHS[IMAGE_WIDTHS.length - 1]));
    if (im.quality !== undefined) {
        const q = IMAGE_QUALITIES.reduce((a, b) => (Math.abs(b - im.quality) < Math.abs(a - im.quality) ? b : a));
        u.searchParams.set("q", String(q));
    }
    u.searchParams.set("f", im.format ?? "webp");
    return u;
}
/** The public URL of an object in a public bucket, optionally a resized image. */
export function publicUrl(name, key, o) {
    const env = envOf(o);
    if (!isPublic(name, o)) {
        throw new Error(`bucket "${name}" is private: use signedUrl() for a time-limited link, or set public: true in tiffin.config.ts`);
    }
    return imageParams(new URL(`${need(env, "TIFFIN_FILES_URL")}/${name}/${encodeKey(key)}`), o).toString();
}
/**
 * A time-limited link to an object on files.<domain>, for private buckets
 * (public ones work too). Resizing options can be added to it later, so it
 * works with the next/image loader.
 */
export function signedUrl(name, key, o) {
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
export async function contentKey(key, data) {
    let bytes;
    if (typeof data === "string")
        bytes = new TextEncoder().encode(data);
    else if (data instanceof Blob)
        bytes = new Uint8Array(await data.arrayBuffer());
    else if (data instanceof ArrayBuffer)
        bytes = new Uint8Array(data);
    else
        bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    const hash = createHash("sha256").update(bytes).digest("hex").slice(0, 16);
    const slash = key.lastIndexOf("/");
    const dot = key.lastIndexOf(".");
    return dot > slash + 1 ? `${key.slice(0, dot)}.${hash}${key.slice(dot)}` : `${key}.${hash}`;
}
const MiB = 1 << 20;
/**
 * Makes an upload ticket: presigned URLs a browser (uploadFile from
 * tiffin-sdk/storage/client) sends a file to, straight to the box's storage.
 * Call it from a route handler or a Server Action after checking who is
 * asking. A file over multipartThreshold gets a multipart upload: the
 * upload is created now, the ticket carries one URL per part.
 */
export async function createUpload(o) {
    checkKey(o.key);
    const env = envOf(o);
    const c = creds(env);
    const s3name = bucketName(o.bucket, o);
    const expiresIn = o.expiresIn ?? 3600;
    if (o.maxSize !== undefined && o.size !== undefined && o.size > o.maxSize) {
        throw new StorageError(413, `<Code>EntityTooLarge</Code><Message>the file is ${o.size} bytes; this upload takes up to ${o.maxSize}</Message>`);
    }
    const ticket = { bucket: o.bucket, key: o.key, expiresAt: new Date(Date.now() + expiresIn * 1000).toISOString() };
    if (o.contentType)
        ticket.contentType = o.contentType;
    if (o.size !== undefined)
        ticket.size = o.size;
    if (isPublic(o.bucket, o))
        ticket.publicUrl = publicUrl(o.bucket, o.key, o);
    const cap = {};
    const max = o.maxSize ?? o.size;
    if (max !== undefined)
        cap[MAX_SIZE_PARAM] = String(max);
    const sign = (method, query, headers = {}) => presignUrl({ method, endpoint: publicEndpoint(env), bucket: s3name, key: o.key, creds: c, expiresIn, query, headers });
    if (o.size === undefined || o.size <= (o.multipartThreshold ?? 64 * MiB)) {
        const headers = {};
        if (o.contentType)
            headers["content-type"] = o.contentType;
        if (o.size !== undefined)
            headers["content-length"] = String(o.size);
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
    if (!res.ok)
        throw new StorageError(res.status, text);
    const uploadId = /<UploadId>([^<]+)<\/UploadId>/.exec(text)?.[1];
    if (!uploadId)
        throw new StorageError(502, `<Code>BadResponse</Code><Message>no UploadId in ${text.slice(0, 200)}</Message>`);
    const parts = [];
    for (let i = 0, n = Math.ceil(size / partSize); i < n; i++) {
        const len = Math.min(partSize, size - i * partSize);
        parts.push(sign("PUT", { ...cap, partNumber: String(i + 1), uploadId }, { "content-length": String(len) }));
    }
    ticket.multipart = { uploadId, partSize, parts, complete: sign("POST", { ...cap, uploadId }), list: sign("GET", { uploadId }), abort: sign("DELETE", { uploadId }) };
    return ticket;
}
function typeMatches(globs, type) {
    if (!globs?.length)
        return true;
    const t = type.split(";")[0].trim().toLowerCase();
    return globs.some((g) => (g.endsWith("/*") ? t.startsWith(g.slice(0, -1).toLowerCase()) : t === g.toLowerCase()));
}
/** A file name safe as the last part of a key. */
function safeName(name) {
    const base = name.split(/[\\/]/).pop() ?? "";
    // eslint-disable-next-line no-control-regex
    return base.replace(/[\u0000-\u001f\u007f]/g, "").replace(/^\.+/, "").slice(-200) || "file";
}
/**
 * A POST route handler that answers uploadFile() with a ticket, after its
 * checks: `export const POST = uploadRoute({ bucket: "media" })` in
 * app/api/upload/route.ts (Next.js), or any `(Request) => Response` server.
 */
export function uploadRoute(o) {
    const refuse = (status, code, error) => Response.json({ code, error }, { status });
    return async (req) => {
        let file;
        try {
            file = (await req.json());
        }
        catch {
            return refuse(400, "BadRequest", "send JSON { name, size, type }");
        }
        if (typeof file?.name !== "string" || typeof file.size !== "number" || !Number.isSafeInteger(file.size) || file.size < 0 || typeof file.type !== "string") {
            return refuse(400, "BadRequest", "send JSON { name, size, type }");
        }
        try {
            if (o.authorize && (await o.authorize(file, req)) === false)
                return refuse(403, "Forbidden", "this upload is not allowed");
        }
        catch (e) {
            return refuse(403, "Forbidden", e instanceof Error ? e.message : "this upload is not allowed");
        }
        if (o.maxSize !== undefined && file.size > o.maxSize)
            return refuse(413, "EntityTooLarge", `files can be up to ${o.maxSize} bytes; this one is ${file.size}`);
        if (!typeMatches(o.allowedTypes, file.type))
            return refuse(415, "InvalidContentType", `${file.type || "this type"} is not accepted (${o.allowedTypes.join(", ")})`);
        const key = o.key ? await o.key(file, req) : `uploads/${randomUUID()}/${safeName(file.name)}`;
        const opts = { bucket: o.bucket, key, size: file.size, contentType: file.type || "application/octet-stream" };
        if (o.env)
            opts.env = o.env;
        if (o.maxSize !== undefined)
            opts.maxSize = o.maxSize;
        if (o.expiresIn !== undefined)
            opts.expiresIn = o.expiresIn;
        try {
            return Response.json(await createUpload(opts));
        }
        catch (e) {
            if (e instanceof StorageError)
                return refuse(e.status, e.code, e.message);
            throw e;
        }
    };
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
export function onUploadCompleted(fn, opts) {
    return defineHandler((job) => fn(job.payload, job), opts);
}
