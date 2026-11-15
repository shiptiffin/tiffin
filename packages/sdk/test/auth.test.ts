import { describe, expect, spyOn, test } from "bun:test";
import { AuthError, createUser, getSession, getUserByEmail, requireRole, requireUser, roleAtLeast, withOrg } from "../src/auth";

const session = (role: string | null) => ({
  user: { id: "u1", email: "a@b.co", name: "A", emailVerified: true, createdAt: "", updatedAt: "" },
  session: { id: "s1", expiresAt: "", activeOrganizationId: "o1" },
  organization: role ? { id: "o1", name: "Acme", slug: "acme", role, memberRole: role } : null,
  via: "session",
  apiKey: null,
});

function fakeFetch(body: unknown, status = 200) {
  const calls: { url: string; headers: Headers }[] = [];
  const f = (async (url: string, init?: RequestInit) => {
    calls.push({ url, headers: new Headers(init?.headers) });
    return Response.json(body, { status });
  }) as unknown as typeof fetch;
  return { f, calls };
}

const req = (headers: Record<string, string>) => new Request("https://shop.example.com/notes", { headers });
// A new session token each time: answers are remembered per token for 5 s.
let n = 0;
const tok = () => `__Host-tiffin.session_token=t${++n}.sig`;

describe("roles", () => {
  test("ordering and multi-role strings", () => {
    expect(roleAtLeast("admin", "member")).toBe(true);
    expect(roleAtLeast("member", "admin")).toBe(false);
    expect(roleAtLeast("viewer,admin", "admin")).toBe(true);
    expect(roleAtLeast(null, "viewer")).toBe(false);
    expect(roleAtLeast("owner", "owner")).toBe(true);
  });
});

describe("getSession", () => {
  test("forwards the cookie, API key and host to the internal endpoint", async () => {
    const { f, calls } = fakeFetch(session("member"));
    const s = await getSession(req({ cookie: "tiffin.session_token=abc", "x-api-key": "tfk_x", host: "shop.example.com" }), { url: "http://127.0.0.1:7393/api/auth", fetch: f, organizationId: "o1" });
    expect(s?.organization?.role).toBe("member");
    expect(calls[0]!.url).toBe("http://127.0.0.1:7393/api/auth/tiffin/session?organizationId=o1");
    expect(calls[0]!.headers.get("cookie")).toBe("tiffin.session_token=abc");
    expect(calls[0]!.headers.get("x-api-key")).toBe("tfk_x");
    expect(calls[0]!.headers.get("x-tiffin-host")).toBe("shop.example.com");
  });

  test("no credentials (or only other cookies): no call, no session", async () => {
    const { f, calls } = fakeFetch(session("member"));
    expect(await getSession(req({}), { url: "http://x/api/auth", fetch: f })).toBeNull();
    expect(await getSession(req({ cookie: "theme=dark" }), { url: "http://x/api/auth", fetch: f })).toBeNull();
    expect(calls.length).toBe(0);
  });

  test("no auth endpoint (running away from the box): signed out, with one warning", async () => {
    const saved = { i: process.env.TIFFIN_AUTH_INTERNAL_URL, u: process.env.TIFFIN_AUTH_URL };
    delete process.env.TIFFIN_AUTH_INTERNAL_URL;
    delete process.env.TIFFIN_AUTH_URL;
    const warn = spyOn(console, "warn").mockImplementation(() => {});
    try {
      const { f, calls } = fakeFetch(session("member"));
      expect(await getSession(req({ cookie: tok() }), { fetch: f })).toBeNull();
      expect(await getSession(req({ "x-api-key": "tfk_x" }), { fetch: f })).toBeNull();
      expect((await requireUser(req({ cookie: tok() }), { fetch: f }).catch((e) => e)).status).toBe(401);
      expect(calls.length).toBe(0);
      expect(warn).toHaveBeenCalledTimes(1);
      expect(String(warn.mock.calls[0]![0])).toContain("signed out");
    } finally {
      warn.mockRestore();
      if (saved.i !== undefined) process.env.TIFFIN_AUTH_INTERNAL_URL = saved.i;
      if (saved.u !== undefined) process.env.TIFFIN_AUTH_URL = saved.u;
    }
  });

  test("reads TIFFIN_AUTH_INTERNAL_URL", async () => {
    process.env.TIFFIN_AUTH_INTERNAL_URL = "http://engine:1/api/auth/";
    const { f, calls } = fakeFetch(null);
    expect(await getSession(req({ cookie: tok() }), { fetch: f })).toBeNull();
    expect(calls[0]!.url).toBe("http://engine:1/api/auth/tiffin/session");
    delete process.env.TIFFIN_AUTH_INTERNAL_URL;
  });
});

describe("require*", () => {
  test("401 without a session, 403 below the role, ok at or above", async () => {
    const none = fakeFetch(null).f;
    const err = await requireUser(req({ cookie: tok() }), { url: "http://x", fetch: none }).catch((e) => e);
    expect(err).toBeInstanceOf(AuthError);
    expect(err.status).toBe(401);
    expect(err.toResponse().status).toBe(401);

    const viewer = fakeFetch(session("viewer")).f;
    const low = await requireRole(req({ cookie: tok() }), "member", { url: "http://x", fetch: viewer }).catch((e) => e);
    expect(low.status).toBe(403);
    expect(low.code).toBe("forbidden");
    expect(low.message).toContain("member");

    const noOrg = await requireRole(req({ cookie: tok() }), "viewer", { url: "http://x", fetch: fakeFetch(session(null)).f }).catch((e) => e);
    expect(noOrg.code).toBe("no_organization");

    const admin = await requireRole(req({ cookie: tok() }), "member", { url: "http://x", fetch: fakeFetch(session("admin")).f });
    expect(admin.organization.name).toBe("Acme");
  });
});

describe("withOrg", () => {
  test("node-postgres style: BEGIN, set_config, COMMIT; ROLLBACK on error", async () => {
    const log: string[] = [];
    const client = { query: async (t: string, p?: unknown[]) => void log.push(p ? `${t} ${JSON.stringify(p)}` : t) };
    expect(await withOrg(client, "org_a", async () => 42, "u1")).toBe(42);
    expect(log).toEqual(["BEGIN", `select set_config('app.org_id', $1, true), set_config('app.user_id', $2, true) ["org_a","u1"]`, "COMMIT"]);
    log.length = 0;
    await expect(withOrg(client, "org_a", async () => { throw new Error("boom"); })).rejects.toThrow("boom");
    expect(log.at(-1)).toBe("ROLLBACK");
  });

  test("node-postgres Pool: one client for the whole transaction, then released", async () => {
    const log: string[] = [];
    const client = { query: async (t: string) => void log.push(t), release: () => void log.push("release") };
    const pool = { totalCount: 0, query: async () => { throw new Error("pool.query used"); }, connect: async () => client };
    expect(await withOrg(pool, "org_c", async (tx) => { await tx.query("select 1"); return 7; })).toBe(7);
    expect(log).toEqual(["BEGIN", "select set_config('app.org_id', $1, true), set_config('app.user_id', $2, true)", "select 1", "COMMIT", "release"]);
  });

  test("postgres.js style: sql.begin", async () => {
    const seen: unknown[] = [];
    const tx = (strings: TemplateStringsArray, ...values: unknown[]) => { seen.push(strings.join("?"), values); return Promise.resolve([]); };
    const sql = { begin: async (fn: (t: typeof tx) => Promise<string>) => fn(tx) };
    expect(await withOrg(sql, "org_b", async () => "ok")).toBe("ok");
    expect(seen[1]).toEqual(["org_b", ""]);
  });
});

describe("getUserByEmail / createUser", () => {
  const user = { id: "u9", email: "buyer@example.com", name: "Buyer", image: null, emailVerified: false, banned: false, createdAt: "", updatedAt: "" };

  test("call the engine on the box with the project's host and key, never the public URL", async () => {
    const saved = { ...process.env };
    process.env.TIFFIN_AUTH_URL = "https://shop.example.com/api/auth";
    process.env.TIFFIN_AUTH_INTERNAL_URL = "http://10.0.0.1:7393/api/auth/";
    process.env.TIFFIN_AUTH_HOST = "shop.example.com";
    process.env.TIFFIN_AUTH_KEY = "tak_shop_abc";
    try {
      const { f, calls } = fakeFetch({ user });
      expect(await getUserByEmail("Buyer@example.com", { fetch: f })).toEqual(user);
      const made = fakeFetch({ user, created: true });
      expect(await createUser({ email: "buyer@example.com", name: "Buyer" }, { fetch: made.f })).toEqual({ user, created: true });
      expect(calls[0]!.url).toBe("http://10.0.0.1:7393/api/auth/tiffin/server/find-user");
      expect(made.calls[0]!.url).toBe("http://10.0.0.1:7393/api/auth/tiffin/server/create-user");
      for (const c of [calls[0]!, made.calls[0]!]) {
        expect(c.headers.get("x-tiffin-auth-host")).toBe("shop.example.com");
        expect(c.headers.get("x-tiffin-auth-key")).toBe("tak_shop_abc");
        expect(c.headers.get("cookie")).toBeNull();
      }
      expect(await getUserByEmail("nobody@example.com", { fetch: fakeFetch({ user: null }).f })).toBeNull();
    } finally {
      process.env = saved;
    }
  });

  test("without the box's env they say what's missing; engine errors carry its code", async () => {
    const saved = { ...process.env };
    delete process.env.TIFFIN_AUTH_KEY;
    try {
      const { f, calls } = fakeFetch({ user });
      const err = await getUserByEmail("a@b.co", { url: "http://x/api/auth", host: "shop.example.com", fetch: f }).catch((e) => e);
      expect(err.message).toContain("TIFFIN_AUTH_KEY");
      expect(calls.length).toBe(0);
      const bad = await createUser({ email: "nope" }, { url: "http://x/api/auth", host: "h", key: "k", fetch: fakeFetch({ code: "INVALID_EMAIL", message: "That isn't an email address." }, 422).f }).catch((e) => e);
      expect(bad.message).toContain("That isn't an email address. (422 INVALID_EMAIL)");
    } finally {
      process.env = saved;
    }
  });
});
