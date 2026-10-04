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
/** The S3 bucket name for a bucket in tiffin.config.ts ("media" → "shop-media"). */
export declare function bucketName(name: string, o?: StorageOptions): string;
/** Whether a bucket is public (readable at its publicUrl without a signature). */
export declare function isPublic(name: string, o?: StorageOptions): boolean;
/** A Bun.S3Client for one of the project's buckets, on the box-internal endpoint. */
export declare function bucket(name: string, o?: StorageOptions): Bun.S3Client;
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
export declare function upload(name: string, key: string, data: Body, o?: UploadOptions): Promise<Uploaded>;
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
export declare function presign(name: string, key: string, o?: PresignOptions): string;
/** Encodes a key for a URL path, keeping its slashes. */
export declare function encodeKey(key: string): string;
/** The public URL of an object in a public bucket. */
export declare function publicUrl(name: string, key: string, o?: StorageOptions): string;
/**
 * A content-addressed key: "img/logo.png" + bytes → "img/logo.<16 hex>.png".
 * Public files whose key carries a hash are served with a one-year
 * immutable cache, because their bytes can never change.
 */
export declare function contentKey(key: string, data: string | ArrayBuffer | ArrayBufferView | Blob): Promise<string>;
export {};
