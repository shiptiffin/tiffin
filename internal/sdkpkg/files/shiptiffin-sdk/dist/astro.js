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
import { roleAtLeast, sessionFor } from "./auth.js";
import { dto, localPath, pattern } from "./session-view.js";
/** Each request's session, read once (keyed by its locals). */
const reads = new WeakMap();
async function readSession(context, opts = {}) {
    const o = {};
    if (opts.organizationId)
        o.organizationId = opts.organizationId;
    if (opts.fresh)
        o.fresh = true;
    try {
        const { session, setCookie } = await sessionFor(context.request.headers, o);
        return { session: session && dto(session), setCookie };
    }
    catch (err) {
        // The engine is down or answered an error: signed out, never signed in.
        console.error(`@shiptiffin/sdk/astro: couldn't read the session, treating the request as signed out: ${err instanceof Error ? err.message : String(err)}`);
        return { session: null, setCookie: [] };
    }
}
function read(context, opts = {}) {
    if (opts.organizationId || opts.fresh)
        return readSession(context, opts);
    let p = reads.get(context.locals);
    if (!p) {
        p = readSession(context);
        reads.set(context.locals, p);
    }
    return p;
}
const isApi = (context) => context.url.pathname === "/api" || context.url.pathname.startsWith("/api/");
function signedOut(context, signIn) {
    if (signIn === false || isApi(context)) {
        return Response.json({ code: "unauthenticated", message: "Sign in first." }, { status: 401, headers: { "cache-control": "no-store" } });
    }
    const to = new URL(signIn, context.url);
    const next = localPath(context.url.pathname + context.url.search);
    if (next)
        to.searchParams.set("next", next);
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
export function authMiddleware(opts = {}) {
    const rules = (opts.protect ?? []).map(pattern);
    const signIn = opts.signIn ?? "/sign-in";
    return async (context, next) => {
        const locals = context.locals;
        locals.user = null;
        locals.organization = null;
        locals.session = null;
        if (context.isPrerendered)
            return next();
        const { session, setCookie } = await read(context);
        locals.session = session;
        locals.user = session?.user ?? null;
        locals.organization = session?.organization ?? null;
        if (!session && rules.some((r) => r.test(context.url.pathname)))
            return signedOut(context, signIn);
        const res = await next();
        if (setCookie.length && !context.request.headers.has("x-api-key")) {
            try {
                for (const c of setCookie)
                    res.headers.append("set-cookie", c);
            }
            catch {
                // Immutable headers (Response.redirect): the next engine call refreshes the cookie.
            }
        }
        return res;
    };
}
/** The signed-in session, or null. Once per request (after authMiddleware, free). */
export async function getSession(context, opts = {}) {
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
export async function requireSession(context, opts = {}) {
    const s = await getSession(context, opts);
    return s ?? signedOut(context, opts.signIn ?? "/sign-in");
}
/**
 * The signed-in session with at least `role` (viewer < member < admin <
 * owner) in the active or given organization, or the Response to send
 * instead: signed out as requireSession; no organization or a lower role, 403.
 */
export async function requireRole(context, role, opts = {}) {
    const s = await requireSession(context, opts);
    if (s instanceof Response)
        return s;
    if (!s.organization || !roleAtLeast(s.organization.role, role)) {
        const message = s.organization
            ? `This needs the ${role} role in ${s.organization.name}; you have ${s.organization.role ?? "none"}.`
            : "Pick an organization first (or pass organizationId).";
        const code = s.organization ? "forbidden" : "no_organization";
        if (opts.signIn === false || isApi(context))
            return Response.json({ code, message }, { status: 403, headers: { "cache-control": "no-store" } });
        return new Response(message, { status: 403, headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" } });
    }
    return s;
}
