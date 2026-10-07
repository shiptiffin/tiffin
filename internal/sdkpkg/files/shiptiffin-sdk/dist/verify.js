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
 *   const call = await verifyRequest(req, process.env.TIFFIN_QUEUE_SIGNING_SECRET!);
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
/** Reads a Tiffin-Signature header's parts if it is well formed and fresh. */
function signatureParts(header, toleranceSeconds, now) {
    if (!header)
        return null;
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
    if (!Number.isInteger(t) || !/^[0-9a-f]{64}$/.test(sig) || Math.abs(now / 1000 - t) > toleranceSeconds)
        return null;
    return { ts, sig };
}
/** Verifies a Tiffin-Signature header over the raw body. */
export function verifySignature(secret, header, body, toleranceSeconds = 300, now = Date.now()) {
    const p = signatureParts(header, toleranceSeconds, now);
    if (!secret || !p)
        return false;
    const want = createHmac("sha256", secret).update(`${p.ts}.${body}`).digest("hex");
    return timingSafeEqual(Buffer.from(want), Buffer.from(p.sig));
}
/** The largest job or cron call body read by default (payloads are at most 1 MB). */
export const MAX_CALL_BYTES = 2 << 20;
/**
 * @internal Reads a signed request's body: null (answer 401) when the
 * signature header is missing, malformed or stale, before reading anything;
 * "too large" (answer 413) past maxBytes, without buffering the rest.
 */
export async function readSigned(req, maxBytes, toleranceSeconds = 300) {
    if (!signatureParts(req.headers.get("tiffin-signature"), toleranceSeconds, Date.now()))
        return null;
    const declared = Number(req.headers.get("content-length") ?? "");
    if (Number.isFinite(declared) && declared > maxBytes)
        return "too large";
    if (!req.body)
        return "";
    const reader = req.body.getReader();
    const chunks = [];
    let n = 0;
    for (;;) {
        const { done, value } = await reader.read();
        if (done)
            break;
        n += value.byteLength;
        if (n > maxBytes) {
            await reader.cancel().catch(() => { });
            return "too large";
        }
        chunks.push(value);
    }
    return Buffer.concat(chunks).toString("utf8");
}
/**
 * Reads a request's body and checks its Tiffin-Signature. Returns the parsed
 * call, or null when the signature is missing, wrong or too old (answer 401)
 * or the body is over maxBytes (default 2 MB). A request without a
 * well-formed, fresh signature header is refused before its body is read.
 */
export async function verifyRequest(req, secret, toleranceSeconds = 300, maxBytes = MAX_CALL_BYTES) {
    const body = await readSigned(req, maxBytes, toleranceSeconds);
    if (body === null || body === "too large")
        return null;
    if (!verifySignature(secret, req.headers.get("tiffin-signature"), body, toleranceSeconds))
        return null;
    try {
        return JSON.parse(body);
    }
    catch {
        return null;
    }
}
