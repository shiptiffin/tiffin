// Answers from sign-in providers are bounded in size and time, so one
// project's OpenID Connect issuer can't exhaust the engine every project
// shares.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { adminHandler } from "../src/admin";
import { boundedFetch } from "../src/fetch";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

const KB = 1024;

// A provider that answers too much: with a Content-Length, streamed without one, or slowly.
const provider = Bun.serve({
  port: 0,
  fetch(req) {
    const path = new URL(req.url).pathname;
    if (path === "/small") return Response.json({ ok: true }, { headers: { "x-kept": "yes" } });
    if (path === "/declared") return new Response("x".repeat(80 * KB));
    if (path.endsWith("/.well-known/openid-configuration") || path === "/streamed") {
      // Endless: 16 KB chunks until the reader gives up (a tick apart, so this
      // in-process server doesn't starve the client reading it).
      const chunk = new TextEncoder().encode(`{"pad":"${"x".repeat(16 * KB)}`);
      const pull = async (c: ReadableStreamDefaultController<Uint8Array>) => {
        await Bun.sleep(1);
        c.enqueue(chunk);
      };
      return new Response(new ReadableStream({ pull }), { headers: { "content-type": "application/json" } });
    }
    if (path === "/slow") {
      return new Response(
        new ReadableStream({
          async start(c) {
            c.enqueue(new TextEncoder().encode("{"));
            await Bun.sleep(2_000);
            c.close();
          },
        }),
      );
    }
    return new Response("not found", { status: 404 });
  },
});
const base = `http://127.0.0.1:${provider.port}`;
const realFetch = globalThis.fetch;

describe("boundedFetch", () => {
  const f = boundedFetch(realFetch, 64 * KB, 500);

  test("a small answer comes back whole, with its status and headers", async () => {
    const r = await f(`${base}/small`);
    expect(r.status).toBe(200);
    expect(r.headers.get("x-kept")).toBe("yes");
    expect(await r.json()).toEqual({ ok: true });
    expect((await f(`${base}/nope`)).status).toBe(404);
  });

  test("too big: refused by its Content-Length, or while it streams", async () => {
    await expect(f(`${base}/declared`)).rejects.toThrow(/over 65536 bytes/);
    await expect(f(`${base}/streamed`)).rejects.toThrow(/over 65536 bytes/);
  });

  test("the deadline covers reading the body", async () => {
    const t0 = Date.now();
    await expect(f(`${base}/slow`)).rejects.toThrow();
    expect(Date.now() - t0).toBeLessThan(1_500);
  });
});

describe("a project's own OpenID Connect issuer", () => {
  let reg: Registry;
  let handle: (r: Request) => Promise<Response>;

  beforeAll(async () => {
    globalThis.fetch = boundedFetch(realFetch, 64 * KB, 2_000);
    const url = await freshDatabase("fetch_test");
    const big = projectConfig(url, {
      hosts: ["big.tiffin.localhost"],
      primaryUrl: "https://big.tiffin.localhost:8443",
      origins: ["https://big.tiffin.localhost:8443"],
      methods: ["email", "oidc"],
      captcha: false,
      social: { oidc: { clientId: "big", clientSecret: "big-secret", issuer: base, proxied: false } },
    });
    reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(url, { captcha: false }), big } });
    handle = publicHandler(reg);
    expect((await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }))).status).toBe(200);
  }, 90_000);

  afterAll(async () => {
    globalThis.fetch = realFetch;
    provider.stop(true);
    await reg.closeAll();
    await stopCluster();
  });

  test("an endless discovery document skips that provider; the engine and other projects carry on", async () => {
    const b = new Client(handle);
    const r = await b.json("https://big.tiffin.localhost:8443/api/auth/sign-in/social", { body: { provider: "oidc", callbackURL: "/" } });
    expect(r.status).toBeGreaterThanOrEqual(400);
    expect(r.status).toBeLessThan(500);
    const shop = await new Client(handle).json("/tiffin/config");
    expect(shop.status).toBe(200);
    expect(shop.body.appName).toBe("Shop");
  });
});
