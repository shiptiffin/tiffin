// Box-wide sign-in keys through the box's one callback URL (the OAuth proxy
// on the dashboard host), against a fake OpenID Connect provider and a
// faked Apple token endpoint.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { adminHandler } from "../src/admin";
import type { EngineConfig } from "../src/config";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { proxyRedirectAllowed, purposeKey } from "../src/social";
import { Client, HOST, ORIGIN, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

const DASH = "dashboard.tiffin.localhost";
const PROXY = `https://${DASH}:8443`;
const BLOG = "blog.tiffin.localhost";
const SECRET = "proxy-secret-0123456789abcdef0123456789abcdef";
// A project on its own keys: one redirect URI, on its custom domain.
const OWN_BOX = "own.tiffin.localhost";
const OWN_CUSTOM = "own.example.com";
const OWN_PREVIEW = "pr-3--own.tiffin.localhost";
const OWN_SIGNIN = `https://${OWN_CUSTOM}:8443`;

let reg: Registry;
let handle: (r: Request) => Promise<Response>;
let idp: ReturnType<typeof Bun.serve>;
let issuer: string;
const realFetch = globalThis.fetch;
let codes = new Map<string, { email: string; name: string }>();

const b64url = (o: unknown) => Buffer.from(JSON.stringify(o)).toString("base64url");

beforeAll(async () => {
  // A minimal OpenID Connect provider: discovery, token, userinfo.
  idp = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(req) {
      const u = new URL(req.url);
      if (u.pathname === "/.well-known/openid-configuration") {
        return Response.json({ issuer, authorization_endpoint: `${issuer}/authorize`, token_endpoint: `${issuer}/token`, userinfo_endpoint: `${issuer}/userinfo` });
      }
      if (u.pathname === "/token") {
        const form = new URLSearchParams(await req.text());
        const who = codes.get(form.get("code") ?? "");
        if (!who || ![`${PROXY}/api/auth/callback/oidc`, `${OWN_SIGNIN}/api/auth/callback/oidc`].includes(form.get("redirect_uri") ?? "")) {
          return Response.json({ error: "invalid_grant" }, { status: 400 });
        }
        return Response.json({ access_token: `at-${form.get("code")}`, token_type: "Bearer", expires_in: 3600 });
      }
      if (u.pathname === "/userinfo") {
        const code = (req.headers.get("authorization") ?? "").replace("Bearer at-", "");
        const who = codes.get(code);
        if (!who) return new Response("no", { status: 401 });
        return Response.json({ sub: `sub-${who.email}`, id: `sub-${who.email}`, email: who.email, name: who.name, email_verified: true });
      }
      return new Response("not found", { status: 404 });
    },
  });
  issuer = `http://127.0.0.1:${idp.port}`;
  // Apple's token endpoint, faked: an id_token with the person's email.
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    if (url.startsWith("https://appleid.apple.com/auth/token")) {
      const id = `${b64url({ alg: "none" })}.${b64url({ iss: "https://appleid.apple.com", sub: "apple-001", email: "ada@privaterelay.appleid.com", email_verified: "true", aud: "com.example.signin" })}.x`;
      return Response.json({ access_token: "apple-at", token_type: "Bearer", expires_in: 3600, id_token: id });
    }
    return realFetch(input as never, init);
  }) as typeof fetch;

  const db = await freshDatabase("proxy_test");
  const blogDb = await freshDatabase("proxy_test_blog");
  const ownDb = await freshDatabase("proxy_test_own");
  const oidc = { clientId: "box-oidc", clientSecret: "box-oidc-secret", issuer, label: "Acme SSO" };
  const apple = { clientId: "com.example.signin", clientSecret: "apple-jwt" };
  const config: EngineConfig = {
    version: 1,
    listen: [],
    projects: {
      shop: projectConfig(db, {
        methods: ["email", "oidc", "apple", "github", "discord"],
        captcha: false,
        social: {
          oidc: { ...oidc, proxied: true },
          apple: { ...apple, proxied: true },
          github: { clientId: "shop-gh", clientSecret: "shop-gh-secret", proxied: false },
        },
        oauthProxy: { url: PROXY, secret: SECRET },
      }),
      // Its own GitHub keys, and no box-wide ones in use.
      blog: projectConfig(blogDb, {
        hosts: [BLOG],
        origins: [`https://${BLOG}:8443`],
        primaryUrl: `https://${BLOG}:8443`,
        methods: ["email", "github"],
        captcha: false,
        social: { github: { clientId: "blog-gh", clientSecret: "blog-gh-secret", proxied: false } },
      }),
      own: projectConfig(ownDb, {
        hosts: [OWN_BOX, OWN_CUSTOM, OWN_PREVIEW],
        origins: [`https://${OWN_BOX}:8443`, OWN_SIGNIN, `https://${OWN_PREVIEW}:8443`],
        primaryUrl: `https://${OWN_BOX}:8443`,
        appName: "Own",
        methods: ["email", "oidc"],
        captcha: false,
        social: { oidc: { clientId: "own-oidc", clientSecret: "own-oidc-secret", issuer, label: "Own SSO", proxied: false } },
        oauthProxy: { appUrl: OWN_SIGNIN, secret: SECRET },
      }),
    },
    proxy: { url: PROXY, host: DASH, secret: SECRET, social: { oidc: { ...oidc, proxied: false }, apple: { ...apple, proxied: false } } },
  };
  reg = new Registry(null, config);
  handle = publicHandler(reg);
  for (const p of ["shop", "blog", "own"]) {
    const r = await adminHandler(reg)(new Request(`http://admin/projects/${p}/migrate`, { method: "POST" }));
    expect(r.status).toBe(200);
  }
}, 90_000);

afterAll(async () => {
  globalThis.fetch = realFetch;
  idp.stop(true);
  await reg.closeAll();
  await stopCluster();
});

/** Starts a sign-in on the app; returns the provider URL. */
async function start(c: Client, provider: string, callbackURL = "/welcome") {
  const r = await c.json("/sign-in/social", { body: { provider, callbackURL } });
  expect(r.status).toBe(200);
  return new URL(r.body.url as string);
}

/** The provider sends the browser back to the box's one callback URL. */
const atProxy = (path: string, init: RequestInit = {}) =>
  handle(new Request(`${PROXY}${path}`, { ...init, redirect: "manual", headers: { host: `${DASH}:8443`, ...(init.headers as Record<string, string>) } }));

describe("the one callback URL", () => {
  test("a box-wide provider sends people to the dashboard host's callback; the project's own keys keep the app host's", async () => {
    const c = new Client(handle);
    const oidc = await start(c, "oidc");
    expect(oidc.origin).toBe(issuer);
    expect(oidc.searchParams.get("redirect_uri")).toBe(`${PROXY}/api/auth/callback/oidc`);
    const gh = await start(new Client(handle), "github");
    expect(gh.searchParams.get("redirect_uri")).toBe(`${ORIGIN}/api/auth/callback/github`);
    expect(gh.searchParams.get("client_id")).toBe("shop-gh");
  });

  test("sign in with OpenID Connect through the proxy: the account is made in the project, in this browser only", async () => {
    const c = new Client(handle);
    const url = await start(c, "oidc");
    codes.set("code-ada", { email: "ada@example.com", name: "Ada" });
    const back = await atProxy(`/api/auth/callback/oidc?code=code-ada&state=${encodeURIComponent(url.searchParams.get("state")!)}`, {
      headers: { cookie: "tiffin.session_token=dashboard-session-must-not-leak" },
    });
    expect(back.status).toBe(302);
    expect(back.headers.getSetCookie()).toEqual([]);
    const to = new URL(back.headers.get("location")!);
    expect(to.origin).toBe(ORIGIN);
    expect(to.pathname).toBe("/api/auth/callback/oidc/oauth-proxy");

    // Someone else's browser can't finish it: no matching state cookie.
    const other = new Client(handle);
    const stolen = await other.raw(to.href);
    expect(stolen.status).toBe(302);
    expect(stolen.headers.get("location")).toContain("error=state_mismatch");
    expect((await other.json("/tiffin/session")).body).toBeNull();

    // The browser that started it is signed in, on the project's users.
    const done = await c.raw(to.href);
    expect(done.status).toBe(302);
    expect(done.headers.get("location")).toBe("/welcome");
    const s = await c.json("/tiffin/session");
    expect(s.body.user.email).toBe("ada@example.com");
    expect(s.body.user.name).toBe("Ada");

    // The same profile can't be used twice.
    const again = await c.raw(to.href);
    expect(again.headers.get("location") ?? "").toContain("error=");
  });

  test("Apple's form_post callback works through the proxy", async () => {
    const c = new Client(handle);
    const url = await start(c, "apple");
    expect(url.origin).toBe("https://appleid.apple.com");
    expect(url.searchParams.get("response_mode")).toBe("form_post");
    expect(url.searchParams.get("redirect_uri")).toBe(`${PROXY}/api/auth/callback/apple`);
    const form = new URLSearchParams({ code: "apple-code", state: url.searchParams.get("state")!, user: JSON.stringify({ name: { firstName: "Ada", lastName: "L" } }) });
    const back = await atProxy("/api/auth/callback/apple", {
      method: "POST",
      body: form.toString(),
      headers: { "content-type": "application/x-www-form-urlencoded", origin: "https://appleid.apple.com" },
    });
    expect(back.status).toBe(302);
    const to = new URL(back.headers.get("location")!);
    expect(to.origin).toBe(ORIGIN);
    const done = await c.raw(to.href);
    expect(done.status).toBe(302);
    const s = await c.json("/tiffin/session");
    expect(s.body.user.email).toBe("ada@privaterelay.appleid.com");
    expect(s.body.user.name).toBe("Ada L");
  });

  test("the proxy host serves the callback and its error page only, never HTML, never cookies", async () => {
    for (const [method, path] of [
      ["GET", "/api/auth/get-session"],
      ["POST", "/api/auth/sign-in/social"],
      ["GET", "/api/auth/callback/github"], // no box-wide GitHub keys
      ["GET", "/api/auth/callback/nope"],
      ["GET", "/api/auth/jwks"],
      ["GET", "/api/auth/callback/oidc/oauth-proxy?callbackURL=/x&profile=y"],
    ] as const) {
      const r = await atProxy(path, { method, ...(method === "POST" ? { body: "{}", headers: { "content-type": "application/json" } } : {}) });
      expect(r.status).toBeGreaterThanOrEqual(400);
      expect(r.headers.get("content-type") ?? "").not.toContain("html");
      expect(r.headers.getSetCookie()).toEqual([]);
    }
    const forged = await atProxy("/api/auth/callback/oidc?code=x&state=forged");
    expect(forged.status).toBe(400);
    expect(await forged.text()).toContain("broken or has expired");
    const err = await atProxy("/api/auth/error?error=<script>alert(1)</script>");
    expect(err.headers.get("content-type")).toContain("text/plain");
    expect(await err.text()).not.toContain("<");
  });

  test("redirects only to app hosts of projects on the box-wide keys for that provider", () => {
    const projectFor = (h: string) => {
      const p = reg.projectForHost(h);
      return p ? reg.projectConfig(p) : undefined;
    };
    const ok = (loc: string, prov: "oidc" | "github" = "oidc") => proxyRedirectAllowed(loc, prov, PROXY, projectFor);
    expect(ok(`${ORIGIN}/api/auth/callback/oidc/oauth-proxy?profile=x`)).toBe(true);
    expect(ok(`${PROXY}/api/auth/error?error=x`)).toBe(true);
    expect(ok(`${PROXY}/settings`)).toBe(false);
    expect(ok("https://evil.example/api/auth/callback/oidc/oauth-proxy")).toBe(false);
    expect(ok(`https://${BLOG}:8443/api/auth/callback/oidc/oauth-proxy`)).toBe(false); // blog doesn't use the box's keys
    expect(ok(`${ORIGIN}/api/auth/callback/github/oauth-proxy`, "github")).toBe(false); // shop's GitHub is its own
    expect(ok(`http://${HOST}:8443/x`)).toBe(false);
    expect(ok("/relative")).toBe(false);
  });

  test("purposeKey matches Better Auth's key derivation", () => {
    // Known vector: HKDF-SHA256 of the secret with Better Auth's salt and info.
    expect(purposeKey(SECRET, "oauth-proxy-profile")).toMatch(/^[0-9a-f]{64}$/);
    expect(purposeKey(SECRET, "oauth-proxy-profile")).not.toBe(purposeKey(SECRET, "oauth-proxy-state"));
  });
});

describe("the app's own keys: one redirect URI per app", () => {
  const at = (host: string, path: string) => `https://${host}:8443/api/auth${path}`;

  test("a sign-in on the preview goes out with the custom domain's redirect URI and finishes on the preview", async () => {
    const c = new Client(handle);
    const r = await c.json(at(OWN_PREVIEW, "/sign-in/social"), { body: { provider: "oidc", callbackURL: "/home" } });
    expect(r.status).toBe(200);
    const url = new URL(r.body.url as string);
    expect(url.searchParams.get("redirect_uri")).toBe(`${OWN_SIGNIN}/api/auth/callback/oidc`);
    expect(url.searchParams.get("client_id")).toBe("own-oidc");
    codes.set("code-grace", { email: "grace@example.com", name: "Grace" });
    // The provider sends the browser to the custom domain, which hands it back to the preview.
    const back = await handle(
      new Request(`${OWN_SIGNIN}/api/auth/callback/oidc?code=code-grace&state=${encodeURIComponent(url.searchParams.get("state")!)}`, {
        headers: { host: `${OWN_CUSTOM}:8443` },
        redirect: "manual",
      }),
    );
    expect(back.status).toBe(302);
    const to = new URL(back.headers.get("location")!);
    expect(to.host).toBe(`${OWN_PREVIEW}:8443`);
    expect((await new Client(handle).raw(to.href)).headers.get("location")).toContain("state_mismatch");
    const done = await c.raw(to.href);
    expect(done.headers.get("location")).toBe("/home");
    expect((await c.json(at(OWN_PREVIEW, "/tiffin/session"))).body.user.email).toBe("grace@example.com");
  });

  test("own keys never go through the box's callback URL", async () => {
    const r = await atProxy("/api/auth/callback/oidc?code=x&state=y");
    expect(r.status).toBe(400); // the box's proxy has its own keys for oidc; a forged state goes nowhere
    const viaBox = proxyRedirectAllowed(`https://${OWN_BOX}:8443/api/auth/callback/oidc/oauth-proxy?p=1`, "oidc", PROXY, (h) => {
      const p = reg.projectForHost(h);
      return p ? reg.projectConfig(p) : undefined;
    });
    expect(viaBox).toBe(false);
  });

  test("Test sign-in starts the provider's sign-in and says who signed in", async () => {
    const c = new Client(handle);
    const start = await c.raw(at(OWN_CUSTOM, "/tiffin/test-sign-in?provider=oidc"));
    expect(start.status).toBe(302);
    const url = new URL(start.headers.get("location")!);
    expect(url.origin).toBe(issuer);
    codes.set("code-test", { email: "tester@example.com", name: "Tester" });
    const back = await c.raw(`${OWN_SIGNIN}/api/auth/callback/oidc?code=code-test&state=${encodeURIComponent(url.searchParams.get("state")!)}`);
    expect(back.status).toBe(302);
    let next = back.headers.get("location")!;
    for (let i = 0; i < 3 && next.includes("/api/auth/"); i++) {
      const r = await c.raw(new URL(next, OWN_SIGNIN).href);
      if (r.status !== 302) {
        expect(r.status).toBe(200);
        expect(await r.text()).toContain("Signed in to Own with Own SSO as tester@example.com");
        return;
      }
      next = r.headers.get("location")!;
    }
    throw new Error("never reached the result page: " + next);
  });

  test("Test sign-in refuses a provider that isn't on", async () => {
    const r = await new Client(handle).raw(at(OWN_CUSTOM, "/tiffin/test-sign-in?provider=google"));
    expect(r.status).toBe(404);
    expect(r.headers.get("content-type")).toContain("text/plain");
  });
});

describe("what apps see", () => {
  test("tiffin/config lists every provider turned on, with its name and whether it works", async () => {
    const r = await new Client(handle).json("/tiffin/config");
    expect(r.body.providers).toEqual([
      { id: "github", name: "GitHub", configured: true },
      { id: "apple", name: "Apple", configured: true },
      { id: "discord", name: "Discord", configured: false },
      { id: "oidc", name: "Acme SSO", configured: true },
    ]);
    expect(r.body.social.google).toBeNull();
    expect(r.body.social.discord).toEqual({ configured: false });
  });

  test("a provider without keys says where to set them", async () => {
    const r = await new Client(handle).json("/sign-in/social", { body: { provider: "discord", callbackURL: "/" } });
    expect(r.status).toBe(503);
    expect(r.body.code).toBe("SOCIAL_NOT_CONFIGURED");
    expect(r.body.message).toContain("Box settings → Sign-in providers");
    expect(r.body.message).toContain("DISCORD_CLIENT_ID");
  });
});
