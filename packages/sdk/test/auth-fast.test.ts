// getSession's fast path: the engine's signed session cookie, verified here,
// and a 5-second memory of engine answers.
import { describe, expect, test } from "bun:test";
import { getSession, sessionFor } from "../src/auth";
import { fakeEngine } from "./engine-fake";

const engineAnswer = {
  user: { id: "u1", email: "ana@example.com", name: "Ana", emailVerified: true, createdAt: "", updatedAt: "" },
  session: { id: "s1", expiresAt: "", activeOrganizationId: "o1" },
  organization: { id: "o1", name: "Acme", slug: "acme", role: "member", memberRole: "member" },
  via: "session",
  apiKey: null,
};

let n = 0;
const token = () => `tok${++n}`;
const req = (cookie: string, extra: Record<string, string> = {}) => new Request("https://shop.example.com/x", { headers: { cookie, host: "shop.example.com", ...extra } });
const cookies = (t: string, jwt?: string) => `__Secure-tiffin.session_token=${t}.c2lnbmF0dXJl%2B%3D` + (jwt ? `; __Secure-tiffin.session_data=${jwt}` : "");

describe("signed session cookie", () => {
  test("is read without asking the engine, and strips nothing a page needs", async () => {
    const e = await fakeEngine(engineAnswer);
    const t = token();
    const opts = { url: e.url, fetch: e.fetch };
    const s = await getSession(req(cookies(t, await e.jwt(t))), opts);
    expect(s?.user.email).toBe("ana@example.com");
    expect(s?.user.banned).toBeUndefined();
    expect(s?.organization).toEqual({ id: "o1", name: "Acme", slug: "acme", role: "owner", memberRole: "owner" });
    expect(s?.session).toEqual({ id: "s1", expiresAt: expect.any(String), activeOrganizationId: "o1" });
    expect(s?.via).toBe("session");
    expect(e.sessionCalls()).toBe(0);
    // The JWKS is fetched once, from the engine, for this app's host.
    await getSession(req(cookies(t, await e.jwt(t))), opts);
    const jwks = e.calls.filter((c) => c.url.endsWith("/jwks"));
    expect(jwks.length).toBe(1);
    expect(jwks[0]!.url).toBe(`${e.url}/jwks`);
    expect(jwks[0]!.headers.get("x-tiffin-host")).toBe("shop.example.com");
  });

  test("Better Auth's chunked cookies (name.0, name.1) work too", async () => {
    const e = await fakeEngine(engineAnswer);
    const t = token();
    const jwt = await e.jwt(t);
    const c = `__Secure-tiffin.session_token=${t}.sig; __Secure-tiffin.session_data.0=${jwt.slice(0, 100)}; __Secure-tiffin.session_data.1=${jwt.slice(100)}`;
    expect((await getSession(req(c), { url: e.url, fetch: e.fetch }))?.organization?.role).toBe("owner");
    expect(e.sessionCalls()).toBe(0);
  });

  test("falls back to the engine when it can't be trusted or doesn't answer the question", async () => {
    const e = await fakeEngine(engineAnswer);
    const opts = { url: e.url, fetch: e.fetch };
    const cases: [string, (t: string) => Promise<string>, Record<string, unknown>?][] = [
      ["expired", (t) => e.jwt(t, { ttl: -1 })],
      ["another session's", (t) => e.jwt(t, { sid: "someone-else" })],
      ["unknown key", (t) => e.jwt(t, { kid: "k9" })],
      ["tampered", async (t) => (await e.jwt(t)).replace(/\.[^.]+$/, ".AAAA")],
      ["forged payload", async (t) => {
        const [h, , s] = (await e.jwt(t)).split(".");
        const [, p] = (await e.jwt(t, { org: { id: "o1", name: "Acme", slug: "acme", role: "owner", memberRole: "owner" } })).split(".");
        return `${h}.${p!.slice(0, -2)}AA.${s}`;
      }],
      ["another organization asked for", (t) => e.jwt(t), { organizationId: "o2" }],
      ["fresh asked for", (t) => e.jwt(t), { fresh: true }],
    ];
    for (const [name, make, extra] of cases) {
      const t = token();
      const before = e.sessionCalls();
      const s = await getSession(req(cookies(t, await make(t))), { ...opts, ...extra });
      expect([name, e.sessionCalls() - before]).toEqual([name, 1]);
      expect([name, s?.organization?.role]).toEqual([name, "member"]); // the engine's answer
    }
  });

  test("API keys always go to the engine", async () => {
    const e = await fakeEngine({ ...engineAnswer, via: "api-key" });
    const t = token();
    const s = await getSession(req(cookies(t, await e.jwt(t)), { "x-api-key": "tfk_1" }), { url: e.url, fetch: e.fetch });
    expect(s?.via).toBe("api-key");
    expect(e.sessionCalls()).toBe(1);
  });
});

describe("engine answers", () => {
  test("are remembered for 5 seconds per token; fresh skips the memory", async () => {
    const e = await fakeEngine(engineAnswer);
    const opts = { url: e.url, fetch: e.fetch };
    const t = token();
    await getSession(req(cookies(t)), opts);
    await getSession(req(cookies(t)), opts);
    expect(e.sessionCalls()).toBe(1);
    await getSession(req(cookies(t)), { ...opts, organizationId: "o2" }); // another question
    expect(e.sessionCalls()).toBe(2);
    await getSession(req(cookies(t)), { ...opts, fresh: true });
    expect(e.sessionCalls()).toBe(3);
    await getSession(req(cookies(token())), opts); // another session
    expect(e.sessionCalls()).toBe(4);
    const realNow = Date.now;
    Date.now = () => realNow() + 6_000;
    try {
      await getSession(req(cookies(t)), opts);
      expect(e.sessionCalls()).toBe(5);
    } finally {
      Date.now = realNow;
    }
  });

  test("hand back the engine's Set-Cookie (a refreshed session cookie)", async () => {
    const e = await fakeEngine(engineAnswer);
    e.setCookie = ["__Secure-tiffin.session_data=new; Max-Age=60; Path=/; HttpOnly; Secure; SameSite=Lax"];
    const r = await sessionFor(req(cookies(token())).headers, { url: e.url, fetch: e.fetch });
    expect(r.session?.user.id).toBe("u1");
    expect(r.setCookie).toEqual(e.setCookie);
  });
});
