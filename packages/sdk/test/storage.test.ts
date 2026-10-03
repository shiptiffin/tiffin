import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { bucket, bucketName, contentKey, encodeKey, isPublic, presign, publicUrl, upload } from "../src/storage";

// A stand-in for the box's S3 endpoint that records what it receives.
const seen: { method: string; path: string; auth: string; type: string; body: string }[] = [];
let server: ReturnType<typeof Bun.serve>;
let env: Record<string, string>;

beforeAll(() => {
  server = Bun.serve({
    port: 0,
    async fetch(req) {
      const u = new URL(req.url);
      seen.push({ method: req.method, path: u.pathname, auth: req.headers.get("authorization") ?? "", type: req.headers.get("content-type") ?? "", body: await req.text() });
      return new Response(req.method === "GET" ? "stored bytes" : "", { headers: { etag: '"abc"' } });
    },
  });
  env = {
    S3_ENDPOINT: `http://127.0.0.1:${server.port}`,
    S3_PUBLIC_ENDPOINT: "https://s3.tiffin.localhost:18443",
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
    expect(out).toEqual({ bucket: "media", key: "avatars/42.png", size: 7 });
    const put = seen.find((s) => s.method === "PUT")!;
    expect(put.path).toBe("/shop-media/avatars/42.png");
    expect(put.auth).toContain("Credential=TFNTESTKEY/");
    expect(put.type).toBe("image/png");
    expect(put.body).toBe("PNGDATA");
    const pub = await upload("assets", "a b.txt", "hi", { env });
    expect(pub.url).toBe("https://files.tiffin.localhost:18443/shop/assets/a%20b.txt");
    expect(await bucket("media", { env }).file("avatars/42.png").text()).toBe("stored bytes");
  });

  test("presigned URLs point at the public endpoint", () => {
    const u = new URL(presign("media", "docs/report.pdf", { env, expiresIn: 600 }));
    expect(u.host).toBe("s3.tiffin.localhost:18443");
    expect(u.pathname).toBe("/shop-media/docs/report.pdf");
    expect(u.searchParams.get("X-Amz-Expires")).toBe("600");
    expect(u.searchParams.get("X-Amz-Credential")).toStartWith("TFNTESTKEY/");
    expect(u.searchParams.get("X-Amz-Signature")).toMatch(/^[0-9a-f]{64}$/);
    const put = new URL(presign("media", "up.bin", { env, method: "PUT" }));
    expect(put.searchParams.get("X-Amz-Signature")).not.toBe(u.searchParams.get("X-Amz-Signature"));
  });

  test("public URLs only for public buckets", () => {
    expect(publicUrl("assets", "img/ü 1.png", { env })).toBe("https://files.tiffin.localhost:18443/shop/assets/img/%C3%BC%201.png");
    expect(() => publicUrl("media", "x", { env })).toThrow(/private/);
    expect(encodeKey("a/b c/d")).toBe("a/b%20c/d");
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
