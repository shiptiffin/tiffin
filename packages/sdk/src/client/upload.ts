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
export class UploadError extends Error {
  override name = "UploadError";
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

/**
 * Uploads a file (any Blob) with a ticket, or asks an upload route
 * (uploadRoute() in @shiptiffin/sdk/storage) for one first.
 */
export async function uploadFile(file: Blob, target: UploadTicket | string, o: UploadFileOptions = {}): Promise<UploadResult> {
  const f = o.fetch ?? fetch;
  const ticket = typeof target === "string" ? await requestTicket(f, target, file, o) : target;
  o.onTicket?.(ticket);
  const total = file.size;
  if (ticket.size !== undefined && ticket.size !== total) {
    throw new UploadError(0, "SizeMismatch", `the ticket is for ${ticket.size} bytes; this file has ${total}`);
  }
  const report = (loaded: number) => o.onProgress?.({ loaded, total, percent: total ? Math.min(100, (loaded / total) * 100) : 100 });
  const attempts = o.attempts ?? 4;
  report(0);
  let etag: string;
  if (ticket.multipart) {
    etag = await uploadParts(file, ticket.multipart, o, attempts, report);
  } else if (ticket.url) {
    const res = await retry(attempts, o.signal, () => send("PUT", ticket.url!, file, ticket.headers ?? {}, o.signal, report, f));
    etag = res.etag;
  } else {
    throw new UploadError(0, "BadTicket", "the ticket has neither a url nor multipart parts");
  }
  report(total);
  const out: UploadResult = { bucket: ticket.bucket, key: ticket.key, size: total, etag: etag.replaceAll('"', "") };
  if (ticket.publicUrl) out.url = ticket.publicUrl;
  return out;
}

/** Gives up on a multipart upload and frees its stored parts. */
export async function abortUpload(ticket: UploadTicket, o: { fetch?: typeof fetch } = {}): Promise<void> {
  if (!ticket.multipart) return;
  const res = await (o.fetch ?? fetch)(ticket.multipart.abort, { method: "DELETE" });
  if (!res.ok && res.status !== 404) throw await s3Error(res.status, await res.text());
}

async function requestTicket(f: typeof fetch, route: string, file: Blob, o: UploadFileOptions): Promise<UploadTicket> {
  const name = (file as Blob & { name?: string }).name ?? "file";
  const init: RequestInit = {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ name, size: file.size, type: file.type, payload: o.payload }),
  };
  if (o.signal) init.signal = o.signal;
  const res = await f(route, init);
  const body = (await res.json().catch(() => ({}))) as UploadTicket & { error?: string; code?: string };
  if (!res.ok) throw new UploadError(res.status, body.code ?? "Refused", body.error ?? `the upload route answered ${res.status}`);
  return body;
}

async function uploadParts(
  file: Blob,
  mp: NonNullable<UploadTicket["multipart"]>,
  o: UploadFileOptions,
  attempts: number,
  report: (n: number) => void,
): Promise<string> {
  const f = o.fetch ?? fetch;
  const n = mp.parts.length;
  const sizeOf = (i: number) => Math.min(mp.partSize, file.size - i * mp.partSize);
  const etags: (string | undefined)[] = Array.from({ length: n }, () => undefined);
  // Resume: parts already stored (with the right size) are skipped.
  const listed = await retry(attempts, o.signal, async () => {
    const res = await f(mp.list, o.signal ? { signal: o.signal } : {});
    const text = await res.text();
    if (res.status === 404) throw new UploadError(404, "NoSuchUpload", "this upload was aborted or completed already; ask for a new ticket");
    if (!res.ok) throw await s3Error(res.status, text);
    return text;
  });
  for (const m of listed.matchAll(/<Part>([\s\S]*?)<\/Part>/g)) {
    const num = Number(tag(m[1]!, "PartNumber"));
    if (num >= 1 && num <= n && Number(tag(m[1]!, "Size")) === sizeOf(num - 1)) etags[num - 1] = tag(m[1]!, "ETag");
  }
  const inFlight = new Map<number, number>();
  let doneBytes = etags.reduce((s, e, i) => (e ? s + sizeOf(i) : s), 0);
  const progress = () => report(doneBytes + [...inFlight.values()].reduce((a, b) => a + b, 0));
  const todo = etags.flatMap((e, i) => (e ? [] : [i]));
  const worker = async () => {
    for (let i = todo.shift(); i !== undefined; i = todo.shift()) {
      const idx = i;
      const blob = file.slice(idx * mp.partSize, idx * mp.partSize + sizeOf(idx));
      const res = await retry(attempts, o.signal, () => {
        inFlight.set(idx, 0);
        return send("PUT", mp.parts[idx]!, blob, {}, o.signal, (loaded) => {
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
  const xml =
    "<CompleteMultipartUpload>" +
    etags.map((e, i) => `<Part><PartNumber>${i + 1}</PartNumber><ETag>${escapeXml(e ?? "")}</ETag></Part>`).join("") +
    "</CompleteMultipartUpload>";
  const done = await retry(attempts, o.signal, async () => {
    const res = await f(mp.complete, { method: "POST", body: xml, headers: { "content-type": "application/xml" }, ...(o.signal ? { signal: o.signal } : {}) });
    const text = await res.text();
    // S3 can answer 200 with an error in the body.
    if (!res.ok || text.includes("<Error>")) throw await s3Error(res.ok ? 500 : res.status, text);
    return text;
  });
  return tag(done, "ETag");
}

function tag(xml: string, name: string): string {
  const m = new RegExp(`<${name}>([\\s\\S]*?)</${name}>`).exec(xml);
  return (m?.[1] ?? "").replaceAll("&quot;", '"').replaceAll("&amp;", "&");
}

function escapeXml(s: string): string {
  return s.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");
}

async function s3Error(status: number, body: string): Promise<UploadError> {
  const code = tag(body, "Code") || "HTTP" + status;
  const msg = tag(body, "Message") || body.trim().slice(0, 200) || `HTTP ${status}`;
  return new UploadError(status, code, msg);
}

/** Network errors, timeouts, throttling and 5xx are worth another try. */
function retryable(e: unknown): boolean {
  if (e instanceof UploadError) return e.status === 0 || e.status === 408 || e.status === 429 || e.status >= 500;
  return !(e instanceof DOMException && e.name === "AbortError");
}

async function retry<T>(attempts: number, signal: AbortSignal | undefined, fn: () => Promise<T>): Promise<T> {
  for (let i = 1; ; i++) {
    try {
      return await fn();
    } catch (e) {
      if (signal?.aborted || i >= attempts || !retryable(e)) throw e;
      await new Promise((r) => setTimeout(r, Math.min(8000, 500 * 2 ** (i - 1)) * (0.75 + Math.random() / 2)));
    }
  }
}

/** One PUT of a blob: XMLHttpRequest in browsers (upload progress), fetch elsewhere. */
function send(
  method: string,
  url: string,
  body: Blob,
  headers: Record<string, string>,
  signal: AbortSignal | undefined,
  onLoaded: (n: number) => void,
  f: typeof fetch,
): Promise<{ etag: string }> {
  if (signal?.aborted) return Promise.reject(new DOMException("the upload was cancelled", "AbortError"));
  if (typeof XMLHttpRequest === "undefined") {
    return f(url, { method, body, headers, ...(signal ? { signal } : {}) }).then(async (res) => {
      if (!res.ok) throw await s3Error(res.status, await res.text());
      onLoaded(body.size);
      return { etag: res.headers.get("etag") ?? "" };
    });
  }
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(new DOMException("the upload was cancelled", "AbortError"));
    const xhr = new XMLHttpRequest();
    xhr.open(method, url);
    for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
    xhr.upload.onprogress = (e) => onLoaded(e.loaded);
    const onAbort = () => xhr.abort();
    signal?.addEventListener("abort", onAbort, { once: true });
    xhr.onload = async () => {
      signal?.removeEventListener("abort", onAbort);
      if (xhr.status >= 200 && xhr.status < 300) {
        onLoaded(body.size);
        resolve({ etag: xhr.getResponseHeader("ETag") ?? "" });
      } else reject(await s3Error(xhr.status, xhr.responseText));
    };
    xhr.onerror = () => {
      signal?.removeEventListener("abort", onAbort);
      reject(new UploadError(0, "NetworkError", "the connection to storage failed"));
    };
    xhr.onabort = () => reject(new DOMException("the upload was cancelled", "AbortError"));
    xhr.send(body);
  });
}
