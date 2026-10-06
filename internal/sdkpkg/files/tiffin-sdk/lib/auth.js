// tiffin-sdk/auth: server-side helpers for apps on a Tiffin box with
// services.auth turned on. No dependencies: plain fetch and WebCrypto.
//
//   import { getSession, requireRole, withOrg } from "tiffin-sdk/auth";
//
//   export async function GET(request: Request) {
//     const { user, organization } = await requireRole(request, "member");
//     return withOrg(sql, organization.id, (tx) => tx`select * from notes`);
//   }
//
// The box gives every app TIFFIN_AUTH_URL (public), TIFFIN_AUTH_INTERNAL_URL
// (the engine on the box) and TIFFIN_AUTH_HOST. Server-side calls go to the
// internal URL so they skip the edge.
/** Organization roles, weakest first. */
export const ROLES = ["viewer", "member", "admin", "owner"];
/** An auth failure with the HTTP status to answer with. */
export class AuthError extends Error {
    status;
    code;
    constructor(status, code, message) {
        super(message);
        this.status = status;
        this.code = code;
        this.name = "AuthError";
    }
    /** A JSON response for this error. */
    toResponse() {
        return Response.json({ code: this.code, message: this.message }, { status: this.status });
    }
}
const env = (k) => (typeof process !== "undefined" ? process.env?.[k] : undefined) || undefined;
function base(o) {
    const u = o.url ?? env("TIFFIN_AUTH_INTERNAL_URL") ?? env("TIFFIN_AUTH_URL");
    if (!u)
        throw new Error("tiffin-sdk/auth: no auth endpoint. Turn on services.auth in tiffin.config.ts (the box sets TIFFIN_AUTH_URL), or pass { url }.");
    return u.replace(/\/+$/, "");
}
const FORWARD = ["cookie", "x-api-key", "authorization", "user-agent", "x-forwarded-for", "x-forwarded-proto"];
/**
 * The signed-in user for a request (cookie or x-api-key), with their role in
 * the active organization, or null. Pass organizationId to check another
 * organization the user belongs to (API keys have no active organization).
 *
 * Fast: a browser session is read from the engine's signed session cookie
 * (verified here with the engine's public key, no request), else asked of the
 * engine and remembered for 5 seconds. So a revoked session or a changed role
 * can take up to 60 seconds to reach this; pass { fresh: true } where that
 * matters.
 */
export async function getSession(request, opts = {}) {
    return (await sessionFor(request.headers, opts)).session;
}
/**
 * getSession from any request headers, plus the engine's Set-Cookie headers
 * when it was asked (a refreshed session cookie, to pass on to the browser
 * when the framework can). For framework helpers such as tiffin-sdk/next/auth.
 */
export async function sessionFor(from, opts = {}) {
    const headers = forwardHeaders(from, opts);
    const cookies = parseCookies(headers.get("cookie"));
    const token = cookies.get(SECURE + TOKEN) ?? cookies.get(TOKEN);
    const key = headers.get("x-api-key") ?? headers.get("authorization");
    if (!key && !token)
        return { session: null, setCookie: [] };
    const url = base(opts);
    if (!key && !opts.fresh) {
        const jwt = chunked(cookies, SECURE + DATA) ?? chunked(cookies, DATA);
        const s = jwt ? await fromSessionCookie(jwt, token, url, headers.get("x-tiffin-host"), opts) : undefined;
        if (s)
            return { session: s, setCookie: [] };
    }
    const cacheKey = `${url}|${headers.get("x-tiffin-host")}|${opts.organizationId ?? ""}|${key ?? token}`;
    const hit = recent.get(cacheKey);
    if (!opts.fresh && hit && Date.now() - hit.at < RECENT_MS)
        return { session: hit.session, setCookie: [] };
    const q = opts.organizationId ? `?organizationId=${encodeURIComponent(opts.organizationId)}` : "";
    const res = await (opts.fetch ?? fetch)(`${url}/tiffin/session${q}`, { headers });
    let session = null;
    if (res.status !== 401 && res.status !== 403) {
        if (!res.ok)
            throw new Error(`tiffin-sdk/auth: the auth engine answered ${res.status}`);
        session = (await res.json());
    }
    recent.delete(cacheKey);
    recent.set(cacheKey, { at: Date.now(), session });
    if (recent.size > RECENT_MAX)
        recent.delete(recent.keys().next().value);
    return { session, setCookie: res.headers.getSetCookie?.() ?? [] };
}
/** Drops what this process remembers about a session token (after signing out). */
export function forgetSession(token) {
    for (const k of recent.keys())
        if (k.endsWith("|" + token))
            recent.delete(k);
}
/**
 * Headers for a server-side call to the engine on behalf of a request: its
 * credentials, client address and user agent, and the app host it came to.
 */
export function forwardHeaders(from, opts = {}) {
    const headers = new Headers();
    for (const h of FORWARD) {
        const v = from.get(h);
        if (v)
            headers.set(h, v);
    }
    const host = opts.host ?? from.get("x-forwarded-host") ?? from.get("host") ?? env("TIFFIN_AUTH_HOST");
    if (host)
        headers.set("x-tiffin-host", host);
    return headers;
}
/** The engine's base URL (TIFFIN_AUTH_INTERNAL_URL unless opts.url). */
export function authBase(opts = {}) {
    return base(opts);
}
// ---- Session cookie fast path ---------------------------------------------
const SECURE = "__Secure-";
/** Better Auth's cookies on a Tiffin box (cookiePrefix "tiffin"). */
export const TOKEN = "tiffin.session_token";
const DATA = "tiffin.session_data";
/** How long an engine answer is reused in this process. */
const RECENT_MS = 5_000;
const RECENT_MAX = 10_000;
const recent = new Map();
export function parseCookies(header) {
    const out = new Map();
    for (const part of header?.split(";") ?? []) {
        const i = part.indexOf("=");
        if (i > 0)
            out.set(part.slice(0, i).trim(), part.slice(i + 1).trim());
    }
    return out;
}
// Better Auth splits a cookie over 4 KB into name.0, name.1, ...
function chunked(cookies, name) {
    const whole = cookies.get(name);
    if (whole)
        return whole;
    let out = "";
    for (let i = 0; cookies.has(`${name}.${i}`); i++)
        out += cookies.get(`${name}.${i}`);
    return out || undefined;
}
const json = (part) => JSON.parse(new TextDecoder().decode(b64url(part)));
/**
 * The session in the engine's session cookie cache: a JWT the engine signs
 * with the project's Ed25519 key (60 s), bound to the session token cookie.
 * Undefined when it can't be used (missing, expired, another session, or
 * signed by a key the engine doesn't list): the caller asks the engine.
 */
async function fromSessionCookie(jwt, tokenCookie, url, host, opts) {
    try {
        const [h, p, sig] = jwt.split(".");
        const header = json(h);
        if (header.typ !== "better-auth.session-cache+jwt" || header.alg !== "EdDSA" || !header.kid)
            return undefined;
        const claims = json(p);
        const now = Date.now();
        const token = decodeURIComponent(tokenCookie);
        if (!claims.exp || claims.exp * 1000 <= now || !claims.user || !claims.session || !claims.tiffin)
            return undefined;
        if (claims.sid !== token.slice(0, token.lastIndexOf(".")) || !(Array.isArray(claims.aud) ? claims.aud : [claims.aud]).includes("better-auth:session-cache"))
            return undefined;
        if (new Date(claims.session.expiresAt).getTime() <= now)
            return undefined;
        const org = claims.tiffin.organization;
        if (opts.organizationId && opts.organizationId !== org?.id)
            return undefined;
        const key = await signingKey(`${url}/jwks`, host, header.kid, opts.fetch);
        if (!key || !(await crypto.subtle.verify({ name: "Ed25519" }, key, b64url(sig), new TextEncoder().encode(`${h}.${p}`))))
            return undefined;
        const { banned: _b, banReason: _r, banExpires: _e, ...user } = claims.user;
        return {
            user: user,
            session: { id: claims.session.id, expiresAt: claims.session.expiresAt, activeOrganizationId: claims.session.activeOrganizationId ?? null },
            organization: org,
            via: "session",
            apiKey: null,
        };
    }
    catch {
        return undefined;
    }
}
/** The signed-in user, or an AuthError(401). */
export async function requireUser(request, opts = {}) {
    const s = await getSession(request, opts);
    if (!s)
        throw new AuthError(401, "unauthenticated", "Sign in first.");
    return s;
}
/** Whether `have` is at least `need` (viewer < member < admin < owner). */
export function roleAtLeast(have, need) {
    if (!have)
        return false;
    const rank = (r) => ROLES.indexOf(r.trim());
    return Math.max(...have.split(",").map(rank)) >= rank(need);
}
/**
 * The signed-in user with at least `role` in the active (or given)
 * organization, or an AuthError (401 not signed in, 403 no organization or
 * role too low). API keys count at most as their owner's role, lower if the
 * key is capped.
 */
export async function requireRole(request, role, opts = {}) {
    const s = await requireUser(request, opts);
    if (!s.organization)
        throw new AuthError(403, "no_organization", "Pick an organization first (or pass organizationId).");
    if (!roleAtLeast(s.organization.role, role)) {
        throw new AuthError(403, "forbidden", `This needs the ${role} role in ${s.organization.name}; you have ${s.organization.role ?? "none"}.`);
    }
    return s;
}
/**
 * Runs fn in a transaction scoped to an organization: sets app.org_id (and
 * app.user_id when given) with SET LOCAL semantics, so tables protected by
 * `select auth.enable_org_rls('notes')` only show and accept that org's rows.
 *
 * Works with Bun.sql / postgres.js (anything with sql.begin) and with a
 * dedicated node-postgres client (pool.connect()).
 */
export async function withOrg(db, orgId, fn, userId) {
    if (!orgId)
        throw new Error("withOrg: orgId is required");
    if ("begin" in db && typeof db.begin === "function") {
        return db.begin(async (tx) => {
            await tx `select set_config('app.org_id', ${orgId}, true), set_config('app.user_id', ${userId ?? ""}, true)`;
            return fn(tx);
        });
    }
    const c = db;
    await c.query("BEGIN");
    try {
        await c.query("select set_config('app.org_id', $1, true), set_config('app.user_id', $2, true)", [orgId, userId ?? ""]);
        const out = await fn(c);
        await c.query("COMMIT");
        return out;
    }
    catch (err) {
        await c.query("ROLLBACK").catch(() => { });
        throw err;
    }
}
const jwksCache = new Map();
const b64url = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0));
/**
 * The engine's Ed25519 public key `kid`, from its JWKS. Keys are kept for 10
 * minutes; an unknown kid fetches them again, at most every 30 seconds.
 */
async function signingKey(url, host, kid, f = fetch) {
    const id = `${url}|${host ?? ""}`;
    const load = async () => {
        const r = await f(url, { headers: host ? { "x-tiffin-host": host } : {} });
        if (!r.ok)
            throw new Error(`tiffin-sdk/auth: JWKS answered ${r.status}`);
        const keys = new Map();
        for (const k of (await r.json()).keys) {
            if (k.crv === "Ed25519" && k.x)
                keys.set(k.kid, await crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", x: k.x }, { name: "Ed25519" }, false, ["verify"]));
        }
        return { at: Date.now(), keys };
    };
    let p = jwksCache.get(id);
    let c = p && (await p.catch(() => undefined));
    const age = c ? Date.now() - c.at : Infinity;
    if (!c || age > 10 * 60_000 || (kid && !c.keys.has(kid) && age > 30_000)) {
        if (jwksCache.get(id) === p) {
            const next = load();
            jwksCache.set(id, next);
            next.catch(() => jwksCache.get(id) === next && jwksCache.delete(id));
        }
        c = await jwksCache.get(id);
    }
    return kid ? c.keys.get(kid) : undefined;
}
/**
 * Verifies a JWT from the engine (GET /api/auth/token) against its JWKS:
 * EdDSA signature, expiry, and issuer/audience when given. For services that
 * get a bearer token instead of a cookie.
 */
export async function verifyToken(token, opts = {}) {
    const [h, p, s] = token.split(".");
    if (!h || !p || !s)
        throw new AuthError(401, "unauthenticated", "Malformed token.");
    const header = JSON.parse(new TextDecoder().decode(b64url(h)));
    if (header.alg !== "EdDSA")
        throw new AuthError(401, "unauthenticated", `Unsupported token algorithm ${header.alg}.`);
    const url = opts.jwksUrl ?? env("TIFFIN_AUTH_JWKS_URL") ?? `${base(opts)}/jwks`;
    const key = await signingKey(url, opts.host ?? env("TIFFIN_AUTH_HOST"), header.kid, opts.fetch);
    if (!key)
        throw new AuthError(401, "unauthenticated", "Unknown signing key.");
    const ok = await crypto.subtle.verify({ name: "Ed25519" }, key, b64url(s), new TextEncoder().encode(`${h}.${p}`));
    if (!ok)
        throw new AuthError(401, "unauthenticated", "Bad token signature.");
    const claims = JSON.parse(new TextDecoder().decode(b64url(p)));
    const now = Math.floor(Date.now() / 1000);
    if (typeof claims.exp === "number" && claims.exp < now - 30)
        throw new AuthError(401, "unauthenticated", "Token expired.");
    if (opts.issuer && claims.iss !== opts.issuer)
        throw new AuthError(401, "unauthenticated", "Wrong token issuer.");
    if (opts.audience && !(Array.isArray(claims.aud) ? claims.aud : [claims.aud]).includes(opts.audience)) {
        throw new AuthError(401, "unauthenticated", "Wrong token audience.");
    }
    return claims;
}
