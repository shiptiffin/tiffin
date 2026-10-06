/**
 * Upload files from the browser straight to the box's storage, with
 * progress, parallel parts, retries, pause and resume.
 *
 * ```ts
 * import { uploadFile } from "@shiptiffin/sdk/client";
 *
 * // A route made with uploadRoute() from @shiptiffin/sdk/storage (or a ticket from createUpload()):
 * const done = await uploadFile(file, "/api/upload", {
 *   onProgress: (p) => setPercent(p.percent),
 *   signal: controller.signal,
 * });
 * done.key; done.url; // url for public buckets
 * ```
 *
 * The bytes never pass through your app: the server hands out a ticket of
 * presigned URLs (createUpload) and the browser sends the file to
 * s3.<domain>. Files over the ticket's multipart threshold go up in parts,
 * several at a time; a part that fails is retried. To pause, abort the
 * signal; to resume, call uploadFile again with the same ticket (keep it
 * with onTicket): the parts already stored are skipped.
 * Nothing here needs Node: it runs in any browser (and in Bun or Node, with
 * fetch instead of progress events).
 */
/** What createUpload() returns: plain JSON, safe to send to the browser. */
export interface UploadTicket {
    bucket: string;
    key: string;
    contentType?: string;
    /** The file's exact size, when the ticket was made for one. */
    size?: number;
    /** When the URLs stop working. */
    expiresAt: string;
    /** Single upload: PUT the file here with these headers. */
    url?: string;
    headers?: Record<string, string>;
    /** Large files: upload parts, then complete. */
    multipart?: {
        uploadId: string;
        partSize: number;
        /** One presigned PUT URL per part, in order (part 1 first). */
        parts: string[];
        /** POST the part list here to finish. */
        complete: string;
        /** GET here to list the parts already stored (resume). */
        list: string;
        /** DELETE here to give up and free the parts. */
        abort: string;
    };
    /** Where the file can be read afterwards (public buckets). */
    publicUrl?: string;
}
export interface UploadProgress {
    loaded: number;
    total: number;
    /** 0-100. */
    percent: number;
}
export interface UploadFileOptions {
    onProgress?: (p: UploadProgress) => void;
    signal?: AbortSignal;
    /** Parts in flight at once. Default 4. */
    concurrency?: number;
    /** Tries per request before giving up. Default 4. */
    attempts?: number;
    /** Called with the ticket before any bytes are sent: keep it to resume later. */
    onTicket?: (ticket: UploadTicket) => void;
    /** Extra fields sent to an upload route with the file's name, size and type. */
    payload?: unknown;
    fetch?: typeof fetch;
}
export interface UploadResult {
    bucket: string;
    key: string;
    size: number;
    etag: string;
    /** Public URL (public buckets only). */
    url?: string;
}
/** An upload the box refused (code is the S3 error code, e.g. EntityTooLarge). */
export declare class UploadError extends Error {
    readonly status: number;
    readonly code: string;
    name: string;
    constructor(status: number, code: string, message: string);
}
/**
 * Uploads a file (any Blob) with a ticket, or asks an upload route
 * (uploadRoute() in @shiptiffin/sdk/storage) for one first.
 */
export declare function uploadFile(file: Blob, target: UploadTicket | string, o?: UploadFileOptions): Promise<UploadResult>;
/** Gives up on a multipart upload and frees its stored parts. */
export declare function abortUpload(ticket: UploadTicket, o?: {
    fetch?: typeof fetch;
}): Promise<void>;
