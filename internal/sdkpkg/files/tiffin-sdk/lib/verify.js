/**
 * `tiffin-sdk/verify`: check that a request really came from your box.
 *
 * Every call a Tiffin cron or queue makes (to an app, or to a URL outside the
 * box) carries a `Tiffin-Signature` header: `t=<unix seconds>,v1=<hex>`, the
 * hex being HMAC-SHA256 of `<t>.<raw body>` keyed with the project's signing
 * secret (`tiffin queue signing-secret <project>`; apps on the box have it as
 * TIFFIN_QUEUE_SIGNING_SECRET).
 *
 * ```ts
 * import { verifyRequest } from "tiffin-sdk/verify";
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
export function verifySignature(secret, header, body, toleranceSeconds = 300, now = Date.now()) {
    if (!secret || !header)
        return false;
    let ts = "";
    let sig = "";
    for (const part of header.split(",")) {
        const [k, v] = part.trim().split("=", 2);
        if (k === "t")
            ts = v ?? "";
        if (k === "v1")
            sig = v ?? "";
    }
    const t = Number(ts);
    if (!Number.isInteger(t) || !sig || Math.abs(now / 1000 - t) > toleranceSeconds)
        return false;
    const want = createHmac("sha256", secret).update(`${ts}.${body}`).digest("hex");
    return want.length === sig.length && timingSafeEqual(Buffer.from(want), Buffer.from(sig));
}
/**
 * Reads a request's body and checks its Tiffin-Signature. Returns the parsed
 * call, or null when the signature is missing, wrong or too old (answer 401).
 */
export async function verifyRequest(req, secret, toleranceSeconds = 300) {
    const body = await req.text();
    if (!verifySignature(secret, req.headers.get("tiffin-signature"), body, toleranceSeconds))
        return null;
    try {
        return JSON.parse(body);
    }
    catch {
        return null;
    }
}
