import { type HandlerOptions, type Job } from "./queue.js";
import type { UploadTicket } from "./client/upload.js";
export type { UploadTicket, UploadResult, UploadProgress, UploadFileOptions } from "./client/upload.js";
/** Where settings come from; defaults to process.env. */
export type Env = Record<string, string | undefined>;
export interface StorageOptions {
    env?: Env;
}
/** The S3 bucket name for a bucket in tiffin.config.ts ("media" → "shop-media"). */
export declare function bucketName(name: string, o?: StorageOptions): string;
/** Whether a bucket is public (readable at its publicUrl without a signature). */
export declare function isPublic(name: string, o?: StorageOptions): boolean;
/**
 * Bun's S3Client where Bun's types are loaded, else unknown: the package's
 * declarations name no Bun type, so a Node app type-checks them without
 * installing @types/bun.
 */
export type BunS3Client = typeof globalThis extends {
    Bun: {
        S3Client: new (...args: never[]) => infer C;
    };
} ? C : unknown;
/** A Bun.S3Client for one of the project's buckets, on the box-internal endpoint (Bun only). */
export declare function bucket(name: string, o?: StorageOptions): BunS3Client;
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
/** Stores data at key in a bucket (one PUT, up to 5 GiB). */
export declare function upload(name: string, key: string, data: Body, o?: UploadOptions): Promise<Uploaded>;
/** An S3 error from the box (code such as QuotaExceeded, EntityTooLarge). */
export declare class StorageError extends Error {
    readonly status: number;
    name: string;
    readonly code: string;
    constructor(status: number, body: string);
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
/**
 * A presigned URL on the public S3 endpoint, for browsers and other
 * machines. It is computed locally: no request is made.
 */
export declare function presign(name: string, key: string, o?: PresignOptions): string;
/** Encodes a key for a URL path, keeping its slashes. */
export declare function encodeKey(key: string): string;
export interface ImageOptions {
    /** Resize to this width (one of 16 … 3840, the next/image sizes; others are rounded up). Never enlarges. */
    width?: number;
    /** 50, 75 (default), 90 or 100. */
    quality?: number;
    /** "webp" (default when resizing), "avif" or "original". */
    format?: "webp" | "avif" | "original";
}
/** Widths and qualities the box transforms to (internal/mod/storage/image.go). */
export declare const IMAGE_WIDTHS: readonly [16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840];
export declare const IMAGE_QUALITIES: readonly [50, 75, 90, 100];
/** Adds w, q and f for an image transform (nothing when im is empty). */
export declare function imageParams(u: URL, im?: ImageOptions): URL;
/** The public URL of an object in a public bucket, optionally a resized image. */
export declare function publicUrl(name: string, key: string, o?: StorageOptions & ImageOptions): string;
export interface SignedUrlOptions extends StorageOptions, ImageOptions {
    /** Seconds the link works (default 3600). */
    expiresIn?: number;
}
/**
 * A time-limited link to an object on files.<domain>, for private buckets
 * (public ones work too). Resizing options can be added to it later, so it
 * works with the next/image loader.
 */
export declare function signedUrl(name: string, key: string, o?: SignedUrlOptions): string;
/**
 * A content-addressed key: "img/logo.png" + bytes → "img/logo.<16 hex>.png".
 * Public files whose key carries a hash are served with a one-year
 * immutable cache, because their bytes can never change.
 */
export declare function contentKey(key: string, data: Body): Promise<string>;
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
/**
 * Makes an upload ticket: presigned URLs a browser (uploadFile from
 * @shiptiffin/sdk/client) sends a file to, straight to the box's storage.
 * Call it from a route handler or a Server Action after checking who is
 * asking. A file over multipartThreshold gets a multipart upload: the
 * upload is created now, the ticket carries one URL per part.
 */
export declare function createUpload(o: CreateUploadOptions): Promise<UploadTicket>;
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
/**
 * A POST route handler that answers uploadFile() with a ticket, after its
 * checks: `export const POST = uploadRoute({ bucket: "media" })` in
 * app/api/upload/route.ts (Next.js), or any `(Request) => Response` server.
 */
export declare function uploadRoute(o: UploadRouteOptions): (req: Request) => Promise<Response>;
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
export declare const OBJECT_CREATED_TOPIC = "storage.object.created";
/**
 * A queue handler for upload events. Declare the topic with a subscriber
 * queue in tiffin.config.ts, and serve this handler on that queue's path:
 *
 * ```ts
 * queues: { uploads: { app: "web" } },                          // POST /queues/uploads
 * topics: { "storage.object.created": { subscribers: ["uploads"] } },
 * ```
 */
export declare function onUploadCompleted(fn: (e: ObjectCreated, job: Job<ObjectCreated>) => unknown, opts?: HandlerOptions): (req: Request) => Promise<Response>;
