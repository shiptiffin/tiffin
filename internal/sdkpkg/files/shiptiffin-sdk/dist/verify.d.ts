/** Verifies a Tiffin-Signature header over the raw body. */
export declare function verifySignature(secret: string, header: string | null, body: string, toleranceSeconds?: number, now?: number): boolean;
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
 * call, or null when the signature is missing, wrong or too old (answer 401).
 */
export declare function verifyRequest<T = unknown>(req: Request, secret: string, toleranceSeconds?: number): Promise<SignedCall<T> | null>;
