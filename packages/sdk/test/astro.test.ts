// @shiptiffin/sdk/astro: what Astro middleware, endpoints and pages see,
// against the fake engine (no astro needed).
import { afterEach, describe, expect, spyOn, test } from "bun:test";
import { authMiddleware, getSession, requireRole, requireSession, type AstroAuthContext, type AuthLocals } from "../src/astro";
import { fakeEngine, type FakeOrg } from "./engine-fake";

let n = 0;

// The helpers use the global fetch and the box's env: put both back for the other test files.
const realFetch = globalThis.fetch;
const realURL = process.env.TIFFIN_AUTH_INTERNAL_URL;
afterEach(() => {
  globalThis.fetch = realFetch;
  if (realURL === undefined) delete process.env.TIFFIN_AUTH_INTERNAL_URL;
  else process.env.TIFFIN_AUTH_INTERNAL_URL = realURL;
});

/** A context for one request to `path`, as Astro builds it (locals start empty). */
async function context(engine: Awaited<ReturnType<typeof fakeEngine>> | null, path: string, o: { signedIn?: boolean; prerendered?: boolean; org?: FakeOrg } = {}) {
  if (engine) {
    process.env.TIFFIN_AUTH_INTERNAL_URL = engine.url;
    globalThis.fetch = engine.fetch;
  }
  const headers = new Headers({ host: "shop.example.com", "x-forwarded-proto": "https" });
  if (o.signedIn && engine) {
    const t = `tok-astro-${++n}`;
    headers.set("cookie", `__Host-tiffin.session_token=${t}.sig; __Host-tiffin.session_data=${await engine.jwt(t, o.org === undefined ? {} : { org: o.org })}`);
  }
  const url = new URL(path, "https://shop.example.com");
  return { request: new Request(url, { headers }), url, locals: {} as AuthLocals, isPrerendered: !!o.prerendered } satisfies AstroAuthContext;
}

const ok = async () => new Response("page", { headers: { "content-type": "text/html" } });

describe("authMiddleware", () => {
  test("reads the session once into locals; signed-in visitors reach protected pages", async () => {
    const e = await fakeEngine();
    const mw = authMiddleware({ protect: ["/app/:path*"] });
    const ctx = await context(e, "/app/drills/", { signedIn: true });
    const res = await mw(ctx, ok);
    expect(res.status).toBe(200);
    expect(ctx.locals.user).toEqual({ id: "u1", email: "ana@example.com", name: "Ana", image: null, emailVerified: true });
    expect(ctx.locals.organization).toEqual({ id: "o1", name: "Acme", slug: "acme", role: "owner" });
    expect(ctx.locals.session?.sessionId).toBe("s1");
    // An endpoint or page asking again in the same request: no second read.
    const calls = e.calls.length;
    expect((await getSession(ctx))?.user.email).toBe("ana@example.com");
    expect(await requireSession(ctx)).toBe(ctx.locals.session!);
    expect(e.calls.length).toBe(calls);
  });

  test("signed out: pages redirect to sign-in with ?next=, /api/ answers 401, the rest pass", async () => {
    const e = await fakeEngine(null);
    const mw = authMiddleware({ protect: ["/app/:path*", "/api/private/:path*"], signIn: "/login/" });
    const page = await mw(await context(e, "/app/exam?grade=2"), ok);
    expect(page.status).toBe(302);
    expect(page.headers.get("location")).toBe("/login/?next=%2Fapp%2Fexam%3Fgrade%3D2");
    const api = await mw(await context(e, "/api/private/notes"), ok);
    expect(api.status).toBe(401);
    expect(await api.json()).toEqual({ code: "unauthenticated", message: "Sign in first." });
    const open = await context(e, "/pricing/");
    expect((await mw(open, ok)).status).toBe(200);
    expect(open.locals).toEqual({ user: null, organization: null, session: null });
    expect(e.sessionCalls()).toBe(0); // no cookie: the engine isn't asked
  });

  test("prerendered pages are left alone (no request at build time)", async () => {
    const e = await fakeEngine();
    const ctx = await context(e, "/app/", { prerendered: true });
    let ran = false;
    const res = await authMiddleware({ protect: ["/app/:path*"] })(ctx, async () => ((ran = true), new Response("static")));
    expect(ran).toBe(true);
    expect(res.status).toBe(200);
    expect(ctx.locals.user).toBeNull();
    expect(e.calls.length).toBe(0);
  });

  test("passes the engine's refreshed cookie on; an engine error is signed out, never signed in", async () => {
    const e = await fakeEngine({
      user: { id: "u2", email: "bea@example.com", name: "Bea", emailVerified: true, createdAt: "", updatedAt: "" },
      session: { id: "s2", expiresAt: "", activeOrganizationId: null },
      organization: null,
      via: "session",
      apiKey: null,
    });
    e.setCookie = ["__Host-tiffin.session_data=fresh; Path=/; Secure; HttpOnly"];
    const ctx = await context(e, "/app/");
    ctx.request.headers.set("cookie", `__Host-tiffin.session_token=tok-astro-${++n}.sig`);
    const res = await authMiddleware({ protect: ["/app/:path*"] })(ctx, ok);
    expect(ctx.locals.user?.email).toBe("bea@example.com");
    expect(res.headers.getSetCookie()).toEqual(["__Host-tiffin.session_data=fresh; Path=/; Secure; HttpOnly"]);

    const down = await fakeEngine();
    down.fetch = (async (url: string) => (url.endsWith("/jwks") ? Response.json({ keys: [] }) : new Response("boom", { status: 502 }))) as unknown as typeof fetch;
    const err = spyOn(console, "error").mockImplementation(() => {});
    try {
      const c2 = await context(down, "/app/");
      c2.request.headers.set("cookie", `__Host-tiffin.session_token=tok-astro-${++n}.sig`);
      const r2 = await authMiddleware({ protect: ["/app/:path*"] })(c2, ok);
      expect(r2.status).toBe(302);
      expect(c2.locals.user).toBeNull();
      expect(err).toHaveBeenCalled();
    } finally {
      err.mockRestore();
    }
  });
});

describe("requireSession / requireRole", () => {
  test("answer what an endpoint or page should send", async () => {
    const e = await fakeEngine(null);
    const out = await requireSession(await context(e, "/api/notes"));
    expect(out instanceof Response && out.status).toBe(401);
    const page = await requireSession(await context(e, "/account/"));
    expect(page instanceof Response && page.headers.get("location")).toBe("/sign-in?next=%2Faccount%2F");
    const strict = await requireSession(await context(e, "/account/"), { signIn: false });
    expect(strict instanceof Response && strict.status).toBe(401);

    const member = await fakeEngine();
    const viewer = await context(member, "/api/admin", { signedIn: true, org: { id: "o1", name: "Acme", slug: "acme", role: "viewer", memberRole: "viewer" } });
    const low = await requireRole(viewer, "admin");
    expect(low instanceof Response && low.status).toBe(403);
    expect(low instanceof Response && (await low.json()).code).toBe("forbidden");
    const none = await requireRole(await context(member, "/settings/", { signedIn: true, org: null }), "viewer");
    expect(none instanceof Response && none.status).toBe(403);
    expect(none instanceof Response && (await none.text())).toContain("organization");
    const owner = await requireRole(await context(member, "/api/admin", { signedIn: true }), "admin");
    expect(owner instanceof Response ? owner.status : owner.organization.role).toBe("owner");
  });
});
