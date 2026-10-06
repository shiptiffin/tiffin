/**
 * AWS Signature Version 4 for S3, just enough for tiffin-sdk/storage:
 * presigned URLs and header-signed requests. node:crypto only, so it runs
 * on Bun and Node alike. Internal: not a package export.
 */
import { createHash, createHmac } from "node:crypto";
const UNSIGNED = "UNSIGNED-PAYLOAD";
/** URI-encodes every byte but A-Z a-z 0-9 - . _ ~ (and "/" when keepSlash). */
export function s3Escape(s, keepSlash = false) {
    const e = encodeURIComponent(s).replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase());
    return keepSlash ? e.replaceAll("%2F", "/") : e;
}
/** The path-style path of a bucket and key. */
export function objectPath(bucket, key) {
    return key ? `/${bucket}/${s3Escape(key, true)}` : `/${bucket}`;
}
function canonicalQuery(q) {
    return Object.keys(q)
        .sort()
        .map((k) => `${s3Escape(k)}=${s3Escape(q[k])}`)
        .join("&");
}
function hmac(key, data) {
    return createHmac("sha256", key).update(data).digest();
}
function sha256Hex(s) {
    return createHash("sha256").update(s).digest("hex");
}
function amzDate(now) {
    return now.toISOString().replace(/[-:]/g, "").replace(/\.\d{3}/, "");
}
function signature(c, now, canonicalRequest) {
    const date = amzDate(now).slice(0, 8);
    const scope = `${date}/${c.region}/s3/aws4_request`;
    const sts = `AWS4-HMAC-SHA256\n${amzDate(now)}\n${scope}\n${sha256Hex(canonicalRequest)}`;
    let k = hmac("AWS4" + c.secretAccessKey, date);
    k = hmac(k, c.region);
    k = hmac(k, "s3");
    k = hmac(k, "aws4_request");
    return hmac(k, sts).toString("hex");
}
function canonicalHeaders(headers) {
    const lower = {};
    for (const [k, v] of Object.entries(headers))
        lower[k.toLowerCase()] = String(v).trim();
    const names = Object.keys(lower).sort();
    return { block: names.map((n) => `${n}:${lower[n]}\n`).join(""), signed: names.join(";") };
}
/** A presigned URL, the same as the box's own (internal/mod/storage PresignWith). */
export function presignUrl(i) {
    const u = new URL(i.endpoint);
    const now = i.now ?? new Date();
    const path = objectPath(i.bucket, i.key);
    const { block, signed } = canonicalHeaders({ host: u.host, ...i.headers });
    const q = {
        ...i.query,
        "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
        "X-Amz-Credential": `${i.creds.accessKeyId}/${amzDate(now).slice(0, 8)}/${i.creds.region}/s3/aws4_request`,
        "X-Amz-Date": amzDate(now),
        "X-Amz-Expires": String(Math.round(i.expiresIn)),
        "X-Amz-SignedHeaders": signed,
    };
    const cr = `${i.method}\n${path}\n${canonicalQuery(q)}\n${block}\n${signed}\n${UNSIGNED}`;
    q["X-Amz-Signature"] = signature(i.creds, now, cr);
    return `${u.protocol}//${u.host}${path}?${canonicalQuery(q)}`;
}
/** The URL and headers (Authorization included) of a header-signed request with an unsigned payload. */
export function signRequest(i) {
    const u = new URL(i.endpoint);
    const now = i.now ?? new Date();
    const path = objectPath(i.bucket, i.key);
    const headers = { ...i.headers, "x-amz-date": amzDate(now), "x-amz-content-sha256": UNSIGNED };
    const toSign = { host: u.host };
    for (const [k, v] of Object.entries(headers)) {
        const lk = k.toLowerCase();
        if (lk.startsWith("x-amz-") || lk === "content-type" || lk === "content-md5" || lk === "range")
            toSign[lk] = v;
    }
    const { block, signed } = canonicalHeaders(toSign);
    const query = canonicalQuery(i.query ?? {});
    const cr = `${i.method}\n${path}\n${query}\n${block}\n${signed}\n${UNSIGNED}`;
    const date = amzDate(now).slice(0, 8);
    headers.authorization = `AWS4-HMAC-SHA256 Credential=${i.creds.accessKeyId}/${date}/${i.creds.region}/s3/aws4_request, SignedHeaders=${signed}, Signature=${signature(i.creds, now, cr)}`;
    return { url: `${u.protocol}//${u.host}${path}${query ? "?" + query : ""}`, headers };
}
