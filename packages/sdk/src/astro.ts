/**
 * `@shiptiffin/sdk/astro`: sign-in for Astro apps on Tiffin (pages rendered on
 * demand, any adapter). It needs nothing from `astro` at runtime.
 *
 *   // src/middleware.ts: reads the session once per request into Astro.locals
 *   import { authMiddleware } from "@shiptiffin/sdk/astro";
 *   export const onRequest = authMiddleware({ protect: ["/app/:path*", "/api/private/:path*"] });
 *
 *   // src/env.d.ts
 *   declare namespace App {
 *     interface Locals extends import("@shiptiffin/sdk/astro").AuthLocals {}
 *   }
 *
 *   // src/pages/api/notes.ts
 *   export const GET: APIRoute = async (context) => {
 *     const s = await requireRole(context, "member");
 *     if (s instanceof Response) return s; // 401 or 403
 *     return Response.json(await notesFor(s.organization.id));
 *   };
 *
 * Signed out on a protected page: a redirect to the sign-in page with
 * ?next=<the page>; on a protected path under /api/: 401. The session comes
 * from the engine's signed cookie (see getSession in @shiptiffin/sdk/auth for
 * how fresh that is). Prerendered pages skip all of it: they have no request.
 */
import { roleAtLeast, sessionFor, type Role, type SessionOptions } from "./auth";
import { dto, localPath, pattern, type Organization, type Session, type User } from "./session-view";

export type { Role } from "./auth";
export type { Organization, Session, User } from "./session-view";

/** What authMiddleware puts in Astro.locals. Extend App.Locals with it. */
export interface AuthLocals {
  /** The signed-in user, or null. */
  user: User | null;
  /** The active organization and the user's role in it, or null. */
  organization: Organization | null;
  session: Session | null;
}

/**
 * The part of Astro's context (APIContext in middleware and endpoints, the
 * Astro global in pages) these helpers use.
 */
export type AstroAuthContext = { request: Request; url: URL; locals: object; isPrerendered?: boolean };

export type SessionOpts = {
  /** Check this organization instead of the active one. */
  organizationId?: string;
  /** Ask the engine now instead of trusting the signed cookie (up to 60 s old). For sensitive actions. */
  fresh?: boolean;
};

type Read = { session: Session | null; setCookie: string[] };

/** Each request's session, read once (keyed by its locals). */
const reads = new WeakMap<object, Promise<Read>>();

async function readSession(context: AstroAuthContext, opts: SessionOpts = {}): Promise<Read> {
  const o: SessionOptions = {};
  if (opts.organizationId) o.organizationId = opts.organizationId;
  if (opts.fresh) o.fresh = true;
  try {
    const { session, setCookie } = await sessionFor(context.request.headers, o);
    return { session: session && dto(session), setCookie };
  } catch (err) {
    // The engine is down or answered an error: signed out, never signed in.
    console.error(`@shiptiffin/sdk/astro: couldn't read the session, treating the request as signed out: ${err instanceof Error ? err.message : String(err)}`);
    return { session: null, setCookie: [] };
  }
}

function read(context: AstroAuthContext, opts: SessionOpts = {}): Promise<Read> {
  if (opts.organizationId || opts.fresh) return readSession(context, opts);
  let p = reads.get(context.locals);
  if (!p) {
    p = readSession(context);
    reads.set(context.locals, p);
  }
  return p;
}

/**
 * The forms of a request's path a protect rule is checked against: as sent,
 * and percent-decoded with repeated slashes folded, which is how Astro's
 * router reads it (so "/%61pp/x" or "//app/x" can't reach /app/x
 * unprotected). A path that doesn't decode counts as protected.
 */
function guarded(rules: RegExp[], pathname: string): boolean {
  let decoded: string;
  try {
    decoded = decodeURI(pathname).replace(/\/{2,}/g, "/");
  } catch {
    return rules.length > 0;
  }
  return rules.some((r) => r.test(pathname) || r.test(decoded));
}

const isApi = (context: AstroAuthContext) => context.url.pathname === "/api" || context.url.pathname.startsWith("/api/");

function signedOut(context: AstroAuthContext, signIn: string | false): Response {
  if (signIn === false || isApi(context)) {
    return Response.json({ code: "unauthenticated", message: "Sign in first." }, { status: 401, headers: { "cache-control": "no-store" } });
  }
  const to = new URL(signIn, context.url);
  const next = localPath(context.url.pathname + context.url.search);
  if (next) to.searchParams.set("next", next);
  return new Response(null, { status: 302, headers: { location: to.pathname + to.search, "cache-control": "no-store" } });
}

/**
 * Astro middleware that reads the session once per request into
 * `context.locals` (`user`, `organization`, `session`), and keeps signed-out
 * visitors out of the `protect` paths (Next.js matcher syntax:
 * "/app/:path*"). Pages under them redirect to `signIn` (default "/sign-in")
 * with ?next=<the page>; paths under /api/ answer 401. A refreshed session
 * cookie from the engine is passed on to the browser.
 *
 * With other middleware, put it first: `sequence(authMiddleware(...), yours)`.
 */
export function authMiddleware(opts: { protect?: string[]; signIn?: string } = {}) {
  const rules = (opts.protect ?? []).map(pattern);
  const signIn = opts.signIn ?? "/sign-in";
  return async (context: AstroAuthContext, next: () => Promise<Response>): Promise<Response> => {
    const locals = context.locals as AuthLocals;
    locals.user = null;
    locals.organization = null;
    locals.session = null;
    if (context.isPrerendered) return next();
    const { session, setCookie } = await read(context);
    locals.session = session;
    locals.user = session?.user ?? null;
    locals.organization = session?.organization ?? null;
    if (!session && guarded(rules, context.url.pathname)) return signedOut(context, signIn);
    const res = await next();
    if (setCookie.length && !context.request.headers.has("x-api-key")) {
      try {
        for (const c of setCookie) res.headers.append("set-cookie", c);
      } catch {
        // Immutable headers (Response.redirect): the next engine call refreshes the cookie.
      }
    }
    return res;
  };
}

/** The signed-in session, or null. Once per request (after authMiddleware, free). */
export async function getSession(context: AstroAuthContext, opts: SessionOpts = {}): Promise<Session | null> {
  return (await read(context, opts)).session;
}

/**
 * The signed-in session, or the Response to send instead: under /api/ (or
 * with `signIn: false`) a 401, otherwise a redirect to the sign-in page
 * (default "/sign-in") with ?next=<this page>.
 *
 *   const s = await requireSession(Astro);
 *   if (s instanceof Response) return s;
 */
export async function requireSession(context: AstroAuthContext, opts: SessionOpts & { signIn?: string | false } = {}): Promise<Session | Response> {
  const s = await getSession(context, opts);
  return s ?? signedOut(context, opts.signIn ?? "/sign-in");
}

/**
 * The signed-in session with at least `role` (viewer < member < admin <
 * owner) in the active or given organization, or the Response to send
 * instead: signed out as requireSession; no organization or a lower role, 403.
 */
export async function requireRole(
  context: AstroAuthContext,
  role: Role,
  opts: SessionOpts & { signIn?: string | false } = {},
): Promise<(Session & { organization: Organization }) | Response> {
  const s = await requireSession(context, opts);
  if (s instanceof Response) return s;
  if (!s.organization || !roleAtLeast(s.organization.role, role)) {
    const message = s.organization
      ? `This needs the ${role} role in ${s.organization.name}; you have ${s.organization.role ?? "none"}.`
      : "Pick an organization first (or pass organizationId).";
    const code = s.organization ? "forbidden" : "no_organization";
    if (opts.signIn === false || isApi(context)) return Response.json({ code, message }, { status: 403, headers: { "cache-control": "no-store" } });
    return new Response(message, { status: 403, headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" } });
  }
  return s as Session & { organization: Organization };
}
