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
const FORWARD = ["cookie", "x-api-key", "authorization", "user-agent", "x-forwarded-for"];
/**
 * The signed-in user for a request (cookie or x-api-key), with their role in
 * the active organization, or null. Pass organizationId to check another
 * organization the user belongs to (API keys have no active organization).
 */
export async function getSession(request, opts = {}) {
    const headers = new Headers();
    for (const h of FORWARD) {
        const v = request.headers.get(h);
        if (v)
            headers.set(h, v);
    }
    const host = opts.host ?? request.headers.get("x-forwarded-host") ?? request.headers.get("host") ?? env("TIFFIN_AUTH_HOST");
    if (host)
        headers.set("x-tiffin-host", host);
    if (!headers.has("cookie") && !headers.has("x-api-key") && !headers.has("authorization"))
        return null;
    const q = opts.organizationId ? `?organizationId=${encodeURIComponent(opts.organizationId)}` : "";
    const res = await (opts.fetch ?? fetch)(`${base(opts)}/tiffin/session${q}`, { headers });
    if (res.status === 401 || res.status === 403)
        return null;
    if (!res.ok)
        throw new Error(`tiffin-sdk/auth: the auth engine answered ${res.status}`);
    return (await res.json());
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
    const find = async (fresh) => {
        let c = jwksCache.get(url);
        if (fresh || !c || Date.now() - c.at > 10 * 60_000) {
            const headers = {};
            const host = opts.host ?? env("TIFFIN_AUTH_HOST");
            if (host)
                headers["x-tiffin-host"] = host;
            const r = await (opts.fetch ?? fetch)(url, { headers });
            if (!r.ok)
                throw new Error(`tiffin-sdk/auth: JWKS answered ${r.status}`);
            c = { at: Date.now(), keys: (await r.json()).keys };
            jwksCache.set(url, c);
        }
        return c.keys.find((k) => k.kid === header.kid);
    };
    const jwk = (await find(false)) ?? (await find(true));
    if (!jwk || jwk.crv !== "Ed25519" || !jwk.x)
        throw new AuthError(401, "unauthenticated", "Unknown signing key.");
    const key = await crypto.subtle.importKey("jwk", { kty: "OKP", crv: "Ed25519", x: jwk.x }, { name: "Ed25519" }, false, ["verify"]);
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
