import { describe, expect, test } from "bun:test";
import { AuthError, getSession, requireRole, requireUser, roleAtLeast, withOrg } from "../src/auth";

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

  test("no credentials: no call, no session", async () => {
    const { f, calls } = fakeFetch(session("member"));
    expect(await getSession(req({}), { url: "http://x/api/auth", fetch: f })).toBeNull();
    expect(calls.length).toBe(0);
  });

  test("reads TIFFIN_AUTH_INTERNAL_URL", async () => {
    process.env.TIFFIN_AUTH_INTERNAL_URL = "http://engine:1/api/auth/";
    const { f, calls } = fakeFetch(null);
    expect(await getSession(req({ cookie: "x=1" }), { fetch: f })).toBeNull();
    expect(calls[0]!.url).toBe("http://engine:1/api/auth/tiffin/session");
    delete process.env.TIFFIN_AUTH_INTERNAL_URL;
  });
});

describe("require*", () => {
  test("401 without a session, 403 below the role, ok at or above", async () => {
    const none = fakeFetch(null).f;
    const err = await requireUser(req({ cookie: "x=1" }), { url: "http://x", fetch: none }).catch((e) => e);
    expect(err).toBeInstanceOf(AuthError);
    expect(err.status).toBe(401);
    expect(err.toResponse().status).toBe(401);

    const viewer = fakeFetch(session("viewer")).f;
    const low = await requireRole(req({ cookie: "x=1" }), "member", { url: "http://x", fetch: viewer }).catch((e) => e);
    expect(low.status).toBe(403);
    expect(low.code).toBe("forbidden");
    expect(low.message).toContain("member");

    const noOrg = await requireRole(req({ cookie: "x=1" }), "viewer", { url: "http://x", fetch: fakeFetch(session(null)).f }).catch((e) => e);
    expect(noOrg.code).toBe("no_organization");

    const admin = await requireRole(req({ cookie: "x=1" }), "member", { url: "http://x", fetch: fakeFetch(session("admin")).f });
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

  test("Bun.sql / postgres.js style: sql.begin", async () => {
    const seen: unknown[] = [];
    const tx = (strings: TemplateStringsArray, ...values: unknown[]) => { seen.push(strings.join("?"), values); return Promise.resolve([]); };
    const sql = { begin: async (fn: (t: typeof tx) => Promise<string>) => fn(tx) };
    expect(await withOrg(sql, "org_b", async () => "ok")).toBe("ok");
    expect(seen[1]).toEqual(["org_b", ""]);
  });
});
