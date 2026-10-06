/**
 * `tiffin-sdk/next/auth`: sign-in for Next.js App Router apps on Tiffin, the
 * way the Next.js authentication guide lays it out.
 *
 *   // proxy.ts: optimistic check, no network
 *   export const proxy = authProxy({ protect: ["/dashboard/:path*"] });
 *
 *   // app/dashboard/page.tsx (or a data access layer)
 *   const { user, organization } = await verifySession();
 *
 *   // app/actions.ts
 *   "use server";
 *   export async function signInAction(_: unknown, form: FormData) { return signIn(form, { redirectTo: "/dashboard" }); }
 *
 * getSession, verifySession, requireRole and currentUser work in Server
 * Components, Server Actions and Route Handlers, and run once per request.
 * They read the engine's signed session cookie locally; see getSession in
 * tiffin-sdk/auth for how fresh that is.
 */
import "server-only";
import { type NextRequest } from "next/server";
import { type Role } from "../auth.js";
export type { Role } from "../auth.js";
/** The signed-in user: only what pages need, safe to pass to Client Components. */
export type User = {
    id: string;
    email: string;
    name: string;
    image: string | null;
    emailVerified: boolean;
};
/** The active organization (or the one asked for) and the user's role in it. */
export type Organization = {
    id: string;
    name: string;
    slug: string;
    role: Role | null;
};
export type Session = {
    user: User;
    /** Null when organizations are off, or the user isn't a member of the one asked for. */
    organization: Organization | null;
    sessionId: string;
    /** When the session ends, ISO 8601. */
    expiresAt: string;
    /** A browser session cookie, or an API key (x-api-key) in a Route Handler. */
    via: "session" | "api-key";
};
export type SessionOpts = {
    /** Check this organization instead of the active one. */
    organizationId?: string;
    /** Ask the engine now instead of trusting the signed cookie (up to 60 s old). For sensitive actions. */
    fresh?: boolean;
};
/** The signed-in user and their organization, or null. One engine check per request at most. */
export declare function getSession(opts?: SessionOpts): Promise<Session | null>;
/** The signed-in user, or null. */
export declare function currentUser(): Promise<User | null>;
/**
 * The signed-in session, or a redirect to the sign-in page (with ?next= when
 * authProxy runs). `signIn: false` answers 401 with unauthorized() instead
 * (it needs `experimental.authInterrupts`, which the box turns on).
 */
export declare function verifySession(opts?: SessionOpts & {
    signIn?: string | false;
}): Promise<Session>;
/**
 * The signed-in session with at least `role` (viewer < member < admin <
 * owner) in the active or given organization. Signed out: like
 * verifySession. Role too low or no organization: forbidden(), a 403 (it needs
 * `experimental.authInterrupts`, which the box turns on).
 */
export declare function requireRole(role: Role, opts?: SessionOpts & {
    signIn?: string | false;
}): Promise<Session & {
    organization: Organization;
}>;
/** What sign-in and sign-up actions return (for useActionState). */
export type AuthResult = {
    ok: true; /** False when the user must confirm their email first. */
    signedIn: boolean;
} | {
    ok: false; /** The engine's error code, e.g. INVALID_EMAIL_OR_PASSWORD, CAPTCHA_REQUIRED, TWO_FACTOR_REQUIRED. */
    code: string;
    message: string;
};
/**
 * Signs in with email and password from a Server Action and sets the session
 * cookies. Takes the form (email, password, optional captcha, rememberMe and
 * next) or an object. On success it redirects to the form's `next` (a path
 * on this site) or `redirectTo`, if either is given.
 */
export declare function signIn(input: FormData | {
    email: string;
    password: string;
    captcha?: string;
    rememberMe?: boolean;
    next?: string;
}, opts?: {
    redirectTo?: string;
}): Promise<AuthResult>;
/**
 * Creates an account with email and password from a Server Action. Signed in
 * at once unless the box asks new users to confirm their email first
 * (`signedIn: false`: a link is on its way).
 */
export declare function signUp(input: FormData | {
    name?: string;
    email: string;
    password: string;
    captcha?: string;
    next?: string;
}, opts?: {
    redirectTo?: string;
}): Promise<AuthResult>;
/** Signs out from a Server Action: ends the session on the engine and clears its cookies, then redirects if asked. */
export declare function signOut(opts?: {
    redirectTo?: string;
}): Promise<void>;
/**
 * proxy.ts for signed-in areas: an optimistic check of the session cookie
 * only (no network), as the Next.js guide recommends. Pages still call
 * verifySession or requireRole, which check for real.
 *
 *   export const proxy = authProxy({ protect: ["/dashboard/:path*", "/api/private/:path*"] });
 *
 * Without the cookie, a protected page redirects to `signIn` (default
 * "/sign-in") with ?next=<the page>; a protected path under /api/ answers 401.
 */
export declare function authProxy(opts: {
    protect: string[];
    signIn?: string;
}): (request: NextRequest) => Response;
