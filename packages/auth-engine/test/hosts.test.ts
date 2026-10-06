// Apps with several hosts (a box subdomain and custom domains), and the
// signed session cookie cache apps verify on their own.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createLocalJWKSet, decodeProtectedHeader, jwtVerify } from "jose";
import { rpIDFor } from "../src/auth";
import { adminHandler } from "../src/admin";
import { Registry } from "../src/registry";
import { engineRequest, publicHandler } from "../src/server";
import { Client, HOST, linkIn, lastMail, ORIGIN, person, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";
import { SoftPasskey } from "./webauthn";

const CUSTOM = "shop.example.com";
const WWW = "www.shop.example.com";
// A preview: the Go module adds its host after the production ones.
const PREVIEW = "pr-7--shop.tiffin.localhost";

let reg: Registry;
let handle: (r: Request) => Promise<Response>;

beforeAll(async () => {
  const db = await freshDatabase("hosts_test");
  const hosts = [HOST, CUSTOM, WWW, PREVIEW];
  const origins = [ORIGIN, `https://${CUSTOM}`, `https://${WWW}`, `https://${PREVIEW}:8443`];
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(db, { hosts, origins }) } });
  handle = publicHandler(reg);
  const r = await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
  expect(r.status).toBe(200);
}, 60_000);

afterAll(async () => {
  await reg.closeAll();
  await stopCluster();
});

test("rpID: the request's host, or the shortest app host above it", () => {
  const hosts = ["shop.box.test", "example.com", "app.example.com"];
  expect(rpIDFor(hosts, "shop.box.test:8443")).toBe("shop.box.test");
  expect(rpIDFor(hosts, "app.example.com")).toBe("example.com");
  expect(rpIDFor(hosts, "WWW.Example.com")).toBe("example.com");
  expect(rpIDFor(hosts, "elsewhere.test")).toBe("shop.box.test");
  expect(rpIDFor(hosts, undefined)).toBe("shop.box.test");
});

describe("passkeys on every host", () => {
  test("register and sign in on a custom domain; the passkey is bound to it", async () => {
    const pat = await person(handle, "pat@example.com", "Pat");
    const at = (host: string, path: string) => `https://${host}/api/auth${path}`;

    const opts = await pat.json(at(CUSTOM, "/passkey/generate-register-options"));
    expect(opts.status).toBe(200);
    expect(opts.body.rp.id).toBe(CUSTOM);
    const key = await SoftPasskey.create();
    const reg1 = await pat.json(at(CUSTOM, "/passkey/verify-registration"), { body: await key.register(opts.body, `https://${CUSTOM}`) });
    expect(reg1.status).toBe(200);

    // www. shares the parent's rpID, so the same passkey signs in there.
    const fresh = new Client(handle);
    const a = await fresh.json(at(WWW, "/passkey/generate-authenticate-options"));
    expect(a.body.rpId).toBe(CUSTOM);
    const signedIn = await fresh.json(at(WWW, "/passkey/verify-authentication"), { body: await key.authenticate(a.body, CUSTOM, `https://${WWW}`) });
    expect(signedIn.status).toBe(200);
    expect((await fresh.json("/tiffin/session")).body.user.email).toBe("pat@example.com");

    // The box subdomain is another rpID: the browser wouldn't offer it there,
    // and a response made for it anyway is refused.
    const other = new Client(handle);
    const b = await other.json("/passkey/generate-authenticate-options");
    expect(b.body.rpId).toBe(HOST);
    const wrong = await other.json("/passkey/verify-authentication", { body: await key.authenticate(b.body, CUSTOM, ORIGIN) });
    expect(wrong.status).toBe(400);
  });

  test("an internal call naming its host gets that host's rpID", async () => {
    const res = await handle(new Request("http://127.0.0.1:7393/api/auth/passkey/generate-authenticate-options", { headers: { host: "127.0.0.1:7393", "x-tiffin-host": WWW } }));
    expect(((await res.json()) as { rpId: string }).rpId).toBe(CUSTOM);
  });
});

describe("previews", () => {
  const at = (path: string) => `https://${PREVIEW}:8443/api/auth${path}`;

  test("sign up and in on a preview: host-only cookies, links back to it, the project's users", async () => {
    const tess = new Client(handle);
    const up = await tess.json(at("/sign-up/email"), {
      body: { email: "tess@example.com", password: "correct horse battery", name: "Tess" },
      headers: { "x-captcha-response": await tess.captcha() },
    });
    expect(up.status).toBe(200);
    const link = linkIn(lastMail("tess@example.com", "verify").text);
    expect(new URL(link).host).toBe(`${PREVIEW}:8443`);
    const verified = await tess.raw(link);
    expect(verified.status).toBeLessThan(400);
    const set = verified.headers.getSetCookie();
    expect(set.some((c) => c.startsWith("__Secure-tiffin.session_token="))).toBe(true);
    // No Domain attribute: the browser keeps them for the preview's host alone,
    // and production's cookies never reach the preview.
    for (const c of set) expect(c).not.toMatch(/;\s*domain=/i);
    expect((await tess.json(at("/tiffin/session"))).body.user.email).toBe("tess@example.com");

    // The same account signs in on production, and a production user on the preview.
    const prod = new Client(handle);
    expect((await prod.withCaptcha("/sign-in/email", { email: "tess@example.com", password: "correct horse battery" })).status).toBe(200);
    const uma = await person(handle, "uma@example.com", "Uma");
    expect((await uma.json("/tiffin/session")).body.user.email).toBe("uma@example.com");
    const onPreview = new Client(handle);
    const signIn = await onPreview.json(at("/sign-in/email"), {
      body: { email: "uma@example.com", password: "correct horse battery" },
      headers: { "x-captcha-response": await onPreview.captcha() },
    });
    expect(signIn.status).toBe(200);
    expect((await onPreview.json(at("/tiffin/session"))).body.user.email).toBe("uma@example.com");
  });

  test("passkeys on a preview are bound to the preview's host", async () => {
    const vic = await person(handle, "vic@example.com", "Vic");
    const opts = await vic.json(at("/passkey/generate-register-options"));
    expect(opts.status).toBe(200);
    expect(opts.body.rp.id).toBe(PREVIEW);
    const key = await SoftPasskey.create();
    expect((await vic.json(at("/passkey/verify-registration"), { body: await key.register(opts.body, `https://${PREVIEW}:8443`) })).status).toBe(200);
    const fresh = new Client(handle);
    const a = await fresh.json(at("/passkey/generate-authenticate-options"));
    expect(a.body.rpId).toBe(PREVIEW);
    expect((await fresh.json(at("/passkey/verify-authentication"), { body: await key.authenticate(a.body, PREVIEW, `https://${PREVIEW}:8443`) })).status).toBe(200);
    expect((await fresh.json(at("/tiffin/session"))).body.user.email).toBe("vic@example.com");
  });

  test("a host the config doesn't list (a deleted preview) is refused", async () => {
    const r = await handle(new Request("https://pr-8--shop.tiffin.localhost/api/auth/tiffin/config", { headers: { host: "pr-8--shop.tiffin.localhost" } }));
    expect(r.status).toBe(404);
  });
});

test("rate limits leave session reads alone, not sign-ins", async () => {
  const r2 = new Registry(null, { version: 1, listen: [], projects: { shop: { ...reg.projectConfig("shop")!, rateLimit: true } } });
  try {
    const h = publicHandler(r2);
    const ip = { "x-forwarded-for": "198.51.100.7" };
    const c = new Client(h, ip);
    for (let i = 0; i < 150; i++) expect((await c.raw("/tiffin/session")).status).toBe(200);
    const statuses = new Set<number>();
    for (let i = 0; i < 6; i++) statuses.add((await new Client(h, ip).json("/sign-in/email", { body: { email: "x@example.com", password: "nope-nope" } })).status);
    expect(statuses.has(429)).toBe(true);
  } finally {
    await r2.closeAll();
  }
}, 60_000);

describe("session cookie cache", () => {
  test("is a JWT signed with the project's key, carrying the active org and role", async () => {
    const quinn = await person(handle, "quinn@example.com", "Quinn");
    const jwt = quinn.cookies.get("__Secure-tiffin.session_data");
    expect(jwt).toBeDefined();
    expect(decodeProtectedHeader(jwt!).typ).toBe("better-auth.session-cache+jwt");
    const jwks = await new Client(handle).json("/jwks");
    const { payload } = await jwtVerify(jwt!, createLocalJWKSet(jwks.body), { audience: "better-auth:session-cache" });
    const token = decodeURIComponent(quinn.cookies.get("__Secure-tiffin.session_token")!).split(".")[0];
    expect(payload.sid).toBe(token);
    expect((payload.user as { email: string }).email).toBe("quinn@example.com");
    expect(payload.exp! - payload.iat!).toBe(60);
    expect((payload.tiffin as any).organization).toMatchObject({ name: "Personal", role: "owner", memberRole: "owner" });

    // Switching organizations re-signs it with the new one.
    const team = await quinn.json("/organization/create", { body: { name: "Crew", slug: "crew" } });
    await quinn.json("/organization/set-active", { body: { organizationId: team.body.id } });
    const after = await jwtVerify(quinn.cookies.get("__Secure-tiffin.session_data")!, createLocalJWKSet(jwks.body), { audience: "better-auth:session-cache" });
    expect((after.payload.tiffin as any).organization).toMatchObject({ id: team.body.id, name: "Crew", role: "owner" });
  });

  test("the engine never trusts it: a signed-out session is gone at once", async () => {
    const ray = await person(handle, "ray@example.com", "Ray");
    const copy = new Client(handle);
    copy.cookies = new Map(ray.cookies);
    expect((await copy.json("/tiffin/session")).body.user.email).toBe("ray@example.com");
    await ray.json("/sign-out", { body: {} });
    expect(ray.cookies.has("__Secure-tiffin.session_data")).toBe(false);
    expect((await copy.json("/tiffin/session")).body).toBeNull();
    expect((await copy.json("/get-session")).body).toBeNull();
  });

  test("engineRequest strips the cache cookie and serves internal calls as their host", async () => {
    const req = new Request("http://127.0.0.1:7393/api/auth/sign-in/email?x=1", {
      method: "POST",
      headers: { host: "127.0.0.1:7393", cookie: "a=1; __Secure-tiffin.session_data=x; tiffin.session_data.0=y; __Secure-tiffin.session_token=t" },
      body: "{}",
    });
    const out = engineRequest(req, `${CUSTOM}:8443`, ORIGIN);
    expect(out.url).toBe(`https://${CUSTOM}:8443/api/auth/sign-in/email?x=1`);
    expect(out.headers.get("host")).toBe(`${CUSTOM}:8443`);
    expect(out.headers.get("cookie")).toBe("a=1; __Secure-tiffin.session_token=t");
    expect(await out.text()).toBe("{}");
    const plain = new Request("https://x/api/auth/ok", { headers: { cookie: "a=1" } });
    expect(engineRequest(plain, null, ORIGIN)).toBe(plain);
  });
});
