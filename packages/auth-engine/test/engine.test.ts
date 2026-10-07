import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createLocalJWKSet, jwtVerify } from "jose";
import pg from "pg";
import { adminHandler } from "../src/admin";
import { migrate } from "../src/migrate";
import { Registry } from "../src/registry";
import { publicHandler } from "../src/server";
import { Client, HOST, ORIGIN, lastMail, linkIn, person, projectConfig } from "./helpers";
import { freshDatabase, stopCluster } from "./pg";

let reg: Registry;
let handle: (r: Request) => Promise<Response>;
let admin: (r: Request) => Promise<Response>;
let dbUrl: string;

const adminCall = async (method: string, path: string, body?: unknown) => {
  const res = await admin(new Request("http://admin" + path, { method, body: body === undefined ? undefined : JSON.stringify(body) }));
  return { status: res.status, body: (await res.json()) as any };
};

beforeAll(async () => {
  dbUrl = await freshDatabase("engine_test");
  reg = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(dbUrl) } });
  handle = publicHandler(reg);
  admin = adminHandler(reg);
  const r = await adminCall("POST", "/projects/shop/migrate");
  expect(r.status).toBe(200);
  expect(r.body.created).toContain("user");
  expect(r.body.created).toContain("organization");
  expect(r.body.created).toContain("orgInviteLink");
}, 60_000);

afterAll(async () => {
  await reg.closeAll();
  await stopCluster();
});

describe("routing and config", () => {
  test("unknown hosts are refused", async () => {
    const res = await handle(new Request("https://nope.tiffin.localhost/api/auth/ok", { headers: { host: "nope.tiffin.localhost" } }));
    expect(res.status).toBe(404);
    expect(((await res.json()) as any).code).toBe("unknown_host");
  });

  test("migrations are idempotent", async () => {
    const r = await migrate("shop", reg.projectConfig("shop")!, reg.pool("shop")!);
    expect(r.created).toEqual([]);
    expect(r.added).toEqual([]);
  });

  test("public config lists methods and what is set up", async () => {
    const c = new Client(handle);
    const { status, body } = await c.json("/tiffin/config");
    expect(status).toBe(200);
    expect(body.appName).toBe("Shop");
    expect(body.social.google).toEqual({ configured: false });
    expect(body.captcha).toBe(true);
  });

  test("social sign-in without an OAuth app explains how to fix it", async () => {
    const c = new Client(handle);
    const { status, body } = await c.json("/sign-in/social", { body: { provider: "google", callbackURL: "/" } });
    expect(status).toBe(503);
    expect(body.code).toBe("SOCIAL_NOT_CONFIGURED");
    expect(body.message).toContain("GOOGLE_CLIENT_ID");
    expect(body.message).toContain(`${ORIGIN}/api/auth/callback/google`);
  });
});

describe("captcha", () => {
  test("sign-up needs a proof-of-work solution", async () => {
    const c = new Client(handle);
    const r = await c.json("/sign-up/email", { body: { email: "x@example.com", password: "correct horse battery", name: "X" } });
    expect(r.status).toBe(400);
    expect(r.body.code).toBe("CAPTCHA_REQUIRED");
  });

  test("a solution works once", async () => {
    const c = new Client(handle);
    const sol = await c.captcha();
    const body = { email: "once@example.com", password: "wrong password!!" };
    const a = await c.json("/sign-in/email", { body, headers: { "x-captcha-response": sol } });
    expect(a.status).toBe(401); // the captcha passed; the credentials did not
    const b = await c.json("/sign-in/email", { body, headers: { "x-captcha-response": sol } });
    expect(b.status).toBe(400);
    expect(b.body.code).toBe("CAPTCHA_USED");
  });

  test("a forged solution is refused", async () => {
    const c = new Client(handle);
    const sol = JSON.parse(Buffer.from(await c.captcha(), "base64").toString());
    sol.solution.derivedKey = sol.solution.derivedKey.replace(/^./, (ch: string) => (ch === "0" ? "1" : "0"));
    const r = await c.json("/sign-in/email", { body: { email: "a@b.co", password: "whatever123" }, headers: { "x-captcha-response": Buffer.from(JSON.stringify(sol)).toString("base64") } });
    expect(r.status).toBe(400);
    expect(r.body.code).toBe("CAPTCHA_INVALID");
  });
});

describe("email + password", () => {
  test("sign-up → verification email → verified and signed in, with a personal org", async () => {
    const c = new Client(handle);
    const r = await c.withCaptcha("/sign-up/email", { email: "alice@example.com", password: "correct horse battery", name: "Alice" });
    expect(r.status).toBe(200);
    expect(r.body.token).toBeNull(); // not signed in until verified

    const blocked = await c.withCaptcha("/sign-in/email", { email: "alice@example.com", password: "correct horse battery" });
    expect(blocked.status).toBe(403);

    const mail = lastMail("alice@example.com", "verify");
    expect(mail.subject).toBe("Confirm your email for Shop");
    expect(mail.html).toContain("Confirm email");
    const link = linkIn(mail.text);
    expect(link.startsWith(`${ORIGIN}/api/auth/verify-email?token=`)).toBe(true);
    const v = await c.raw(link);
    expect([200, 302]).toContain(v.status);

    const s = await c.json("/tiffin/session");
    expect(s.body.user.email).toBe("alice@example.com");
    expect(s.body.user.emailVerified).toBe(true);
    expect(s.body.user.banned).toBeUndefined();
    expect(s.body.organization.name).toBe("Personal");
    expect(s.body.organization.role).toBe("owner");
    expect(s.body.via).toBe("session");
  });

  test("sign-in with the password", async () => {
    const c = new Client(handle);
    const r = await c.withCaptcha("/sign-in/email", { email: "alice@example.com", password: "correct horse battery" });
    expect(r.status).toBe(200);
    const s = await c.json("/get-session");
    expect(s.body.user.email).toBe("alice@example.com");
  });
});

describe("email verification off", () => {
  test("sign-up signs in at once and sends no confirmation", async () => {
    const db = await freshDatabase("engine_noverify");
    const r2 = new Registry(null, { version: 1, listen: [], projects: { shop: projectConfig(db, { requireEmailVerification: false }) } });
    try {
      const a = adminHandler(r2);
      const m = await a(new Request("http://admin/projects/shop/migrate", { method: "POST" }));
      expect(m.status).toBe(200);
      const c = new Client(publicHandler(r2));
      const r = await c.withCaptcha("/sign-up/email", { email: "tester@example.com", password: "correct horse battery", name: "Tester" });
      expect(r.status).toBe(200);
      expect(typeof r.body.token).toBe("string"); // signed in, no confirmation needed
      const s = await c.json("/tiffin/session");
      expect(s.body.user.email).toBe("tester@example.com");
      expect(s.body.user.emailVerified).toBe(false);
      // The first session already starts in the personal org, and so does its signed cookie.
      expect(s.body.organization).toMatchObject({ name: "Personal", role: "owner" });
      const signed = JSON.parse(Buffer.from(c.cookies.get("__Secure-tiffin.session_data")!.split(".")[1]!, "base64url").toString());
      expect(signed.tiffin.organization).toMatchObject({ id: s.body.organization.id, role: "owner" });
      const orgs = await c.json("/organization/list");
      expect(orgs.body.length).toBe(1);
      expect(() => lastMail("tester@example.com", "verify")).toThrow();
      const again = new Client(publicHandler(r2));
      const si = await again.withCaptcha("/sign-in/email", { email: "tester@example.com", password: "correct horse battery" });
      expect(si.status).toBe(200);
    } finally {
      await r2.closeAll();
    }
  }, 60_000);
});

describe("passwordless", () => {
  test("magic link signs in (and signs up) by email", async () => {
    const c = new Client(handle);
    const r = await c.withCaptcha("/sign-in/magic-link", { email: "maggie@example.com", callbackURL: "/" });
    expect(r.status).toBe(200);
    const mail = lastMail("maggie@example.com", "magic-link");
    expect(mail.subject).toBe("Sign in to Shop");
    const v = await c.raw(linkIn(mail.text));
    expect(v.status).toBe(302);
    const s = await c.json("/get-session");
    expect(s.body.user.email).toBe("maggie@example.com");
    expect(s.body.user.emailVerified).toBe(true);
  });

  test("email one-time code signs in", async () => {
    const c = new Client(handle);
    const r = await c.withCaptcha("/email-otp/send-verification-otp", { email: "otto@example.com", type: "sign-in" });
    expect(r.status).toBe(200);
    const mail = lastMail("otto@example.com", "otp");
    const code = mail.subject.split(" ")[0]!;
    expect(code).toMatch(/^\d{6}$/);
    const bad = await c.json("/sign-in/email-otp", { body: { email: "otto@example.com", otp: code === "000000" ? "111111" : "000000" } });
    expect(bad.status).toBe(400);
    const ok = await c.json("/sign-in/email-otp", { body: { email: "otto@example.com", otp: code } });
    expect(ok.status).toBe(200);
    const s = await c.json("/get-session");
    expect(s.body.user.email).toBe("otto@example.com");
  });
});

describe("account email", () => {
  test("change email: the old address approves, the new one confirms, the old one is told", async () => {
    const c = new Client(handle);
    await c.withCaptcha("/sign-in/magic-link", { email: "mover@example.com", callbackURL: "/" });
    expect((await c.raw(linkIn(lastMail("mover@example.com", "magic-link").text))).status).toBe(302);

    const r = await c.json("/change-email", { body: { newEmail: "moved@example.com", callbackURL: "/" } });
    expect(r.status).toBe(200);
    const approve = lastMail("mover@example.com", "change-email");
    expect(approve.subject).toBe("Approve your new email for Shop");
    expect([200, 302]).toContain((await c.raw(linkIn(approve.text))).status);
    const confirm = lastMail("moved@example.com", "verify");
    expect([200, 302]).toContain((await c.raw(linkIn(confirm.text))).status);

    const s = await c.json("/get-session");
    expect(s.body.user.email).toBe("moved@example.com");
    const notice = lastMail("mover@example.com", "email-changed");
    expect(notice.subject).toBe("Your Shop email was changed");
    expect(notice.text).toContain("moved@example.com");
  });

  test("two-step sign-in by an emailed code, the code in the subject", async () => {
    const pw = "correct horse battery";
    const c = await person(handle, "twostep@example.com", "Tess", pw);
    expect((await c.json("/two-factor/enable", { body: { password: pw } })).status).toBe(200);
    const codeIn = () => {
      const mail = lastMail("twostep@example.com", "two-factor");
      const code = mail.subject.split(" ")[0]!;
      expect(code).toMatch(/^\d{6}$/);
      return code;
    };
    // Turning it on: one emailed code proves the address works.
    expect((await c.json("/two-factor/send-otp", { body: {} })).status).toBe(200);
    expect((await c.json("/two-factor/verify-otp", { body: { code: codeIn() } })).status).toBe(200);

    const d = new Client(handle);
    const first = await d.withCaptcha("/sign-in/email", { email: "twostep@example.com", password: pw });
    expect(first.status).toBe(200);
    expect(first.body.twoFactorRedirect).toBe(true);
    expect((await d.json("/two-factor/send-otp", { body: {} })).status).toBe(200);
    expect((await d.json("/two-factor/verify-otp", { body: { code: codeIn() } })).status).toBe(200);
    expect((await d.json("/get-session")).body.user.email).toBe("twostep@example.com");
  });
});

describe("organizations, roles and invites", () => {
  let alice: Client, bob: Client, carol: Client;
  let acme: string;
  let bobMember: string;

  beforeAll(async () => {
    alice = new Client(handle);
    await alice.withCaptcha("/sign-in/email", { email: "alice@example.com", password: "correct horse battery" });
    bob = await person(handle, "bob@example.com", "Bob");
    carol = await person(handle, "carol@example.com", "Carol");
  }, 60_000);

  test("create an organization", async () => {
    const r = await alice.json("/organization/create", { body: { name: "Acme", slug: "acme" } });
    expect(r.status).toBe(200);
    acme = r.body.id;
    const set = await alice.json("/organization/set-active", { body: { organizationId: acme } });
    expect(set.status).toBe(200);
    const s = await alice.json("/tiffin/session");
    expect(s.body.organization).toMatchObject({ id: acme, name: "Acme", role: "owner" });
  });

  test("email invite → accept → member with the invited role", async () => {
    const inv = await alice.json("/organization/invite-member", { body: { email: "bob@example.com", role: "admin", organizationId: acme } });
    expect(inv.status).toBe(200);
    const mail = lastMail("bob@example.com", "invitation");
    expect(mail.subject).toBe("Alice invited you to Acme on Shop");
    const link = linkIn(mail.text);
    expect(link).toBe(`${ORIGIN}/accept-invite?invitation=${inv.body.id}`);
    const acc = await bob.json("/organization/accept-invitation", { body: { invitationId: inv.body.id } });
    expect(acc.status).toBe(200);
    bobMember = acc.body.member.id;
    expect(acc.body.member.role).toBe("admin");
  });

  test("nobody grants a role above their own", async () => {
    const up = await bob.json("/organization/invite-member", { body: { email: "carol@example.com", role: "owner", organizationId: acme } });
    expect(up.status).toBe(403);
    const members = await alice.json(`/organization/list-members?organizationId=${acme}`);
    const aliceMember = members.body.members.find((m: any) => m.user.email === "alice@example.com");
    const demoteOwner = await bob.json("/organization/update-member-role", { body: { memberId: aliceMember.id, role: "member", organizationId: acme } });
    expect(demoteOwner.status).toBe(403);
    expect(demoteOwner.body.code).toBe("MEMBER_ABOVE_YOU");
  });

  test("role change by the owner", async () => {
    const r = await alice.json("/organization/update-member-role", { body: { memberId: bobMember, role: "member", organizationId: acme } });
    expect(r.status).toBe(200);
    const again = await bob.json("/organization/invite-member", { body: { email: "carol@example.com", role: "viewer", organizationId: acme } });
    expect(again.status).toBe(403);
  });

  test("link invites: role, max uses, revoke", async () => {
    const bad = await bob.json("/invite-link/create", { body: { organizationId: acme, role: "viewer" } });
    expect(bad.status).toBe(403);
    const link = await alice.json("/invite-link/create", { body: { organizationId: acme, role: "viewer", maxUses: 1 } });
    expect(link.status).toBe(200);
    expect(link.body.url).toBe(`${ORIGIN}/accept-invite?link=${encodeURIComponent(link.body.token)}`);
    const peek = await new Client(handle).json(`/invite-link/get?token=${encodeURIComponent(link.body.token)}`);
    expect(peek.body).toMatchObject({ valid: true, role: "viewer", organization: { name: "Acme" } });
    const acc = await carol.json("/invite-link/accept", { body: { token: link.body.token } });
    expect(acc.status).toBe(200);
    expect(acc.body.member.role).toBe("viewer");
    const dave = await person(handle, "dave@example.com", "Dave");
    const used = await dave.json("/invite-link/accept", { body: { token: link.body.token } });
    expect(used.status).toBe(400);
    expect(used.body.code).toBe("INVITE_LINK_INVALID");
    const after = await new Client(handle).json(`/invite-link/get?token=${encodeURIComponent(link.body.token)}`);
    expect(after.body.valid).toBe(false);

    const second = await alice.json("/invite-link/create", { body: { organizationId: acme, role: "member" } });
    const rev = await alice.json("/invite-link/revoke", { body: { id: second.body.id } });
    expect(rev.body.active).toBe(false);
    const late = await dave.json("/invite-link/accept", { body: { token: second.body.token } });
    expect(late.body.code).toBe("INVITE_LINK_INVALID");
    const list = await alice.json(`/invite-link/list?organizationId=${acme}`);
    expect(list.body.length).toBe(2);
    expect(list.body[0].token).toBeUndefined();
  });

  test("API keys act as their sponsor, capped, and can't escalate", async () => {
    const k = await alice.json("/api-key/create", { body: { name: "agent", metadata: { maxRole: "member" } } });
    expect(k.status).toBe(200);
    const agent = new Client(handle, { "x-api-key": k.body.key });
    const s = await agent.json(`/tiffin/session?organizationId=${acme}`);
    expect(s.body.via).toBe("api-key");
    expect(s.body.organization).toMatchObject({ id: acme, memberRole: "owner", role: "member" });
    // capped at member: can't invite or change roles even though the sponsor is owner
    const inv = await agent.json("/organization/invite-member", { body: { email: "eve@example.com", role: "viewer", organizationId: acme } });
    expect(inv.status).toBe(403);
    const rc = await agent.json("/organization/update-member-role", { body: { memberId: bobMember, role: "admin", organizationId: acme } });
    expect(rc.status).toBe(403);
    // keys can't mint keys (a new key would carry no cap)
    const mint = await agent.json("/api-key/create", { body: { name: "child" } });
    expect(mint.status).toBe(403);
    expect(mint.body.code).toBe("API_KEY_NOT_ALLOWED");
    // a client can't set server-only permissions on a key
    const perms = await alice.json("/api-key/create", { body: { name: "sneaky", permissions: { organization: ["delete"] } } });
    expect(perms.status).toBe(400);
    // a key whose cap claims more than the sponsor has gets the sponsor's role
    const bk = await bob.json("/api-key/create", { body: { name: "bob-agent", metadata: { maxRole: "owner" } } });
    const bobAgent = new Client(handle, { "x-api-key": bk.body.key });
    const bs = await bobAgent.json(`/tiffin/session?organizationId=${acme}`);
    expect(bs.body.organization.role).toBe("member");
    const bInv = await bobAgent.json("/organization/invite-member", { body: { email: "eve@example.com", role: "viewer", organizationId: acme } });
    expect(bInv.status).toBe(403);
  });

  test("JWTs carry the org and role and verify against the JWKS", async () => {
    const t = await alice.json("/token");
    expect(t.status).toBe(200);
    const jwks = await new Client(handle).json("/jwks");
    const { payload } = await jwtVerify(t.body.token, createLocalJWKSet(jwks.body), { issuer: ORIGIN, audience: ORIGIN });
    expect(payload.email).toBe("alice@example.com");
    expect(payload.org).toBe(acme);
    expect(payload.role).toBe("owner");
  });

  test("admin API: users, orgs, stats, ban and revoke", async () => {
    const users = await adminCall("GET", "/projects/shop/users?search=bob");
    expect(users.body.total).toBe(1);
    const bobId = users.body.users[0].id;
    const detail = await adminCall("GET", `/projects/shop/users/${bobId}`);
    expect(detail.body.memberships.map((m: any) => m.name).sort()).toEqual(["Acme", "Personal"]);
    expect(detail.body.sessions.length).toBeGreaterThan(0);
    expect(detail.body.apiKeys.length).toBe(1);

    const orgs = await adminCall("GET", "/projects/shop/orgs?search=acme");
    expect(orgs.body.organizations[0]).toMatchObject({ name: "Acme", memberCount: 3 });
    const org = await adminCall("GET", `/projects/shop/orgs/${acme}`);
    expect(org.body.members.map((m: any) => `${m.email}:${m.role}`).sort()).toEqual(["alice@example.com:owner", "bob@example.com:member", "carol@example.com:viewer"]);
    expect(org.body.inviteLinks.length).toBe(2);

    const stats = await adminCall("GET", "/projects/shop/stats");
    expect(stats.body.users).toBeGreaterThanOrEqual(6);
    expect(stats.body.organizations).toBeGreaterThanOrEqual(7);

    const ban = await adminCall("POST", `/projects/shop/users/${bobId}/ban`, { reason: "spam" });
    expect(ban.body.banned).toBe(true);
    expect(ban.body.sessionsRevoked).toBeGreaterThan(0);
    expect((await bob.json("/get-session")).body).toBeNull();
    const again = await new Client(handle).withCaptcha("/sign-in/email", { email: "bob@example.com", password: "correct horse battery" });
    expect(again.status).toBe(403);
    expect(again.body.code).toBe("BANNED");
    await adminCall("POST", `/projects/shop/users/${bobId}/unban`);
    const back = await new Client(handle).withCaptcha("/sign-in/email", { email: "bob@example.com", password: "correct horse battery" });
    expect(back.status).toBe(200);

    const rv = await adminCall("POST", `/projects/shop/users/${bobId}/revoke-sessions`);
    expect(rv.body.sessionsRevoked).toBeGreaterThan(0);
  });
});

describe("row-level security helpers", () => {
  test("tiffin_auth.enable_org_rls isolates two orgs' rows", async () => {
    const su = new pg.Client(dbUrl);
    await su.connect();
    await su.query(`DROP ROLE IF EXISTS app_rls; CREATE ROLE app_rls LOGIN PASSWORD 'app'`);
    await su.query(`GRANT CREATE, USAGE ON SCHEMA public TO app_rls`);
    await su.end();
    const app = new pg.Client(dbUrl.replace("postgres:postgres@", "app_rls:app@"));
    await app.connect();
    // the app role owns its table; FORCE makes the policy apply to it anyway
    await app.query(`CREATE TABLE notes (id serial primary key, org_id text not null, body text not null)`);
    await app.query(`SELECT tiffin_auth.enable_org_rls('notes')`);
    const as = async (org: string | null, f: () => Promise<unknown>) => {
      await app.query("BEGIN");
      await app.query(`SELECT set_config('app.org_id', $1, true)`, [org ?? ""]);
      try {
        return await f();
      } finally {
        await app.query("COMMIT");
      }
    };
    await as("org_a", () => app.query(`INSERT INTO notes (org_id, body) VALUES ('org_a', 'a1'), ('org_a', 'a2')`));
    await as("org_b", () => app.query(`INSERT INTO notes (org_id, body) VALUES ('org_b', 'b1')`));
    const a = (await as("org_a", () => app.query(`SELECT body FROM notes ORDER BY body`))) as pg.QueryResult;
    expect(a.rows.map((r) => r.body)).toEqual(["a1", "a2"]);
    const b = (await as("org_b", () => app.query(`SELECT body FROM notes`))) as pg.QueryResult;
    expect(b.rows.map((r) => r.body)).toEqual(["b1"]);
    const none = (await as(null, () => app.query(`SELECT body FROM notes`))) as pg.QueryResult;
    expect(none.rows).toEqual([]);
    await app.query("BEGIN");
    await app.query(`SELECT set_config('app.org_id', 'org_a', true)`);
    await expect(app.query(`INSERT INTO notes (org_id, body) VALUES ('org_b', 'sneak')`)).rejects.toThrow(/row-level security/);
    await app.query("ROLLBACK");
    const upd = (await as("org_a", () => app.query(`UPDATE notes SET body = 'x' WHERE org_id = 'org_b'`))) as pg.QueryResult;
    expect(upd.rowCount).toBe(0);
    await app.end();
  });
});

describe("delete", () => {
  test("drop needs the project name and removes the schema", async () => {
    const no = await adminCall("POST", "/projects/shop/drop");
    expect(no.status).toBe(428);
    // keep the main DB for other tests: drop a second project's schema
    const url2 = await freshDatabase("engine_drop");
    reg.set({ version: 1, listen: [], projects: { shop: reg.projectConfig("shop")!, other: projectConfig(url2, { hosts: ["other.tiffin.localhost"] }) } });
    const c = new pg.Client(url2);
    await c.connect();
    // `auth` is a name apps may use: the engine keeps to tiffin_auth and
    // never touches the app's own schema, on migrate or on drop.
    await c.query(`CREATE SCHEMA auth; CREATE TABLE auth."user" (id text); INSERT INTO auth."user" VALUES ('mine')`);
    const m = await adminCall("POST", "/projects/other/migrate");
    expect(m.body.created).toContain("user");
    const where = await c.query(`SELECT to_regclass('tiffin_auth."user"')::text AS t, to_regclass('tiffin_auth.session')::text AS s`);
    expect(where.rows[0]).toEqual({ t: 'tiffin_auth."user"', s: "tiffin_auth.session" });
    const ok = await adminCall("POST", "/projects/other/drop?confirm=other");
    expect(ok.body.dropped).toBe(true);
    const r = await c.query(`SELECT to_regnamespace('tiffin_auth') AS t, (SELECT array_agg(id) FROM auth."user") AS mine`);
    await c.end();
    expect(r.rows[0].t).toBeNull();
    expect(r.rows[0].mine).toEqual(["mine"]);
  });
});

test("host header picks the project", () => {
  expect(reg.projectForHost(`${HOST}:8443`)).toBe("shop");
});
