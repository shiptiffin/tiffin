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
import { type Role } from "./auth.js";
import { type Organization, type Session, type User } from "./session-view.js";
export type { Role } from "./auth.js";
export type { Organization, Session, User } from "./session-view.js";
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
export type AstroAuthContext = {
    request: Request;
    url: URL;
    locals: object;
    isPrerendered?: boolean;
};
export type SessionOpts = {
    /** Check this organization instead of the active one. */
    organizationId?: string;
    /** Ask the engine now instead of trusting the signed cookie (up to 60 s old). For sensitive actions. */
    fresh?: boolean;
};
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
export declare function authMiddleware(opts?: {
    protect?: string[];
    signIn?: string;
}): (context: AstroAuthContext, next: () => Promise<Response>) => Promise<Response>;
/** The signed-in session, or null. Once per request (after authMiddleware, free). */
export declare function getSession(context: AstroAuthContext, opts?: SessionOpts): Promise<Session | null>;
/**
 * The signed-in session, or the Response to send instead: under /api/ (or
 * with `signIn: false`) a 401, otherwise a redirect to the sign-in page
 * (default "/sign-in") with ?next=<this page>.
 *
 *   const s = await requireSession(Astro);
 *   if (s instanceof Response) return s;
 */
export declare function requireSession(context: AstroAuthContext, opts?: SessionOpts & {
    signIn?: string | false;
}): Promise<Session | Response>;
/**
 * The signed-in session with at least `role` (viewer < member < admin <
 * owner) in the active or given organization, or the Response to send
 * instead: signed out as requireSession; no organization or a lower role, 403.
 */
export declare function requireRole(context: AstroAuthContext, role: Role, opts?: SessionOpts & {
    signIn?: string | false;
}): Promise<(Session & {
    organization: Organization;
}) | Response>;
