/** Organization roles, weakest first. */
export declare const ROLES: readonly ["viewer", "member", "admin", "owner"];
export type Role = (typeof ROLES)[number];
export type AuthUser = {
    id: string;
    email: string;
    name: string;
    image?: string | null;
    emailVerified: boolean;
    createdAt: string;
    updatedAt: string;
    twoFactorEnabled?: boolean | null;
    [key: string]: unknown;
};
export type AuthOrganization = {
    id: string;
    name: string;
    slug: string;
    /** The role to enforce: the member's role, lowered to an API key's cap. */
    role: Role | null;
    /** The person's own role in the organization. */
    memberRole: string | null;
};
export type AuthSession = {
    user: AuthUser;
    session: {
        id: string;
        expiresAt: string;
        activeOrganizationId: string | null;
    };
    /** The active organization (or the one asked for), if the user belongs to it. */
    organization: AuthOrganization | null;
    /** How the request authenticated: a browser session cookie, or an API key (x-api-key). */
    via: "session" | "api-key";
    apiKey: {
        id: string;
        maxRole: string | null;
    } | null;
};
/** An auth failure with the HTTP status to answer with. */
export declare class AuthError extends Error {
    status: 401 | 403;
    code: "unauthenticated" | "no_organization" | "forbidden";
    constructor(status: 401 | 403, code: "unauthenticated" | "no_organization" | "forbidden", message: string);
    /** A JSON response for this error. */
    toResponse(): Response;
}
export type AuthOptions = {
    /** Engine base URL. Default: TIFFIN_AUTH_INTERNAL_URL, else TIFFIN_AUTH_URL. */
    url?: string;
    /** The app host to act for on internal calls. Default: the request's Host, held to TIFFIN_AUTH_HOST's project. */
    host?: string;
    fetch?: typeof fetch;
};
type HeadersLike = {
    get(name: string): string | null;
};
export type SessionOptions = AuthOptions & {
    /** Check this organization instead of the active one (API keys have no active organization). */
    organizationId?: string;
    /** Ask the engine, skipping the signed cookie and the in-process cache (for sensitive actions). */
    fresh?: boolean;
};
/**
 * The signed-in user for a request (cookie or x-api-key), with their role in
 * the active organization, or null. Pass organizationId to check another
 * organization the user belongs to (API keys have no active organization).
 * Without an auth endpoint (running locally, away from the box) it answers
 * null, signed out, and warns once.
 *
 * Fast: a browser session is read from the engine's signed session cookie
 * (verified here with the engine's public key, no request), else asked of the
 * engine and remembered for 5 seconds. So a revoked session or a changed role
 * can take up to 60 seconds to reach this; pass { fresh: true } where that
 * matters.
 */
export declare function getSession(request: Request, opts?: SessionOptions): Promise<AuthSession | null>;
/**
 * getSession from any request headers, plus the engine's Set-Cookie headers
 * when it was asked (a refreshed session cookie, to pass on to the browser
 * when the framework can). For framework helpers such as @shiptiffin/sdk/next/auth.
 */
export declare function sessionFor(from: HeadersLike, opts?: SessionOptions): Promise<{
    session: AuthSession | null;
    setCookie: string[];
}>;
/** Drops what this process remembers about a session token (after signing out). */
export declare function forgetSession(token: string): void;
/**
 * Headers for a server-side call to the engine on behalf of a request: its
 * credentials, client address and user agent, and the app host it came to.
 *
 * The request's Host can be anything: other apps on the box reach this one
 * directly, not through the edge. So the call also names the box-given
 * TIFFIN_AUTH_HOST, and the engine answers for that project whatever host
 * the request names (the request's host only picks which of the project's
 * hosts links and passkeys use). A session from another project never
 * passes here.
 */
export declare function forwardHeaders(from: HeadersLike, opts?: AuthOptions): Headers;
/** The engine's base URL (TIFFIN_AUTH_INTERNAL_URL unless opts.url). */
export declare function authBase(opts?: AuthOptions): string;
/** Better Auth's cookies on a Tiffin box, without their __Host- prefix. */
export declare const TOKEN = "tiffin.session_token";
/**
 * A session cookie's name for a request: over https the engine's cookies are
 * __Host-tiffin.*, which only the app's own host can set. Any other spelling
 * (__Secure-, or plain) can be planted by another app under the same domain,
 * so it is never read. Plain names only on an http-only box.
 */
export declare function cookieName(name: string, request: HeadersLike): string;
export declare function parseCookies(header: string | null): Map<string, string>;
/** The signed-in user, or an AuthError(401). */
export declare function requireUser(request: Request, opts?: SessionOptions): Promise<AuthSession>;
/** Whether `have` is at least `need` (viewer < member < admin < owner). */
export declare function roleAtLeast(have: string | null | undefined, need: Role): boolean;
/**
 * The signed-in user with at least `role` in the active (or given)
 * organization, or an AuthError (401 not signed in, 403 no organization or
 * role too low). API keys count at most as their owner's role, lower if the
 * key is capped.
 */
export declare function requireRole(request: Request, role: Role, opts?: SessionOptions): Promise<AuthSession & {
    organization: AuthOrganization;
}>;
/** An account as getUserByEmail and createUser return it. */
export type ServerUser = {
    id: string;
    /** Lowercased, as the engine stores it. */
    email: string;
    name: string;
    image: string | null;
    emailVerified: boolean;
    banned: boolean;
    createdAt: string;
    updatedAt: string;
};
export type ServerAuthOptions = {
    /** The engine on the box. Default TIFFIN_AUTH_INTERNAL_URL (never the public URL: these calls don't go through the edge). */
    url?: string;
    /** This app's host, which names the project. Default TIFFIN_AUTH_HOST. */
    host?: string;
    /** The project's server key. Default TIFFIN_AUTH_KEY. */
    key?: string;
    fetch?: typeof fetch;
};
/**
 * The project's account with this email (any case), or null. For server code
 * of the project's own apps on the box, e.g. a payment webhook matching a
 * buyer to an account. Another project's apps can't read these users.
 */
export declare function getUserByEmail(email: string, opts?: ServerAuthOptions): Promise<ServerUser | null>;
/**
 * The project's account with this email, made now if there is none
 * (`created: true`), else the one already there, unchanged. It makes only
 * the account: nobody is signed in, no cookie is set and no email is sent.
 * The address stays unconfirmed unless you pass `emailVerified: true`, which
 * you should only do when you know the person controls it (a checkout form's
 * email isn't proof). Unconfirmed, the first magic link or one-time code to
 * that address confirms it and signs them in.
 */
export declare function createUser(input: {
    email: string;
    name?: string;
    emailVerified?: boolean;
}, opts?: ServerAuthOptions): Promise<{
    user: ServerUser;
    created: boolean;
}>;
type BeginSQL<T> = {
    begin: (fn: (tx: any) => Promise<T>) => Promise<T>;
};
type QueryClient = {
    query: (text: string, params?: unknown[]) => Promise<unknown>;
};
type PgPool = QueryClient & {
    totalCount: number;
    connect: () => Promise<QueryClient & {
        release: () => void;
    }>;
};
/**
 * Runs fn in a transaction scoped to an organization: sets app.org_id (and
 * app.user_id when given) with SET LOCAL semantics, so tables protected by
 * `select tiffin_auth.enable_org_rls('notes')` only show and accept that org's rows.
 *
 * Works with postgres.js (anything with sql.begin) and with node-postgres: a
 * Pool (one client is checked out for the transaction and handed to fn) or a
 * client from pool.connect().
 */
export declare function withOrg<T>(db: BeginSQL<T> | QueryClient | PgPool, orgId: string, fn: (tx: any) => Promise<T>, userId?: string): Promise<T>;
export type TokenClaims = {
    sub: string;
    email: string;
    name: string;
    emailVerified: boolean;
    org: string | null;
    role: Role | null;
    iss: string;
    aud: string | string[];
    exp: number;
    iat: number;
    [key: string]: unknown;
};
/**
 * Verifies a JWT from the engine (GET /api/auth/token) against its JWKS:
 * EdDSA signature, expiry, and issuer/audience when given. For services that
 * get a bearer token instead of a cookie.
 */
export declare function verifyToken(token: string, opts?: AuthOptions & {
    issuer?: string;
    audience?: string;
    jwksUrl?: string;
}): Promise<TokenClaims>;
export {};
