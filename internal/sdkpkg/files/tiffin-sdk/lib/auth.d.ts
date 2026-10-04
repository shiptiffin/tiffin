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
    /** The app host to act for on internal calls. Default: the request's Host, else TIFFIN_AUTH_HOST. */
    host?: string;
    fetch?: typeof fetch;
};
/**
 * The signed-in user for a request (cookie or x-api-key), with their role in
 * the active organization, or null. Pass organizationId to check another
 * organization the user belongs to (API keys have no active organization).
 */
export declare function getSession(request: Request, opts?: AuthOptions & {
    organizationId?: string;
}): Promise<AuthSession | null>;
/** The signed-in user, or an AuthError(401). */
export declare function requireUser(request: Request, opts?: AuthOptions & {
    organizationId?: string;
}): Promise<AuthSession>;
/** Whether `have` is at least `need` (viewer < member < admin < owner). */
export declare function roleAtLeast(have: string | null | undefined, need: Role): boolean;
/**
 * The signed-in user with at least `role` in the active (or given)
 * organization, or an AuthError (401 not signed in, 403 no organization or
 * role too low). API keys count at most as their owner's role, lower if the
 * key is capped.
 */
export declare function requireRole(request: Request, role: Role, opts?: AuthOptions & {
    organizationId?: string;
}): Promise<AuthSession & {
    organization: AuthOrganization;
}>;
type BeginSQL<T> = {
    begin: (fn: (tx: any) => Promise<T>) => Promise<T>;
};
type QueryClient = {
    query: (text: string, params?: unknown[]) => Promise<unknown>;
};
/**
 * Runs fn in a transaction scoped to an organization: sets app.org_id (and
 * app.user_id when given) with SET LOCAL semantics, so tables protected by
 * `select auth.enable_org_rls('notes')` only show and accept that org's rows.
 *
 * Works with Bun.sql / postgres.js (anything with sql.begin) and with a
 * dedicated node-postgres client (pool.connect()).
 */
export declare function withOrg<T>(db: BeginSQL<T> | QueryClient, orgId: string, fn: (tx: any) => Promise<T>, userId?: string): Promise<T>;
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
