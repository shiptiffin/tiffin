import { afterAll, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import imageLoader from "../src/next/image-loader";
import { presignUrl } from "../src/s3sign";
import {
  bucket,
  bucketName,
  contentKey,
  createUpload,
  encodeKey,
  isPublic,
  onUploadCompleted,
  presign,
  publicUrl,
  signedUrl,
  upload,
  uploadRoute,
  type ObjectCreated,
} from "../src/storage";
import { abortUpload, uploadFile, UploadError, type UploadProgress } from "../src/client/upload";

// A stand-in for the box's S3 endpoint (and the gateway's multipart calls)
// that records what it receives.
type Seen = { method: string; path: string; query: URLSearchParams; auth: string; type: string; body: string };
const seen: Seen[] = [];
const stored = new Map<number, number>(); // multipart: part number → size
let failNext = 0; // the next n part uploads answer 503
let server: ReturnType<typeof Bun.serve>;
let env: Record<string, string>;

beforeAll(() => {
  server = Bun.serve({
    port: 0,
    async fetch(req) {
      const u = new URL(req.url);
      const body = await req.text();
      seen.push({ method: req.method, path: u.pathname, query: u.searchParams, auth: req.headers.get("authorization") ?? "", type: req.headers.get("content-type") ?? "", body });
      if (req.method === "POST" && u.searchParams.has("uploads")) {
        return new Response("<InitiateMultipartUploadResult><UploadId>UP-1</UploadId></InitiateMultipartUploadResult>");
      }
      if (req.method === "PUT" && u.searchParams.has("partNumber")) {
        if (failNext > 0) {
          failNext--;
          return new Response("<Error><Code>ServiceUnavailable</Code></Error>", { status: 503 });
        }
        const n = Number(u.searchParams.get("partNumber"));
        stored.set(n, body.length);
        return new Response("", { headers: { etag: `"part-${n}"` } });
      }
      if (req.method === "GET" && u.searchParams.has("uploadId")) {
        const parts = [...stored].map(([n, size]) => `<Part><PartNumber>${n}</PartNumber><ETag>&quot;part-${n}&quot;</ETag><Size>${size}</Size></Part>`);
        return new Response(`<ListPartsResult>${parts.join("")}</ListPartsResult>`);
      }
      if (req.method === "POST" && u.searchParams.has("uploadId")) {
        return new Response("<CompleteMultipartUploadResult><ETag>&quot;whole-3&quot;</ETag></CompleteMultipartUploadResult>");
      }
      if (req.method === "PUT" && body === "too big") return new Response("<Error><Code>EntityTooLarge</Code><Message>no</Message></Error>", { status: 413 });
      return new Response(req.method === "GET" ? "stored bytes" : "", { headers: { etag: '"abc"' } });
    },
  });
  env = {
    S3_ENDPOINT: `http://127.0.0.1:${server.port}`,
    S3_PUBLIC_ENDPOINT: `http://127.0.0.1:${server.port}`,
    S3_REGION: "us-east-1",
    S3_ACCESS_KEY_ID: "TFNTESTKEY",
    S3_SECRET_ACCESS_KEY: "secret",
    S3_BUCKET_MEDIA: "shop-media",
    S3_BUCKET_USER_UPLOADS: "shop-user-uploads",
    S3_BUCKET_ASSETS: "shop-assets",
    TIFFIN_PUBLIC_BUCKETS: "assets",
    TIFFIN_FILES_URL: "https://files.tiffin.localhost:18443/shop",
  };
});
afterAll(() => server.stop(true));
beforeEach(() => {
  seen.length = 0;
  stored.clear();
});

describe("storage", () => {
  test("bucket names come from the env", () => {
    expect(bucketName("media", { env })).toBe("shop-media");
    expect(bucketName("user-uploads", { env })).toBe("shop-user-uploads");
    expect(() => bucketName("nope", { env })).toThrow(/no bucket "nope".*media/);
    expect(isPublic("assets", { env })).toBe(true);
    expect(isPublic("media", { env })).toBe(false);
  });

  test("upload signs with the project key against the internal endpoint", async () => {
    const out = await upload("media", "avatars/42.png", "PNGDATA", { env, contentType: "image/png" });
    expect(out).toEqual({ bucket: "media", key: "avatars/42.png", size: 7, etag: "abc" });
    const put = seen.find((s) => s.method === "PUT")!;
    expect(put.path).toBe("/shop-media/avatars/42.png");
    expect(put.auth).toContain("Credential=TFNTESTKEY/");
    expect(put.auth).toContain("SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date");
    expect(put.type).toBe("image/png");
    expect(put.body).toBe("PNGDATA");
    const pub = await upload("assets", "a b.txt", "hi", { env });
    expect(pub.url).toBe("https://files.tiffin.localhost:18443/shop/assets/a%20b.txt");
    expect(seen.at(-1)!.type).toBe("text/plain; charset=utf-8"); // guessed from the key
    expect(await bucket("media", { env }).file("avatars/42.png").text()).toBe("stored bytes");
    expect(upload("media", "x", "too big", { env })).rejects.toThrow(/EntityTooLarge/);
  });

  test("presigned URLs point at the public endpoint and match the box's signer", () => {
    const pubEnv = { ...env, S3_PUBLIC_ENDPOINT: "https://s3.tiffin.localhost:18443" };
    const u = new URL(presign("media", "docs/report.pdf", { env: pubEnv, expiresIn: 600 }));
    expect(u.host).toBe("s3.tiffin.localhost:18443");
    expect(u.pathname).toBe("/shop-media/docs/report.pdf");
    expect(u.searchParams.get("X-Amz-Expires")).toBe("600");
    expect(u.searchParams.get("X-Amz-Credential")).toStartWith("TFNTESTKEY/");
    expect(u.searchParams.get("X-Amz-Signature")).toMatch(/^[0-9a-f]{64}$/);
    const put = new URL(presign("media", "up.bin", { env: pubEnv, method: "PUT", contentType: "image/png", maxSize: 1000 }));
    expect(put.searchParams.get("X-Amz-SignedHeaders")).toBe("content-type;host");
    expect(put.searchParams.get("x-tiffin-max-size")).toBe("1000");
    // The AWS documentation's example (GET /test.txt on examplebucket).
    const aws = presignUrl({ method: "GET", endpoint: "https://examplebucket.s3.amazonaws.com", bucket: "test.txt", key: "",
      creds: { accessKeyId: "AKIAIOSFODNN7EXAMPLE", secretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", region: "us-east-1" },
      expiresIn: 86400, now: new Date("2013-05-24T00:00:00Z") });
    expect(aws).toContain("X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404");
    // Same URL as internal/mod/storage PresignWith for the same input.
    const box = presignUrl({ method: "PUT", endpoint: "https://s3.tiffin.localhost:18443", bucket: "shop-media", key: "up/ü a+b.png",
      creds: { accessKeyId: "TFNTESTKEY", secretAccessKey: "secret", region: "us-east-1" }, expiresIn: 600, now: new Date("2013-05-24T00:00:00Z"),
      query: { partNumber: "2", uploadId: "abc-123", "x-tiffin-max-size": "1000" }, headers: { "content-type": "image/png", "content-length": "500" } });
    expect(box).toBe(
      "https://s3.tiffin.localhost:18443/shop-media/up/%C3%BC%20a%2Bb.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=TFNTESTKEY%2F20130524%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20130524T000000Z&X-Amz-Expires=600&X-Amz-Signature=f63432929d2c2cd09bd366b63c556dd7b4c6025626c553f5b0e55c31e91f595d&X-Amz-SignedHeaders=content-length%3Bcontent-type%3Bhost&partNumber=2&uploadId=abc-123&x-tiffin-max-size=1000",
    );
  });

  test("public URLs only for public buckets, with image transforms", () => {
    expect(publicUrl("assets", "img/ü 1.png", { env })).toBe("https://files.tiffin.localhost:18443/shop/assets/img/%C3%BC%201.png");
    expect(publicUrl("assets", "hero.jpg", { env, width: 1000 })).toBe("https://files.tiffin.localhost:18443/shop/assets/hero.jpg?w=1080&f=webp");
    expect(publicUrl("assets", "hero.jpg", { env, width: 5000, quality: 80, format: "avif" })).toBe(
      "https://files.tiffin.localhost:18443/shop/assets/hero.jpg?w=3840&q=75&f=avif",
    );
    expect(() => publicUrl("media", "x", { env })).toThrow(/private/);
    expect(encodeKey("a/b c/d")).toBe("a/b%20c/d");
  });

  test("signed file URLs match the box's signature", () => {
    const u = new URL(signedUrl("media", "a/b c.png", { env, expiresIn: 60, width: 256 }));
    expect(u.pathname).toBe("/shop/media/a/b%20c.png");
    expect(u.searchParams.get("w")).toBe("256");
    const exp = Number(u.searchParams.get("exp"));
    expect(exp - Date.now() / 1000).toBeGreaterThan(55);
    // FilesSignature("secret", "shop", "media", "a/b c.png", 1700000000) in Go:
    const realNow = Date.now;
    Date.now = () => 1700000000_000 - 60_000;
    try {
      expect(new URL(signedUrl("media", "a/b c.png", { env, expiresIn: 60 })).searchParams.get("sig")).toBe("Ggrzj6yhLmDtFlvZPSjCs3FCPiNOqRQ_uPKsjjhpvAc");
    } finally {
      Date.now = realNow;
    }
  });

  test("content keys get the immutable cache", async () => {
    const k = await contentKey("img/logo.png", "same bytes");
    expect(k).toMatch(/^img\/logo\.[0-9a-f]{16}\.png$/);
    expect(await contentKey("img/logo.png", new TextEncoder().encode("same bytes"))).toBe(k);
    expect(await contentKey("noext", "x")).toMatch(/^noext\.[0-9a-f]{16}$/);
    // The box's rule (internal/mod/storage CacheControl) for year-long caching.
    expect(/(^|[./_-])[0-9a-fA-F]{8,}([./_-]|$)/.test(k)).toBe(true);
  });

  test("missing settings say what to do", () => {
    expect(() => bucket("media", { env: { S3_BUCKET_MEDIA: "shop-media" } })).toThrow(/S3_ACCESS_KEY_ID is not set/);
  });
});

describe("browser uploads", () => {
  test("a small file gets one PUT URL with its type, size and cap signed in", async () => {
    const t = await createUpload({ env, bucket: "assets", key: "u/a.png", contentType: "image/png", size: 5, maxSize: 100 });
    const u = new URL(t.url!);
    expect(u.pathname).toBe("/shop-assets/u/a.png");
    expect(u.searchParams.get("X-Amz-SignedHeaders")).toBe("content-length;content-type;host");
    expect(u.searchParams.get("x-tiffin-max-size")).toBe("100");
    expect(t.headers).toEqual({ "Content-Type": "image/png" });
    expect(t.publicUrl).toBe("https://files.tiffin.localhost:18443/shop/assets/u/a.png");
    expect(t.multipart).toBeUndefined();
    const progress: UploadProgress[] = [];
    const done = await uploadFile(new Blob(["12345"], { type: "image/png" }), t, { onProgress: (p) => progress.push(p) });
    expect(done).toEqual({ bucket: "assets", key: "u/a.png", size: 5, etag: "abc", url: t.publicUrl! });
    expect(progress.at(-1)!.percent).toBe(100);
    expect(seen.at(-1)!.type).toBe("image/png");
    await expect(uploadFile(new Blob(["123456"]), t)).rejects.toThrow(/ticket is for 5 bytes/);
    await expect(createUpload({ env, bucket: "media", key: "x", size: 200, maxSize: 100 })).rejects.toThrow(/EntityTooLarge/);
  });

  test("a large file goes up in parts, retried and resumable", async () => {
    const MiB = 1 << 20;
    const size = 20 * MiB + 3;
    const t = await createUpload({ env, bucket: "media", key: "v/movie.mp4", contentType: "video/mp4", size, multipartThreshold: 8 * MiB, partSize: 8 * MiB });
    const create = seen.find((s) => s.query.has("uploads"))!;
    expect(create.auth).toContain("Credential=TFNTESTKEY/");
    expect(create.type).toBe("video/mp4");
    const mp = t.multipart!;
    expect(mp.uploadId).toBe("UP-1");
    expect(mp.parts.length).toBe(3);
    expect(new URL(mp.parts[2]!).searchParams.get("partNumber")).toBe("3");
    expect(new URL(mp.parts[0]!).searchParams.get("X-Amz-SignedHeaders")).toBe("content-length;host");
    expect(new URL(mp.complete).searchParams.get("x-tiffin-max-size")).toBe(String(size));
    const file = new Blob([new Uint8Array(size)], { type: "video/mp4" });

    // First try: cancelled after the first part lands.
    const ctrl = new AbortController();
    const first = uploadFile(file, t, { concurrency: 1, onProgress: (p) => p.loaded >= 8 * MiB && ctrl.abort(), signal: ctrl.signal });
    await expect(first).rejects.toThrow();
    expect(stored.size).toBeGreaterThanOrEqual(1);
    const before = seen.filter((s) => s.method === "PUT").length;

    // Second try resumes: listed parts are skipped; a 503 is retried.
    failNext = 1;
    const progress: number[] = [];
    const done = await uploadFile(file, t, { onProgress: (p) => progress.push(p.percent) });
    expect(done.etag).toBe("whole-3");
    expect(done.size).toBe(size);
    const puts = seen.filter((s) => s.method === "PUT").length - before;
    expect(puts).toBe(3 - 1 + 1); // two parts left, one of them twice
    const complete = seen.find((s) => s.method === "POST" && s.query.has("uploadId"))!;
    expect(complete.body).toBe(
      '<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"part-1"</ETag></Part><Part><PartNumber>2</PartNumber><ETag>"part-2"</ETag></Part><Part><PartNumber>3</PartNumber><ETag>"part-3"</ETag></Part></CompleteMultipartUpload>',
    );
    expect(progress.at(-1)).toBe(100);
    for (let i = 1; i < progress.length; i++) expect(progress[i]!).toBeGreaterThanOrEqual(progress[i - 1]!);
    await abortUpload(t);
    expect(seen.at(-1)!.method).toBe("DELETE");
  });

  test("an upload from a route pauses and resumes with the ticket from onTicket", async () => {
    const MiB = 1 << 20;
    const size = 16 * MiB;
    const t = await createUpload({ env, bucket: "media", key: "v/clip.mp4", size, multipartThreshold: 8 * MiB, partSize: 8 * MiB });
    const f = ((input: string | URL | Request, init?: RequestInit) => (String(input) === "/api/upload" ? Promise.resolve(Response.json(t)) : fetch(input, init))) as typeof fetch;
    const file = new Blob([new Uint8Array(size)]);
    let kept: typeof t | undefined;
    const pause = new AbortController();
    const first = uploadFile(file, "/api/upload", { fetch: f, concurrency: 1, onTicket: (k) => (kept = k), onProgress: (p) => p.loaded >= 8 * MiB && pause.abort(), signal: pause.signal });
    await expect(first).rejects.toThrow();
    expect(kept?.multipart?.uploadId).toBe("UP-1");
    const before = seen.filter((s) => s.method === "PUT").length;
    const done = await uploadFile(file, kept!, { fetch: f });
    expect(done.size).toBe(size);
    expect(seen.filter((s) => s.method === "PUT").length - before).toBe(1); // only the part left
  });

  test("a refusal is not retried", async () => {
    const t = await createUpload({ env, bucket: "media", key: "x.bin" });
    const err = await uploadFile(new Blob(["too big"]), t).catch((e) => e);
    expect(err).toBeInstanceOf(UploadError);
    expect(err.code).toBe("EntityTooLarge");
    expect(seen.filter((s) => s.method === "PUT").length).toBe(1);
  });

  test("uploadRoute checks the file and answers with a ticket", async () => {
    const route = uploadRoute({ env, bucket: "media", maxSize: 1000, allowedTypes: ["image/*"], authorize: (f) => f.name !== "nope.png" });
    const ask = (b: unknown) => route(new Request("http://app/api/upload", { method: "POST", body: JSON.stringify(b) }));
    const ok = await ask({ name: "../cat.png", size: 10, type: "image/png" });
    const t = await ok.json();
    expect(ok.status).toBe(200);
    expect(t.key).toMatch(/^uploads\/[0-9a-f-]{36}\/cat\.png$/);
    expect(t.contentType).toBe("image/png");
    expect((await ask({ name: "a.png", size: 2000, type: "image/png" })).status).toBe(413);
    expect((await ask({ name: "a.txt", size: 10, type: "text/plain" })).status).toBe(415);
    expect((await ask({ name: "nope.png", size: 10, type: "image/png" })).status).toBe(403);
    expect((await ask({ size: "x" })).status).toBe(400);
    // The browser side asks the route itself.
    const f = ((input: string | URL | Request, init?: RequestInit) =>
      String(input) === "/api/upload" ? route(new Request("http://app/api/upload", init)) : fetch(input, init)) as typeof fetch;
    const file = new File(["12345"], "dog.png", { type: "image/png" });
    const done = await uploadFile(file, "/api/upload", { fetch: f });
    expect(done.key).toEndWith("/dog.png");
    await expect(uploadFile(new File(["x"], "a.txt", { type: "text/plain" }), "/api/upload", { fetch: f })).rejects.toThrow(/not accepted/);
  });

  test("onUploadCompleted hands the event to the function", async () => {
    const got: ObjectCreated[] = [];
    const h = onUploadCompleted((e) => void got.push(e), { secret: "s" });
    const res = await h(new Request("http://app/queues/uploads", { method: "POST", body: "{}" }));
    expect(res.status).toBe(401); // unsigned deliveries are refused
    expect(got.length).toBe(0);
  });
});

describe("next/image loader", () => {
  test("box files are resized; anything else is left alone", () => {
    expect(imageLoader({ src: "https://files.example.com/shop/assets/a.png", width: 700 })).toBe("https://files.example.com/shop/assets/a.png?w=750&q=75&f=webp");
    expect(imageLoader({ src: "https://files.example.com/shop/media/a.png?exp=1&sig=x", width: 16, quality: 95 })).toBe(
      "https://files.example.com/shop/media/a.png?exp=1&sig=x&w=16&q=90&f=webp",
    );
    expect(imageLoader({ src: "https://cdn.example.com/a.png", width: 640 })).toBe("https://cdn.example.com/a.png");
    expect(imageLoader({ src: "/logo.png", width: 640 })).toBe("/logo.png");
  });
});
