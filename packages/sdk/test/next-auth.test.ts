// tiffin-sdk/next/auth with next/headers and next/navigation stubbed: what a
// page, a Server Action and proxy.ts see.
import { beforeEach, describe, expect, mock, test } from "bun:test";
import { NextRequest } from "next/server";
import { fakeEngine } from "./engine-fake";

class Interrupt extends Error {
  constructor(
    public kind: string,
    public to?: string,
  ) {
    super(kind);
  }
}

let reqHeaders = new Headers();
let jar = new Map<string, string>();
let sets: { name: string; value: string; opts: Record<string, unknown> }[] = [];
let readOnly = false;

mock.module("server-only", () => ({}));
mock.module("next/headers", () => ({
  headers: async () => reqHeaders,
  cookies: async () => ({
    getAll: () => [...jar].map(([name, value]) => ({ name, value })),
    set: (name: string, value: string, opts: Record<string, unknown>) => {
      if (readOnly) throw new Error("Cookies can only be modified in a Server Action or Route Handler.");
      sets.push({ name, value, opts });
      if (opts.maxAge === 0) jar.delete(name);
      else jar.set(name, value);
    },
  }),
}));
mock.module("next/navigation", () => ({
  redirect: (to: string) => {
    throw new Interrupt("redirect", to);
  },
  forbidden: () => {
    throw new Interrupt("forbidden");
  },
  unauthorized: () => {
    throw new Interrupt("unauthorized");
  },
}));

const auth = await import("../src/next/auth");
const caught = (p: Promise<unknown>) => p.then(() => null, (e) => e as Interrupt);

let n = 0;
async function browser(engine: Awaited<ReturnType<typeof fakeEngine>>, signedIn: boolean, extra: Record<string, string> = {}) {
  process.env.TIFFIN_AUTH_INTERNAL_URL = engine.url;
  globalThis.fetch = engine.fetch;
  jar = new Map();
  sets = [];
  readOnly = false;
  if (signedIn) {
    const t = `tok-next-${++n}`;
    jar.set("__Secure-tiffin.session_token", `${t}.sig+/=`);
    jar.set("__Secure-tiffin.session_data", await engine.jwt(t));
  }
  reqHeaders = new Headers({ host: "shop.example.com", "x-forwarded-for": "203.0.113.9", "x-forwarded-proto": "https", "user-agent": "test", ...extra });
}

describe("reading the session", () => {
  test("getSession and currentUser: plain data, no tokens", async () => {
    const e = await fakeEngine(null);
    await browser(e, true);
    const s = await auth.getSession();
    expect(s).toEqual({
      user: { id: "u1", email: "ana@example.com", name: "Ana", image: null, emailVerified: true },
      organization: { id: "o1", name: "Acme", slug: "acme", role: "owner" },
      sessionId: "s1",
      expiresAt: expect.any(String),
      via: "session",
    });
    expect((await auth.currentUser())?.email).toBe("ana@example.com");
    expect(e.sessionCalls()).toBe(0);
  });

  test("a refreshed cookie from the engine reaches the browser where it can, and is skipped in a page", async () => {
    const e = await fakeEngine({ user: { id: "u2", email: "b@example.com", name: "B", emailVerified: true }, session: { id: "s2", expiresAt: "x", activeOrganizationId: null }, organization: null, via: "session", apiKey: null });
    e.setCookie = ["__Secure-tiffin.session_data=fresh.jwt; Max-Age=60; Path=/; HttpOnly; Secure; SameSite=Lax"];
    await browser(e, false);
    jar.set("__Secure-tiffin.session_token", "only-token.sig");
    expect((await auth.getSession())?.user.email).toBe("b@example.com");
    expect(sets[0]).toEqual({ name: "__Secure-tiffin.session_data", value: "fresh.jwt", opts: { maxAge: 60, path: "/", httpOnly: true, secure: true, sameSite: "lax" } });

    await browser(e, false);
    jar.set("__Secure-tiffin.session_token", "other-token.sig");
    readOnly = true; // a Server Component
    expect((await auth.getSession())?.user.email).toBe("b@example.com");
  });

  test("verifySession: signed out → sign-in with ?next= from proxy.ts, or 401", async () => {
    const e = await fakeEngine(null);
    await browser(e, false, { "x-tiffin-path": "/dashboard/settings?tab=2" });
    expect(await caught(auth.verifySession())).toMatchObject({ kind: "redirect", to: "/sign-in?next=%2Fdashboard%2Fsettings%3Ftab%3D2" });
    expect(await caught(auth.verifySession({ signIn: "/login" }))).toMatchObject({ to: "/login?next=%2Fdashboard%2Fsettings%3Ftab%3D2" });
    expect(await caught(auth.verifySession({ signIn: false }))).toMatchObject({ kind: "unauthorized" });
    await browser(e, false, { "x-tiffin-path": "//evil.example/x" });
    expect(await caught(auth.verifySession())).toMatchObject({ to: "/sign-in" });
    expect(e.calls.length).toBe(0); // no session cookie: nothing to ask
  });

  test("requireRole: 403 below the role", async () => {
    const e = await fakeEngine(null);
    await browser(e, true);
    expect((await auth.requireRole("admin")).organization.role).toBe("owner");
    const t = "tok-member";
    jar.set("__Secure-tiffin.session_token", `${t}.sig`);
    jar.set("__Secure-tiffin.session_data", await e.jwt(t, { org: { id: "o1", name: "Acme", slug: "acme", role: "member", memberRole: "member" } }));
    expect(await caught(auth.requireRole("admin"))).toMatchObject({ kind: "forbidden" });
    jar.set("__Secure-tiffin.session_data", await e.jwt(t, { org: null }));
    expect(await caught(auth.requireRole("viewer"))).toMatchObject({ kind: "forbidden" });
  });
});

describe("Server Actions", () => {
  test("signIn calls the engine for the browser, sets its cookies, and goes to ?next=", async () => {
    const e = await fakeEngine({ redirect: false, token: "t", user: {} });
    e.setCookie = [
      "__Secure-tiffin.session_token=newtok.c2ln%2B%3D; Max-Age=2592000; Path=/; HttpOnly; Secure; SameSite=Lax",
      "__Secure-tiffin.session_data=eyJ.x.y; Max-Age=60; Path=/; HttpOnly; Secure; SameSite=Lax",
    ];
    await browser(e, false, { origin: "https://shop.example.com" });
    const form = new FormData();
    form.set("email", "ana@example.com");
    form.set("password", "pw-123456");
    form.set("captcha", "c0ffee");
    form.set("next", "/dashboard");
    expect(await caught(auth.signIn(form, { redirectTo: "/" }))).toMatchObject({ kind: "redirect", to: "/dashboard" });
    const call = e.calls[0]!;
    expect(call.url).toBe(`${e.url}/sign-in/email`);
    expect(call.headers.get("x-captcha-response")).toBe("c0ffee");
    expect(call.headers.get("x-tiffin-host")).toBe("shop.example.com");
    expect(call.headers.get("origin")).toBe("https://shop.example.com");
    expect(call.headers.get("x-forwarded-for")).toBe("203.0.113.9");
    expect(jar.get("__Secure-tiffin.session_token")).toBe("newtok.c2ln+=");
    expect(sets[0]!.opts).toEqual({ maxAge: 2592000, path: "/", httpOnly: true, secure: true, sameSite: "lax" });

    // An outside next is ignored; redirectTo applies.
    await browser(e, false);
    form.set("next", "https://evil.example/");
    expect(await caught(auth.signIn(form, { redirectTo: "/home" }))).toMatchObject({ to: "/home" });
  });

  test("signIn and signUp report failures for the form; sign-up may need email confirmation", async () => {
    const e = await fakeEngine({ code: "INVALID_EMAIL_OR_PASSWORD", message: "Invalid email or password" });
    const realFetch = e.fetch;
    globalThis.fetch = (async (u: string, i?: RequestInit) => {
      const r = await realFetch(u, i);
      return new Response(r.body, { status: 401, headers: r.headers });
    }) as typeof fetch;
    process.env.TIFFIN_AUTH_INTERNAL_URL = e.url;
    expect(await auth.signIn({ email: "a@b.co", password: "x" })).toEqual({ ok: false, code: "INVALID_EMAIL_OR_PASSWORD", message: "Invalid email or password" });

    const e2 = await fakeEngine({ token: null, user: { id: "u9" } });
    await browser(e2, false);
    expect(await auth.signUp({ email: "new@example.com", password: "pw-123456" }, { redirectTo: "/welcome" })).toEqual({ ok: true, signedIn: false });
    expect(e2.calls[0]!.url).toBe(`${e2.url}/sign-up/email`);
  });

  test("signOut ends the session on the engine and clears the cookies", async () => {
    const e = await fakeEngine({ success: true });
    e.setCookie = ["__Secure-tiffin.session_token=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Lax", "__Secure-tiffin.session_data=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Lax"];
    await browser(e, true);
    expect(await caught(auth.signOut({ redirectTo: "/" }))).toMatchObject({ kind: "redirect", to: "/" });
    expect(e.calls[0]!.url).toBe(`${e.url}/sign-out`);
    expect(e.calls[0]!.headers.get("cookie")).toContain("__Secure-tiffin.session_token=");
    expect(jar.size).toBe(0);
  });
});

describe("authProxy", () => {
  const proxy = auth.authProxy({ protect: ["/dashboard/:path*", "/api/private/:path*", "/team/:id"] });
  const call = (path: string, cookie?: string) => proxy(new NextRequest(`https://shop.example.com${path}`, { headers: cookie ? { cookie } : {} }));

  test("redirects signed-out visitors of protected pages, with ?next=", () => {
    for (const p of ["/dashboard", "/dashboard/", "/dashboard/a/b?x=1", "/team/7"]) {
      const r = call(p);
      expect([p, r.status]).toEqual([p, 307]);
      const loc = new URL(r.headers.get("location")!);
      expect(loc.pathname).toBe("/sign-in");
      expect(loc.searchParams.get("next")).toBe(p);
    }
    expect(call("/api/private/x").status).toBe(401);
  });

  test("lets everything else through, and tells pages their path", () => {
    for (const p of ["/", "/dashboards", "/team", "/team/7/edit", "/sign-in"]) {
      const r = call(p);
      expect([p, r.headers.get("x-middleware-next")]).toEqual([p, "1"]);
    }
    const ok = call("/dashboard?tab=1", "__Secure-tiffin.session_token=abc.sig");
    expect(ok.headers.get("x-middleware-next")).toBe("1");
    expect(ok.headers.get("x-middleware-request-x-tiffin-path")).toBe("/dashboard?tab=1");
  });
});
