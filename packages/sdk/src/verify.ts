/**
 * `@shiptiffin/sdk/verify`: check that a request really came from your box.
 *
 * Every call a Tiffin cron or queue makes (to an app, or to a URL outside the
 * box) carries a `Tiffin-Signature` header: `t=<unix seconds>,v1=<hex>`, the
 * hex being HMAC-SHA256 of `<t>.<raw body>` keyed with the project's signing
 * secret (`tiffin queue signing-secret <project>`; apps on the box have it as
 * TIFFIN_QUEUE_SIGNING_SECRET).
 *
 * ```ts
 * import { verifyRequest } from "@shiptiffin/sdk/verify";
 *
 * export async function POST(req: Request) {
 *   const call = await verifyRequest(req, process.env.TIFFIN_SIGNING_SECRET!);
 *   if (!call) return new Response("bad signature", { status: 401 });
 *   console.log(call.cron ?? call.queue, call.payload);  // { type, id, queue, cron?, attempt, payload, ... }
 *   return new Response(null, { status: 204 });           // 2xx: done; 489: don't retry; anything else retries
 * }
 * ```
 *
 * No SDK? Any HMAC library does: split the header on ",", recompute
 * HMAC-SHA256(secret, t + "." + body), compare in constant time, and reject
 * timestamps more than five minutes away.
 */
import { createHmac, timingSafeEqual } from "node:crypto";

/** Verifies a Tiffin-Signature header over the raw body. */
export function verifySignature(secret: string, header: string | null, body: string, toleranceSeconds = 300, now = Date.now()): boolean {
  if (!secret || !header) return false;
  let ts = "";
  let sig = "";
  for (const part of header.split(",")) {
    const [k, v] = part.trim().split("=", 2);
    if (k === "t") ts = v ?? "";
    if (k === "v1") sig = v ?? "";
  }
  const t = Number(ts);
  if (!Number.isInteger(t) || !sig || Math.abs(now / 1000 - t) > toleranceSeconds) return false;
  const want = createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex");
  return want.length === sig.length && timingSafeEqual(Buffer.from(want), Buffer.from(sig));
}

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
export async function verifyRequest<T = unknown>(req: Request, secret: string, toleranceSeconds = 300): Promise<SignedCall<T> | null> {
  const body = await req.text();
  if (!verifySignature(secret, req.headers.get("tiffin-signature"), body, toleranceSeconds)) return null;
  try {
    return JSON.parse(body) as SignedCall<T>;
  } catch {
    return null;
  }
}
