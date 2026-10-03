/**
 * `tiffin-sdk/storage`: thin helpers over `Bun.s3` for the project's buckets.
 *
 * ```ts
 * import { bucket, upload, presign, publicUrl, contentKey } from "tiffin-sdk/storage";
 *
 * await upload("media", "avatars/42.png", file, { contentType: "image/png" });
 * const url = presign("media", "avatars/42.png", { expiresIn: 600 });        // browser download link
 * const put = presign("media", "uploads/a.bin", { method: "PUT" });          // browser upload link
 * const key = await contentKey("img/logo.png", bytes);  // "img/logo.3f9a2c1d8e7b6a50.png": cached for a year
 * publicUrl("assets", key);                             // public buckets only
 * const s3 = bucket("media");                           // a Bun.S3Client for anything else
 * ```
 *
 * The box gives every app S3_ENDPOINT, S3_REGION, S3_ACCESS_KEY_ID,
 * S3_SECRET_ACCESS_KEY and S3_BUCKET_<NAME> (plus AWS_* twins), so `Bun.s3`
 * and the AWS SDKs work with no setup. These helpers add what they do not
 * know: bucket names from tiffin.config.ts, presigned URLs on the public
 * endpoint (S3_PUBLIC_ENDPOINT, reachable from browsers) and public file URLs
 * (TIFFIN_FILES_URL).
 */

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

function credentials(env: Env) {
  return {
    accessKeyId: need(env, "S3_ACCESS_KEY_ID"),
    secretAccessKey: need(env, "S3_SECRET_ACCESS_KEY"),
    region: env.S3_REGION ?? "us-east-1",
  };
}

/** A Bun.S3Client for one of the project's buckets, on the box-internal endpoint. */
export function bucket(name: string, o?: StorageOptions): Bun.S3Client {
  const env = envOf(o);
  return new Bun.S3Client({ ...credentials(env), endpoint: need(env, "S3_ENDPOINT"), bucket: bucketName(name, o) });
}

export interface UploadOptions extends StorageOptions {
  /** MIME type; Bun guesses one from the data when it can. */
  contentType?: string;
}

export interface Uploaded {
  bucket: string;
  key: string;
  size: number;
  /** The public URL, for public buckets. */
  url?: string;
}

/** What upload() accepts: anything Bun.S3Client.write takes. */
type Body = Parameters<Bun.S3Client["write"]>[1];

/** Stores data at key in a bucket. */
export async function upload(name: string, key: string, data: Body, o?: UploadOptions): Promise<Uploaded> {
  if (!key || key.startsWith("/")) throw new Error("key must be a non-empty path without a leading /");
  const size = await bucket(name, o).write(key, data, o?.contentType ? { type: o.contentType } : {});
  const out: Uploaded = { bucket: name, key, size };
  if (isPublic(name, o)) out.url = publicUrl(name, key, o);
  return out;
}

export interface PresignOptions extends StorageOptions {
  /** GET (download, the default) or PUT (upload). */
  method?: "GET" | "PUT";
  /** Seconds the URL is valid (default 3600, max 604800). */
  expiresIn?: number;
}

/**
 * A presigned URL on the public S3 endpoint, for browsers and other
 * machines. It is computed locally: no request is made.
 */
export function presign(name: string, key: string, o?: PresignOptions): string {
  const env = envOf(o);
  const endpoint = env.S3_PUBLIC_ENDPOINT ?? need(env, "S3_ENDPOINT");
  const client = new Bun.S3Client({ ...credentials(env), endpoint, bucket: bucketName(name, o) });
  return client.presign(key, { method: o?.method ?? "GET", expiresIn: o?.expiresIn ?? 3600 });
}

/** Encodes a key for a URL path, keeping its slashes. */
export function encodeKey(key: string): string {
  return key.split("/").map(encodeURIComponent).join("/");
}

/** The public URL of an object in a public bucket. */
export function publicUrl(name: string, key: string, o?: StorageOptions): string {
  const env = envOf(o);
  if (!isPublic(name, o)) {
    throw new Error(`bucket "${name}" is private: use presign() for a time-limited link, or set public: true in tiffin.config.ts`);
  }
  return `${need(env, "TIFFIN_FILES_URL")}/${name}/${encodeKey(key)}`;
}

/**
 * A content-addressed key: "img/logo.png" + bytes → "img/logo.<16 hex>.png".
 * Public files whose key carries a hash are served with a one-year
 * immutable cache, because their bytes can never change.
 */
export async function contentKey(key: string, data: string | ArrayBuffer | ArrayBufferView | Blob): Promise<string> {
  let bytes: Uint8Array;
  if (typeof data === "string") bytes = new TextEncoder().encode(data);
  else if (data instanceof Blob) bytes = new Uint8Array(await data.arrayBuffer());
  else if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
  else bytes = new Uint8Array(data.buffer as ArrayBuffer, data.byteOffset, data.byteLength);
  const hash = new Bun.CryptoHasher("sha256").update(bytes).digest("hex").slice(0, 16);
  const slash = key.lastIndexOf("/");
  const dot = key.lastIndexOf(".");
  return dot > slash + 1 ? `${key.slice(0, dot)}.${hash}${key.slice(dot)}` : `${key}.${hash}`;
}
