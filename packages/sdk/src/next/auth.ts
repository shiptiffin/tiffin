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
import { cookies, headers } from "next/headers";
import { forbidden, redirect, unauthorized } from "next/navigation";
import { NextResponse, type NextRequest } from "next/server";
import { cache } from "react";
import { TOKEN, authBase, forgetSession, forwardHeaders, parseCookies, roleAtLeast, sessionFor, type AuthSession, type Role } from "../auth";

export type { Role } from "../auth";

/** The signed-in user: only what pages need, safe to pass to Client Components. */
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
  /** A browser session cookie, or an API key (x-api-key) in a Route Handler. */
  via: "session" | "api-key";
};

export type SessionOpts = {
  /** Check this organization instead of the active one. */
  organizationId?: string;
  /** Ask the engine now instead of trusting the signed cookie (up to 60 s old). For sensitive actions. */
  fresh?: boolean;
};

/** The request header authProxy sets to the page's path, for ?next= on redirects to sign-in. */
const PATH_HEADER = "x-tiffin-path";

function dto(s: AuthSession): Session {
  const o = s.organization;
  return {
    user: { id: s.user.id, email: s.user.email, name: s.user.name, image: (s.user.image as string | null | undefined) ?? null, emailVerified: !!s.user.emailVerified },
    organization: o ? { id: o.id, name: o.name, slug: o.slug, role: o.role } : null,
    sessionId: s.session.id,
    expiresAt: s.session.expiresAt,
    via: s.via,
  };
}

/** The request's headers, with the cookie header as cookies() sees it (it includes cookies a Server Action just set). */
async function requestHeaders(): Promise<Headers> {
  const h = new Headers(await headers());
  const all = (await cookies()).getAll();
  if (all.length) h.set("cookie", all.map((c) => `${c.name}=${encodeURIComponent(c.value)}`).join("; "));
  else h.delete("cookie");
  return h;
}

/**
 * Passes the engine's Set-Cookie headers on to the browser. Only Server
 * Actions and Route Handlers can set cookies; elsewhere, with `quiet`, it
 * skips them (the browser keeps its cookies, and the next engine call
 * refreshes them).
 */
async function relay(setCookie: string[], quiet = false) {
  if (!setCookie.length) return;
  const jar = await cookies();
  for (const line of setCookie) {
    const [pair, ...attrs] = line.split(";");
    const i = pair!.indexOf("=");
    if (i <= 0) continue;
    const name = pair!.slice(0, i).trim();
    let value = pair!.slice(i + 1).trim();
    try {
      value = decodeURIComponent(value);
    } catch {}
    const o: Record<string, unknown> = {};
    for (const a of attrs) {
      const [k, v = ""] = a.split("=");
      const key = k!.trim().toLowerCase();
      const val = v.trim();
      if (key === "max-age") o.maxAge = Number(val);
      else if (key === "expires") o.expires = new Date(val);
      else if (key === "path") o.path = val;
      else if (key === "domain") o.domain = val;
      else if (key === "secure") o.secure = true;
      else if (key === "httponly") o.httpOnly = true;
      else if (key === "samesite") o.sameSite = val.toLowerCase();
      else if (key === "partitioned") o.partitioned = true;
    }
    try {
      jar.set(name, value, o);
    } catch (err) {
      if (!quiet) throw err;
      return;
    }
  }
}

const load = cache(async (organizationId: string | undefined, fresh: boolean): Promise<Session | null> => {
  const h = await requestHeaders();
  const { session, setCookie } = await sessionFor(h, { ...(organizationId ? { organizationId } : {}), fresh });
  if (!h.has("x-api-key")) await relay(setCookie, true); // a re-signed cookie, or an ended session's cleared
  return session && dto(session);
});

/** The signed-in user and their organization, or null. One engine check per request at most. */
export function getSession(opts: SessionOpts = {}): Promise<Session | null> {
  return load(opts.organizationId, opts.fresh === true);
}

/** The signed-in user, or null. */
export async function currentUser(): Promise<User | null> {
  return (await getSession())?.user ?? null;
}

/** A path on this site (for ?next=), or undefined: never another origin. */
function localPath(p: unknown): string | undefined {
  if (typeof p !== "string" || !p.startsWith("/") || p.startsWith("//") || p.startsWith("/\\") || /[\u0000-\u001f]/.test(p)) return undefined;
  return p;
}

/**
 * The signed-in session, or a redirect to the sign-in page (with ?next= when
 * authProxy runs). `signIn: false` answers 401 with unauthorized() instead
 * (it needs `experimental.authInterrupts` in next.config).
 */
export async function verifySession(opts: SessionOpts & { signIn?: string | false } = {}): Promise<Session> {
  const s = await getSession(opts);
  if (s) return s;
  if (opts.signIn === false) unauthorized();
  const next = localPath((await headers()).get(PATH_HEADER));
  redirect((opts.signIn ?? "/sign-in") + (next ? `?next=${encodeURIComponent(next)}` : ""));
}

/**
 * The signed-in session with at least `role` (viewer < member < admin <
 * owner) in the active or given organization. Signed out: like
 * verifySession. Role too low or no organization: forbidden(), a 403 (it needs
 * `experimental.authInterrupts` in next.config).
 */
export async function requireRole(role: Role, opts: SessionOpts & { signIn?: string | false } = {}): Promise<Session & { organization: Organization }> {
  const s = await verifySession(opts);
  if (!s.organization || !roleAtLeast(s.organization.role, role)) forbidden();
  return s as Session & { organization: Organization };
}

// ---- Server Actions ---------------------------------------------------------

/** What sign-in and sign-up actions return (for useActionState). */
export type AuthResult =
  | { ok: true; /** False when the user must confirm their email first. */ signedIn: boolean }
  | { ok: false; /** The engine's error code, e.g. INVALID_EMAIL_OR_PASSWORD, CAPTCHA_REQUIRED, TWO_FACTOR_REQUIRED. */ code: string; message: string };

type Fields = Record<string, unknown>;
const fields = (input: FormData | Fields): Fields => (input instanceof FormData ? Object.fromEntries(input) : input);
const text = (v: unknown) => (typeof v === "string" ? v : "");

/** POSTs to the engine for the browser that called the action and relays its cookies. */
async function engine(path: string, body: unknown, captcha?: string): Promise<{ status: number; body: Record<string, unknown> | null }> {
  const req = await requestHeaders();
  const h = forwardHeaders(req);
  const host = h.get("x-tiffin-host");
  h.set("content-type", "application/json");
  h.set("origin", req.get("origin") ?? `${req.get("x-forwarded-proto") === "http" ? "http" : "https"}://${host}`);
  if (captcha) h.set("x-captcha-response", captcha);
  const res = await fetch(`${authBase()}${path}`, { method: "POST", headers: h, body: JSON.stringify(body) });
  await relay(res.headers.getSetCookie());
  const raw = await res.text();
  let parsed: Record<string, unknown> | null = null;
  try {
    parsed = raw ? JSON.parse(raw) : null;
  } catch {}
  return { status: res.status, body: parsed };
}

function failure(r: { status: number; body: Record<string, unknown> | null }): AuthResult {
  const code = text(r.body?.code) || (r.status === 429 ? "TOO_MANY_REQUESTS" : "AUTH_FAILED");
  const message = text(r.body?.message) || (r.status === 429 ? "Too many tries in a row. Wait a little and try again." : `The auth engine answered ${r.status}.`);
  return { ok: false, code, message };
}

function done(input: Fields, opts: { redirectTo?: string }, result: AuthResult): AuthResult {
  const to = localPath(input.next) ?? localPath(opts.redirectTo);
  if (result.ok && result.signedIn && to) redirect(to);
  return result;
}

/**
 * Signs in with email and password from a Server Action and sets the session
 * cookies. Takes the form (email, password, optional captcha, rememberMe and
 * next) or an object. On success it redirects to the form's `next` (a path
 * on this site) or `redirectTo`, if either is given.
 */
export async function signIn(
  input: FormData | { email: string; password: string; captcha?: string; rememberMe?: boolean; next?: string },
  opts: { redirectTo?: string } = {},
): Promise<AuthResult> {
  const f = fields(input);
  const rememberMe = f.rememberMe === undefined ? true : f.rememberMe === true || f.rememberMe === "on" || f.rememberMe === "true";
  const r = await engine("/sign-in/email", { email: text(f.email), password: text(f.password), rememberMe }, text(f.captcha));
  if (r.status !== 200) return failure(r);
  if (r.body?.twoFactorRedirect) return { ok: false, code: "TWO_FACTOR_REQUIRED", message: "Enter the code from your authenticator app." };
  return done(f, opts, { ok: true, signedIn: true });
}

/**
 * Creates an account with email and password from a Server Action. Signed in
 * at once unless the box asks new users to confirm their email first
 * (`signedIn: false`: a link is on its way).
 */
export async function signUp(
  input: FormData | { name?: string; email: string; password: string; captcha?: string; next?: string },
  opts: { redirectTo?: string } = {},
): Promise<AuthResult> {
  const f = fields(input);
  const email = text(f.email);
  const r = await engine("/sign-up/email", { name: text(f.name).trim() || email.split("@")[0], email, password: text(f.password) }, text(f.captcha));
  if (r.status !== 200) return failure(r);
  return done(f, opts, { ok: true, signedIn: typeof r.body?.token === "string" });
}

/** Signs out from a Server Action: ends the session on the engine and clears its cookies, then redirects if asked. */
export async function signOut(opts: { redirectTo?: string } = {}): Promise<void> {
  const raw = parseCookies((await requestHeaders()).get("cookie"));
  const token = raw.get("__Secure-" + TOKEN) ?? raw.get(TOKEN);
  if (token) {
    await engine("/sign-out", {});
    forgetSession(token);
  }
  const to = localPath(opts.redirectTo);
  if (to) redirect(to);
}

// ---- proxy.ts ---------------------------------------------------------------

/** "/dashboard/:path*" → a RegExp (Next.js matcher syntax: :name, :name?, :name+, :name*). */
function pattern(p: string): RegExp {
  let re = "";
  for (const seg of p.split("/").filter(Boolean)) {
    const m = /^:\w+([?+*]?)$/.exec(seg);
    if (!m) re += "/" + seg.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    else if (m[1] === "") re += "/[^/]+";
    else re += `(?:/[^/]+)${m[1]}`;
  }
  return new RegExp(`^${re}/?$`);
}

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
export function authProxy(opts: { protect: string[]; signIn?: string }): (request: NextRequest) => Response {
  const rules = opts.protect.map(pattern);
  const signIn = opts.signIn ?? "/sign-in";
  return (request) => {
    const { pathname, search } = request.nextUrl;
    const signedIn = request.cookies.has("__Secure-" + TOKEN) || request.cookies.has(TOKEN) || request.headers.has("x-api-key");
    if (!signedIn && rules.some((r) => r.test(pathname))) {
      if (pathname.startsWith("/api/")) return Response.json({ code: "unauthenticated", message: "Sign in first." }, { status: 401 });
      const url = new URL(signIn, request.nextUrl);
      url.searchParams.set("next", pathname + search);
      return NextResponse.redirect(url);
    }
    const h = new Headers(request.headers);
    h.set(PATH_HEADER, pathname + search);
    return NextResponse.next({ request: { headers: h } });
  };
}
