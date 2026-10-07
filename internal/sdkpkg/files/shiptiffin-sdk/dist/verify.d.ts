/** Verifies a Tiffin-Signature header over the raw body. */
export declare function verifySignature(secret: string, header: string | null, body: string, toleranceSeconds?: number, now?: number): boolean;
/** The largest job or cron call body read by default (payloads are at most 1 MB). */
export declare const MAX_CALL_BYTES: number;
/**
 * @internal Reads a signed request's body: null (answer 401) when the
 * signature header is missing, malformed or stale, before reading anything;
 * "too large" (answer 413) past maxBytes, without buffering the rest.
 */
export declare function readSigned(req: Request, maxBytes: number, toleranceSeconds?: number): Promise<string | null | "too large">;
/** What the box sends in every call: the job, its attempt and your payload. */
export interface SignedCall<T = unknown> {
    type: "job" | "cron" | "workflow";
    /** Job ID, e.g. job_42 (the same on every retry of one job: use it to skip duplicates). */
    id: string;
    queue: string;
    cron?: string;
    topic?: string;
    key?: string;
    /** 1 for the first try. */
    attempt: number;
    maxAttempts: number;
    enqueuedAt: string;
    payload: T;
}
/**
 * Reads a request's body and checks its Tiffin-Signature. Returns the parsed
 * call, or null when the signature is missing, wrong or too old (answer 401)
 * or the body is over maxBytes (default 2 MB). A request without a
 * well-formed, fresh signature header is refused before its body is read.
 */
export declare function verifyRequest<T = unknown>(req: Request, secret: string, toleranceSeconds?: number, maxBytes?: number): Promise<SignedCall<T> | null>;
