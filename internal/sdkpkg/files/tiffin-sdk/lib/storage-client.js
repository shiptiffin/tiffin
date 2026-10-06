/**
 * `tiffin-sdk/storage/client`: upload files from the browser straight to
 * the box's storage, with progress, parallel parts, retries and resume.
 *
 * ```ts
 * import { uploadFile } from "tiffin-sdk/storage/client";
 *
 * // A route made with uploadRoute() from tiffin-sdk/storage (or a ticket from createUpload()):
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
 * several at a time; a part that fails is retried, and calling uploadFile
 * again with the same ticket resumes, skipping the parts already stored.
 * Nothing here needs Node: it runs in any browser (and in Bun or Node, with
 * fetch instead of progress events).
 */
/** An upload the box refused (code is the S3 error code, e.g. EntityTooLarge). */
export class UploadError extends Error {
    status;
    code;
    name = "UploadError";
    constructor(status, code, message) {
        super(message);
        this.status = status;
        this.code = code;
    }
}
/**
 * Uploads a file (any Blob) with a ticket, or asks an upload route
 * (uploadRoute() in tiffin-sdk/storage) for one first.
 */
export async function uploadFile(file, target, o = {}) {
    const f = o.fetch ?? fetch;
    const ticket = typeof target === "string" ? await requestTicket(f, target, file, o) : target;
    const total = file.size;
    if (ticket.size !== undefined && ticket.size !== total) {
        throw new UploadError(0, "SizeMismatch", `the ticket is for ${ticket.size} bytes; this file has ${total}`);
    }
    const report = (loaded) => o.onProgress?.({ loaded, total, percent: total ? Math.min(100, (loaded / total) * 100) : 100 });
    const attempts = o.attempts ?? 4;
    report(0);
    let etag;
    if (ticket.multipart) {
        etag = await uploadParts(file, ticket.multipart, o, attempts, report);
    }
    else if (ticket.url) {
        const res = await retry(attempts, o.signal, () => send("PUT", ticket.url, file, ticket.headers ?? {}, o.signal, report, f));
        etag = res.etag;
    }
    else {
        throw new UploadError(0, "BadTicket", "the ticket has neither a url nor multipart parts");
    }
    report(total);
    const out = { bucket: ticket.bucket, key: ticket.key, size: total, etag: etag.replaceAll('"', "") };
    if (ticket.publicUrl)
        out.url = ticket.publicUrl;
    return out;
}
/** Gives up on a multipart upload and frees its stored parts. */
export async function abortUpload(ticket, o = {}) {
    if (!ticket.multipart)
        return;
    const res = await (o.fetch ?? fetch)(ticket.multipart.abort, { method: "DELETE" });
    if (!res.ok && res.status !== 404)
        throw await s3Error(res.status, await res.text());
}
async function requestTicket(f, route, file, o) {
    const name = file.name ?? "file";
    const init = {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ name, size: file.size, type: file.type, payload: o.payload }),
    };
    if (o.signal)
        init.signal = o.signal;
    const res = await f(route, init);
    const body = (await res.json().catch(() => ({})));
    if (!res.ok)
        throw new UploadError(res.status, body.code ?? "Refused", body.error ?? `the upload route answered ${res.status}`);
    return body;
}
async function uploadParts(file, mp, o, attempts, report) {
    const f = o.fetch ?? fetch;
    const n = mp.parts.length;
    const sizeOf = (i) => Math.min(mp.partSize, file.size - i * mp.partSize);
    const etags = Array.from({ length: n }, () => undefined);
    // Resume: parts already stored (with the right size) are skipped.
    const listed = await retry(attempts, o.signal, async () => {
        const res = await f(mp.list, o.signal ? { signal: o.signal } : {});
        const text = await res.text();
        if (res.status === 404)
            throw new UploadError(404, "NoSuchUpload", "this upload was aborted or completed already; ask for a new ticket");
        if (!res.ok)
            throw await s3Error(res.status, text);
        return text;
    });
    for (const m of listed.matchAll(/<Part>([\s\S]*?)<\/Part>/g)) {
        const num = Number(tag(m[1], "PartNumber"));
        if (num >= 1 && num <= n && Number(tag(m[1], "Size")) === sizeOf(num - 1))
            etags[num - 1] = tag(m[1], "ETag");
    }
    const inFlight = new Map();
    let doneBytes = etags.reduce((s, e, i) => (e ? s + sizeOf(i) : s), 0);
    const progress = () => report(doneBytes + [...inFlight.values()].reduce((a, b) => a + b, 0));
    const todo = etags.flatMap((e, i) => (e ? [] : [i]));
    const worker = async () => {
        for (let i = todo.shift(); i !== undefined; i = todo.shift()) {
            const idx = i;
            const blob = file.slice(idx * mp.partSize, idx * mp.partSize + sizeOf(idx));
            const res = await retry(attempts, o.signal, () => {
                inFlight.set(idx, 0);
                return send("PUT", mp.parts[idx], blob, {}, o.signal, (loaded) => {
                    inFlight.set(idx, loaded);
                    progress();
                }, f);
            });
            inFlight.delete(idx);
            etags[idx] = res.etag;
            doneBytes += blob.size;
            progress();
        }
    };
    await Promise.all(Array.from({ length: Math.max(1, Math.min(o.concurrency ?? 4, todo.length)) }, worker));
    const xml = "<CompleteMultipartUpload>" +
        etags.map((e, i) => `<Part><PartNumber>${i + 1}</PartNumber><ETag>${escapeXml(e ?? "")}</ETag></Part>`).join("") +
        "</CompleteMultipartUpload>";
    const done = await retry(attempts, o.signal, async () => {
        const res = await f(mp.complete, { method: "POST", body: xml, headers: { "content-type": "application/xml" }, ...(o.signal ? { signal: o.signal } : {}) });
        const text = await res.text();
        // S3 can answer 200 with an error in the body.
        if (!res.ok || text.includes("<Error>"))
            throw await s3Error(res.ok ? 500 : res.status, text);
        return text;
    });
    return tag(done, "ETag");
}
function tag(xml, name) {
    const m = new RegExp(`<${name}>([\\s\\S]*?)</${name}>`).exec(xml);
    return (m?.[1] ?? "").replaceAll("&quot;", '"').replaceAll("&amp;", "&");
}
function escapeXml(s) {
    return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}
async function s3Error(status, body) {
    const code = tag(body, "Code") || "HTTP" + status;
    const msg = tag(body, "Message") || body.trim().slice(0, 200) || `HTTP ${status}`;
    return new UploadError(status, code, msg);
}
/** Network errors, timeouts, throttling and 5xx are worth another try. */
function retryable(e) {
    if (e instanceof UploadError)
        return e.status === 0 || e.status === 408 || e.status === 429 || e.status >= 500;
    return !(e instanceof DOMException && e.name === "AbortError");
}
async function retry(attempts, signal, fn) {
    for (let i = 1;; i++) {
        try {
            return await fn();
        }
        catch (e) {
            if (signal?.aborted || i >= attempts || !retryable(e))
                throw e;
            await new Promise((r) => setTimeout(r, Math.min(8000, 500 * 2 ** (i - 1)) * (0.75 + Math.random() / 2)));
        }
    }
}
/** One PUT of a blob: XMLHttpRequest in browsers (upload progress), fetch elsewhere. */
function send(method, url, body, headers, signal, onLoaded, f) {
    if (signal?.aborted)
        return Promise.reject(new DOMException("the upload was cancelled", "AbortError"));
    if (typeof XMLHttpRequest === "undefined") {
        return f(url, { method, body, headers, ...(signal ? { signal } : {}) }).then(async (res) => {
            if (!res.ok)
                throw await s3Error(res.status, await res.text());
            onLoaded(body.size);
            return { etag: res.headers.get("etag") ?? "" };
        });
    }
    return new Promise((resolve, reject) => {
        if (signal?.aborted)
            return reject(new DOMException("the upload was cancelled", "AbortError"));
        const xhr = new XMLHttpRequest();
        xhr.open(method, url);
        for (const [k, v] of Object.entries(headers))
            xhr.setRequestHeader(k, v);
        xhr.upload.onprogress = (e) => onLoaded(e.loaded);
        const onAbort = () => xhr.abort();
        signal?.addEventListener("abort", onAbort, { once: true });
        xhr.onload = async () => {
            signal?.removeEventListener("abort", onAbort);
            if (xhr.status >= 200 && xhr.status < 300) {
                onLoaded(body.size);
                resolve({ etag: xhr.getResponseHeader("ETag") ?? "" });
            }
            else
                reject(await s3Error(xhr.status, xhr.responseText));
        };
        xhr.onerror = () => {
            signal?.removeEventListener("abort", onAbort);
            reject(new UploadError(0, "NetworkError", "the connection to storage failed"));
        };
        xhr.onabort = () => reject(new DOMException("the upload was cancelled", "AbortError"));
        xhr.send(body);
    });
}
