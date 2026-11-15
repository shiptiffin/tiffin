import type { AuthSession, Role } from "./auth.js";
/** The signed-in user: only what pages need, safe to pass to the browser. */
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
    /** A browser session cookie, or an API key (x-api-key). */
    via: "session" | "api-key";
};
export declare function dto(s: AuthSession): Session;
/** A path on this site (for ?next=), or undefined: never another origin. */
export declare function localPath(p: unknown): string | undefined;
/** "/dashboard/:path*" → a RegExp (Next.js matcher syntax: :name, :name?, :name+, :name*). */
export declare function pattern(p: string): RegExp;
