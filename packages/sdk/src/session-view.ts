// What the framework helpers (@shiptiffin/sdk/next/auth, @shiptiffin/sdk/astro)
// share: the session as pages see it, and the path rules for protect and ?next=.
import type { AuthSession, Role } from "./auth";

/** The signed-in user: only what pages need, safe to pass to the browser. */
export type User = { id: string; email: string; name: string; image: string | null; emailVerified: boolean };

/** The active organization (or the one asked for) and the user's role in it. */
export type Organization = { id: string; name: string; slug: string; role: Role | null };

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

export function dto(s: AuthSession): Session {
  const o = s.organization;
  return {
    user: { id: s.user.id, email: s.user.email, name: s.user.name, image: (s.user.image as string | null | undefined) ?? null, emailVerified: !!s.user.emailVerified },
    organization: o ? { id: o.id, name: o.name, slug: o.slug, role: o.role } : null,
    sessionId: s.session.id,
    expiresAt: s.session.expiresAt,
    via: s.via,
  };
}

/** A path on this site (for ?next=), or undefined: never another origin. */
export function localPath(p: unknown): string | undefined {
  if (typeof p !== "string" || !p.startsWith("/") || p.startsWith("//") || p.startsWith("/\\") || /[\u0000-\u001f]/.test(p)) return undefined;
  return p;
}

/** "/dashboard/:path*" → a RegExp (Next.js matcher syntax: :name, :name?, :name+, :name*). */
export function pattern(p: string): RegExp {
  let re = "";
  for (const seg of p.split("/").filter(Boolean)) {
    const m = /^:\w+([?+*]?)$/.exec(seg);
    if (!m) re += "/" + seg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    else if (m[1] === "") re += "/[^/]+";
    else re += `(?:/[^/]+)${m[1]}`;
  }
  return new RegExp(`^${re}/?$`);
}
