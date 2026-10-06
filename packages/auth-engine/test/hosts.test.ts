// Apps with several hosts (a box subdomain and custom domains), and the
// signed session cookie cache apps verify on their own.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createLocalJWKSet, decodeProtectedHeader, jwtVerify } from "jose";
import { rpIDFor } from "../src/auth";
import { adminHandler } from "../src/admin";
import { Registry } from "../src/registry";
import { engineRequest, publicHandler } from "../src/server";
import { Client, HOST, ORIGIN, person, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";
import { SoftPasskey } from "./webauthn";

const CUSTOM = "shop.example.com";
const WWW = "www.shop.example.com";

let reg: Registry;
let handle: (r: Request) => Promise<Response>;

beforeAll(async () => {
  const db = await freshDatabase("hosts_test");
  const hosts = [HOST, CUSTOM, WWW];
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(db, { hosts, origins: [ORIGIN, `https://${CUSTOM}`, `https://${WWW}`] }) } });
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
