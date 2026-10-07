// Provider tokens at rest: what Google and GitHub return at sign-in is
// stored encrypted in the project's database, and comes back as it was
// through Better Auth's own endpoints (getAccessToken, refreshToken).
// Google's and GitHub's endpoints are faked.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { symmetricDecrypt } from "better-auth/crypto";
import pg from "pg";
import { adminHandler } from "../src/admin";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

const SECRET = "tokens-secret-0123456789abcdef0123456789abcdef";

let reg: Registry;
let handle: (r: Request) => Promise<Response>;
let db: pg.Pool;
const realFetch = globalThis.fetch;

const b64url = (o: unknown) => Buffer.from(JSON.stringify(o)).toString("base64url");
const idTokenFor = (email: string, n: number) =>
  `${b64url({ alg: "RS256", kid: "k" })}.${b64url({ iss: "https://accounts.google.com", aud: "shop-google", sub: `g-${email}`, email, email_verified: true, name: "Ada", n })}.sig`;

// What the fake providers handed out, and the refresh tokens they were sent.
let issued = 0;
const refreshesSeen: string[] = [];
let githubPeople = new Map<string, string>(); // access token -> email

beforeAll(async () => {
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const form = () => new URLSearchParams(typeof init?.body === "string" ? init.body : init?.body ? String(init.body) : "");
    if (url.startsWith("https://oauth2.googleapis.com/token")) {
      const f = form();
      const n = ++issued;
      if (f.get("grant_type") === "refresh_token") {
        refreshesSeen.push(f.get("refresh_token") ?? "");
        // Google keeps the refresh token and sends no new ID token on a refresh.
        return Response.json({ access_token: `ya29.google-at-${n}`, token_type: "Bearer", expires_in: 3599 });
      }
      const email = f.get("code")!.replace(/^code-/, "");
      return Response.json({
        access_token: `ya29.google-at-${n}`,
        refresh_token: `1//google-rt-${n}`,
        id_token: idTokenFor(email, n),
        token_type: "Bearer",
        expires_in: 3599,
        scope: "openid email profile",
      });
    }
    if (url.startsWith("https://github.com/login/oauth/access_token")) {
      const n = ++issued;
      const f = form();
      if (f.get("grant_type") === "refresh_token") {
        refreshesSeen.push(f.get("refresh_token") ?? "");
        return Response.json({ access_token: `ghu_github-at-${n}`, refresh_token: `ghr_github-rt-${n}`, token_type: "bearer", expires_in: 28800 });
      }
      githubPeople.set(`ghu_github-at-${n}`, f.get("code")!.replace(/^code-/, ""));
      return Response.json({ access_token: `ghu_github-at-${n}`, refresh_token: `ghr_github-rt-${n}`, token_type: "bearer", expires_in: 28800, scope: "read:user,user:email" });
    }
    if (url.startsWith("https://api.github.com/user")) {
      const auth = new Headers(init?.headers).get("authorization") ?? "";
      const email = githubPeople.get(auth.replace(/^Bearer /, ""));
      if (!email) return Response.json({ message: "Bad credentials" }, { status: 401 });
      if (url.endsWith("/emails")) return Response.json([{ email, primary: true, verified: true }]);
      return Response.json({ id: Number(BigInt(Bun.hash(email)) % 1_000_000n), login: email.split("@")[0], name: "Ada", email, avatar_url: "" });
    }
    return realFetch(input as never, init);
  }) as typeof fetch;

  const url = await freshDatabase("tokens_test");
  reg = new Registry(null, {
    version: 1,
    listen: [],
    projects: {
      shop: projectConfig(url, {
        secret: SECRET,
        methods: ["email", "google", "github"],
        captcha: false,
        requireEmailVerification: false,
        social: { google: { clientId: "shop-google", clientSecret: "shop-google-secret", proxied: false }, github: { clientId: "shop-gh", clientSecret: "shop-gh-secret", proxied: false } },
      }),
    },
  });
  handle = publicHandler(reg);
  const r = await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
  expect(r.status).toBe(200);
  db = new pg.Pool({ connectionString: url, max: 2 });
}, 90_000);

afterAll(async () => {
  globalThis.fetch = realFetch;
  await db.end();
  await reg.closeAll();
  await stopCluster();
});

type Row = { id: string; accessToken: string | null; refreshToken: string | null; idToken: string | null };

async function rowFor(providerId: string, email: string): Promise<Row> {
  const r = await db.query<Row>(
    `SELECT a.id, a."accessToken", a."refreshToken", a."idToken" FROM tiffin_auth.account a JOIN tiffin_auth."user" u ON u.id = a."userId" WHERE a."providerId" = $1 AND u.email = $2`,
    [providerId, email],
  );
  expect(r.rows).toHaveLength(1);
  return r.rows[0]!;
}

const open = (data: string) => symmetricDecrypt({ key: SECRET, data });
const hex = /^[0-9a-f]+$/;

/** Signs in (or links, when `link`) with a provider; `email` is who the fake provider says it is. */
async function signIn(c: Client, provider: "google" | "github", email: string, link = false) {
  const r = await c.json(link ? "/link-social" : "/sign-in/social", { body: { provider, callbackURL: "/welcome" } });
  expect(r.status).toBe(200);
  const state = new URL(r.body.url as string).searchParams.get("state")!;
  const back = await c.raw(`/callback/${provider}?code=code-${encodeURIComponent(email)}&state=${encodeURIComponent(state)}`);
  expect(back.status).toBe(302);
  expect(back.headers.get("location")).toBe("/welcome");
}

describe("provider tokens at rest", () => {
  test("Google sign-in stores ciphertext; getAccessToken returns the tokens Google gave", async () => {
    const c = new Client(handle);
    const before = issued;
    await signIn(c, "google", "ada@example.com");
    const n = before + 1;
    const row = await rowFor("google", "ada@example.com");
    for (const [stored, raw] of [
      [row.accessToken, `ya29.google-at-${n}`],
      [row.refreshToken, `1//google-rt-${n}`],
      [row.idToken, idTokenFor("ada@example.com", n)],
    ] as const) {
      expect(stored).not.toBe(raw);
      expect(stored!).toMatch(hex);
      expect(stored!).not.toContain(raw);
      expect(await open(stored!)).toBe(raw);
    }

    const t = await c.json("/get-access-token", { body: { accountId: row.id } });
    expect(t.status).toBe(200);
    expect(t.body.accessToken).toBe(`ya29.google-at-${n}`);
    expect(t.body.idToken).toBe(idTokenFor("ada@example.com", n));
    expect(t.body.scopes).toEqual(["openid", "email", "profile"]);
  });

  test("refreshToken sends Google the real refresh token and stores the new access token encrypted", async () => {
    const c = new Client(handle);
    await signIn(c, "google", "bea@example.com");
    const row = await rowFor("google", "bea@example.com");
    const rt = await open(row.refreshToken!);
    const id = await open(row.idToken!);

    const r = await c.json("/refresh-token", { body: { accountId: row.id } });
    expect(r.status).toBe(200);
    expect(refreshesSeen.at(-1)).toBe(rt);
    const at = r.body.accessToken as string;
    expect(at).toMatch(/^ya29\.google-at-\d+$/);
    expect(r.body.refreshToken).toBe(rt); // Google kept it
    expect(r.body.idToken).toBe(id); // no new one: the stored one, opened

    const after = await rowFor("google", "bea@example.com");
    expect(after.accessToken).not.toBe(at);
    expect(await open(after.accessToken!)).toBe(at);
    expect(after.refreshToken).toBe(row.refreshToken); // not encrypted twice
    expect(after.idToken).toBe(row.idToken);
  });

  test("getAccessToken refreshes an expired token with the real refresh token", async () => {
    const c = new Client(handle);
    await signIn(c, "google", "cy@example.com");
    const row = await rowFor("google", "cy@example.com");
    await db.query(`UPDATE tiffin_auth.account SET "accessTokenExpiresAt" = now() - interval '1 minute' WHERE id = $1`, [row.id]);
    const t = await c.json("/get-access-token", { body: { accountId: row.id } });
    expect(t.status).toBe(200);
    expect(refreshesSeen.at(-1)).toBe(await open(row.refreshToken!));
    const after = await rowFor("google", "cy@example.com");
    expect(await open(after.accessToken!)).toBe(t.body.accessToken);
    expect(t.body.idToken).toBe(await open(row.idToken!));
    expect(after.idToken).toBe(row.idToken);
  });

  test("signing in again replaces the tokens, still encrypted", async () => {
    const c = new Client(handle);
    await signIn(c, "google", "ada@example.com");
    const row = await rowFor("google", "ada@example.com");
    const t = await c.json("/get-access-token", { body: { accountId: row.id } });
    expect(await open(row.accessToken!)).toBe(t.body.accessToken);
    expect(await open(row.idToken!)).toBe(t.body.idToken);
    expect(row.idToken).toMatch(hex);
  });

  test("GitHub sign-in stores ciphertext; getAccessToken and refreshToken return the real tokens", async () => {
    const c = new Client(handle);
    const before = issued;
    await signIn(c, "github", "dee@example.com");
    const n = before + 1;
    const row = await rowFor("github", "dee@example.com");
    expect(row.accessToken).toMatch(hex);
    expect(row.refreshToken).toMatch(hex);
    expect(row.idToken).toBeNull();
    expect(await open(row.accessToken!)).toBe(`ghu_github-at-${n}`);
    expect(await open(row.refreshToken!)).toBe(`ghr_github-rt-${n}`);

    const t = await c.json("/get-access-token", { body: { accountId: row.id } });
    expect(t.status).toBe(200);
    expect(t.body.accessToken).toBe(`ghu_github-at-${n}`);

    const r = await c.json("/refresh-token", { body: { accountId: row.id } });
    expect(r.status).toBe(200);
    expect(refreshesSeen.at(-1)).toBe(`ghr_github-rt-${n}`);
    const after = await rowFor("github", "dee@example.com");
    expect(await open(after.accessToken!)).toBe(r.body.accessToken);
    expect(await open(after.refreshToken!)).toBe(r.body.refreshToken);
    expect(after.refreshToken).not.toBe(r.body.refreshToken);
  });

  test("linking: a password account that adds GitHub, and one Google joins by email, keep their tokens encrypted", async () => {
    const c = new Client(handle);
    const up = await c.json("/sign-up/email", { body: { email: "eve@example.com", password: "correct horse battery", name: "Eve" } });
    expect(up.status).toBe(200);
    await signIn(c, "github", "eve@example.com", true);
    const gh = await rowFor("github", "eve@example.com");
    expect(gh.accessToken).toMatch(hex);
    expect((await c.json("/get-access-token", { body: { accountId: gh.id } })).body.accessToken).toBe(await open(gh.accessToken!));

    // Google is trusted for linking: signing in with the same (confirmed) address joins the account.
    await db.query(`UPDATE tiffin_auth."user" SET "emailVerified" = true WHERE email = $1`, ["eve@example.com"]);
    const g = new Client(handle);
    await signIn(g, "google", "eve@example.com");
    const row = await rowFor("google", "eve@example.com");
    for (const v of [row.accessToken, row.refreshToken, row.idToken]) expect(v).toMatch(hex);
    const t = await g.json("/get-access-token", { body: { accountId: row.id } });
    expect(t.body.accessToken).toBe(await open(row.accessToken!));
    expect(t.body.idToken).toBe(await open(row.idToken!));
    const accounts = await g.json("/list-accounts");
    expect(accounts.body.map((a: { providerId: string }) => a.providerId).sort()).toEqual(["credential", "github", "google"]);
    expect(JSON.stringify(accounts.body)).not.toContain(row.accessToken!);
    // getAccessToken's accountId is the id listAccounts gives.
    expect(accounts.body.find((a: { providerId: string }) => a.providerId === "google").id).toBe(row.id);
  });

  test("only the account's own user can read its tokens", async () => {
    const row = await rowFor("google", "ada@example.com");
    expect((await new Client(handle).json("/get-access-token", { body: { accountId: row.id } })).status).toBe(401);
    const other = new Client(handle);
    await signIn(other, "github", "fay@example.com");
    const t = await other.json("/get-access-token", { body: { accountId: row.id } });
    expect(t.status).toBe(400);
    expect(t.body.accessToken).toBeUndefined();
  });
});
