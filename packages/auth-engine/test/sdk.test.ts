// @shiptiffin/sdk/auth against the real engine: what an app's server code sees.
import { afterAll, beforeAll, expect, test } from "bun:test";
import pg from "pg";
import { getSession, requireRole, sessionFor, verifyToken, withOrg } from "../../sdk/src/auth";
import { adminHandler } from "../src/admin";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, HOST, ORIGIN, person, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

let reg: Registry;
let handle: (r: Request) => Promise<Response>;
let dbUrl: string;
// The app calls the engine "internally": no Host of its own, x-tiffin-host names it.
const engineFetch = ((url: string, init?: RequestInit) => {
  const h = new Headers(init?.headers);
  h.set("host", "127.0.0.1:7393");
  return handle(new Request(url, { ...init, headers: h }));
}) as unknown as typeof fetch;
const opts = { url: "http://127.0.0.1:7393/api/auth", host: HOST, fetch: engineFetch };

// Another project on the box, with its own users.
const EVIL = "evil.tiffin.localhost";

beforeAll(async () => {
  dbUrl = await freshDatabase("sdk_test");
  const evilUrl = await freshDatabase("sdk_test_evil");
  const evil = projectConfig(evilUrl, {
    hosts: [EVIL],
    primaryUrl: `https://${EVIL}:8443`,
    origins: [`https://${EVIL}:8443`],
    captcha: false,
    requireEmailVerification: false,
  });
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(dbUrl), evil } });
  handle = publicHandler(reg);
  await adminHandler(reg)(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
  await adminHandler(reg)(new Request("http://admin/projects/evil/migrate", { method: "POST" }));
}, 60_000);

afterAll(async () => {
  await reg.closeAll();
  await stopCluster();
});

test("getSession / requireRole / API key caps / JWT / withOrg with RLS", async () => {
  const ana = await person(handle, "ana@example.com", "Ana");
  const appReq = (h: Record<string, string>) => new Request(`${ORIGIN}/dashboard`, { headers: h });

  // Cookie session: personal org is active, Ana owns it.
  const s = await getSession(appReq({ cookie: ana.cookieHeader() }), opts);
  expect(s?.user.email).toBe("ana@example.com");
  expect(s?.organization?.role).toBe("owner");
  expect(await getSession(appReq({ cookie: "tiffin.session_token=nope" }), opts)).toBeNull();

  // A team org, and an agent key capped at viewer.
  const team = await ana.json("/organization/create", { body: { name: "Team", slug: "team" } });
  const key = await ana.json("/api-key/create", { body: { name: "reporter", metadata: { maxRole: "viewer" } } });
  const agentReq = appReq({ "x-api-key": key.body.key });
  const viewer = await requireRole(agentReq, "viewer", { ...opts, organizationId: team.body.id });
  expect(viewer.via).toBe("api-key");
  expect(viewer.organization.role).toBe("viewer");
  const denied = await requireRole(agentReq, "member", { ...opts, organizationId: team.body.id }).catch((e) => e);
  expect(denied.status).toBe(403);

  // JWT for a downstream service, verified with the engine's JWKS.
  const t = await ana.json("/token");
  const claims = await verifyToken(t.body.token, { ...opts, issuer: ORIGIN, audience: ORIGIN });
  expect(claims.email).toBe("ana@example.com");
  expect(claims.role).toBe("owner");
  const forged = t.body.token.slice(0, -4) + (t.body.token.endsWith("AAAA") ? "BBBB" : "AAAA");
  expect((await verifyToken(forged, opts).catch((e) => e)).status).toBe(401);

  // withOrg scopes rows by organization through RLS.
  const su = new pg.Client(dbUrl);
  await su.connect();
  await su.query(`CREATE ROLE app_sdk LOGIN PASSWORD 'app'; GRANT CREATE, USAGE ON SCHEMA public TO app_sdk`);
  await su.end();
  const pool = new pg.Pool({ connectionString: dbUrl.replace("postgres:postgres@", "app_sdk:app@") });
  await pool.query(`CREATE TABLE docs (id serial primary key, org_id text not null, title text not null)`);
  await pool.query(`SELECT tiffin_auth.enable_org_rls('docs')`);
  const c = await pool.connect();
  await withOrg(c, team.body.id, (tx) => tx.query(`INSERT INTO docs (org_id, title) VALUES ($1, 'team plan')`, [team.body.id]));
  await withOrg(c, s!.organization!.id, (tx) => tx.query(`INSERT INTO docs (org_id, title) VALUES ($1, 'diary')`, [s!.organization!.id]));
  const seen = await withOrg(c, team.body.id, (tx) => tx.query(`SELECT title FROM docs`));
  expect((seen as pg.QueryResult).rows.map((r) => r.title)).toEqual(["team plan"]);
  const sneak = await withOrg(c, team.body.id, (tx) => tx.query(`INSERT INTO docs (org_id, title) VALUES ('someone-else', 'x')`)).catch((e) => e);
  expect(String(sneak)).toContain("row-level security");
  c.release();
  await pool.end();
}, 60_000);

test("fast path: the signed session cookie is read without the engine, and stays honest", async () => {
  const bea = await person(handle, "bea@example.com", "Bea");
  let calls = 0;
  const counting = ((url: string, init?: RequestInit) => {
    if (url.includes("/tiffin/session")) calls++;
    return engineFetch(url, init);
  }) as unknown as typeof fetch;
  const o = { ...opts, fetch: counting };
  const appReq = () => new Request(`${ORIGIN}/dashboard`, { headers: { cookie: bea.cookieHeader() } });

  const viaCookie = await getSession(appReq(), o);
  const viaEngine = await getSession(appReq(), { ...o, fresh: true });
  expect(calls).toBe(1);
  const same = (s: typeof viaCookie) => ({ ...s, user: { ...s!.user, updatedAt: null } });
  expect(same(viaCookie)).toEqual(same(viaEngine)); // the cookie holds the user as of signing in

  // The engine's answer re-signs the cookie, for frameworks to pass on.
  const { setCookie } = await sessionFor(appReq().headers, { ...o, fresh: true });
  expect(setCookie.some((c) => c.startsWith("__Host-tiffin.session_data="))).toBe(true);

  // Creating an organization makes it active and re-signs the cookie.
  const team = await bea.json("/organization/create", { body: { name: "Bea's team", slug: "bea-team" } });
  expect((await getSession(appReq(), o))?.organization?.id).toBe(team.body.id);

  // Signed out elsewhere: the engine says so at once; a copied cookie lasts until it expires (60 s).
  const copy = bea.cookieHeader();
  await bea.json("/sign-out", { body: {} });
  const stale = new Request(`${ORIGIN}/dashboard`, { headers: { cookie: copy } });
  expect(await getSession(stale, { ...o, fresh: true })).toBeNull();
  expect((await getSession(stale, o))?.user.email).toBe("bea@example.com");

  // Latency, one process (engine in-process, Postgres local).
  const time = async (f: () => Promise<unknown>) => {
    const t = performance.now();
    for (let i = 0; i < 200; i++) await f();
    return (performance.now() - t) / 200;
  };
  const signedIn = await person(handle, "cal@example.com", "Cal");
  const r = () => new Request(`${ORIGIN}/x`, { headers: { cookie: signedIn.cookieHeader() } });
  const fast = await time(() => getSession(r(), o));
  const engine = await time(() => getSession(r(), { ...o, fresh: true }));
  console.log(JSON.stringify({ getSessionMs: { signedCookie: +fast.toFixed(3), engine: +engine.toFixed(3) } }));
  expect(fast).toBeLessThan(engine);
}, 60_000);

test("a request naming another project's host is still checked against this app's project", async () => {
  // Mallory owns an account on the evil app, and calls the shop app's
  // server directly (apps reach each other on the box), naming evil's host.
  const mallory = new Client(handle);
  const up = await mallory.json(`https://${EVIL}:8443/api/auth/sign-up/email`, { body: { email: "mallory@example.com", password: "correct horse battery", name: "M" } });
  expect(up.status).toBe(200);
  expect(mallory.cookies.size).toBeGreaterThan(0);
  const direct = (extra: Record<string, string> = {}) =>
    new Request(`http://10.0.0.5:3000/admin`, { headers: { host: EVIL, "x-forwarded-host": EVIL, "x-forwarded-proto": "https", cookie: mallory.cookieHeader(), ...extra } });
  const appOpts = { url: "http://127.0.0.1:7393/api/auth", fetch: engineFetch };
  const was = process.env.TIFFIN_AUTH_HOST;
  process.env.TIFFIN_AUTH_HOST = HOST; // what the box gives the shop app
  try {
    expect(await getSession(direct(), appOpts)).toBeNull();
    expect(await getSession(direct(), { ...appOpts, fresh: true })).toBeNull();
    expect((await requireRole(direct(), "viewer", appOpts).catch((e) => e)).status).toBe(401);
    // The engine itself: a pinned call names a host of another project: answered for the pinned one.
    const res = await engineFetch("http://127.0.0.1:7393/api/auth/tiffin/session", { headers: { cookie: mallory.cookieHeader(), "x-tiffin-host": EVIL, "x-tiffin-auth-host": HOST } });
    expect(await res.json()).toBeNull();
  } finally {
    if (was === undefined) delete process.env.TIFFIN_AUTH_HOST;
    else process.env.TIFFIN_AUTH_HOST = was;
  }
  // Without the pin (no TIFFIN_AUTH_HOST, outside a box) the named host is used, as before.
  expect((await getSession(direct(), appOpts))?.user.email).toBe("mallory@example.com");
}, 60_000);

test("an Authorization header is never the session's identity in the SDK's memory", async () => {
  const ana2 = await person(handle, "ana2@example.com", "Ana Two");
  const appReq = (h: Record<string, string>) => new Request(`${ORIGIN}/dashboard`, { headers: h });
  const shared = { authorization: "Basic c2hhcmVkOnZhbHVl" };
  const s = await getSession(appReq({ cookie: ana2.cookieHeader(), ...shared }), { ...opts, fresh: true });
  expect(s?.user.email).toBe("ana2@example.com");
  // Same header, no cookie, within the SDK's 5-second memory: nobody.
  expect(await getSession(appReq(shared), opts)).toBeNull();
}, 60_000);
