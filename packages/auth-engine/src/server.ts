// The public side: the edge sends /api/auth/* for every web app host of an
// auth-enabled project here, keeping the Host header. Host picks the project.
// Apps calling from inside the box can name the host in x-tiffin-host, held to
// their own project by x-tiffin-auth-host.
import { createHmac, timingSafeEqual } from "node:crypto";
import { reportSignup } from "./analytics";
import { SCHEMA } from "./auth";
import { requestFacts, type RequestFacts } from "./context";
import type { Registry } from "./registry";
import { isSocial, type ProjectConfig } from "./config";
import { providerName, proxyCallbackProvider, proxyRedirectAllowed } from "./social";

const problem = (status: number, code: string, message: string) => Response.json({ code, message }, { status });

const CACHE_COOKIE = /^(__Secure-|__Host-)?tiffin\.session_data(\.\d+)?$/;

/**
 * The request Better Auth sees. The session cookie cache is for apps: the
 * engine always reads sessions from the database, so a revoked session or a
 * ban ends here at once. An internal call naming its app in x-tiffin-host is
 * served as if it came to that host, so links in emails and passkeys use it.
 */
export function engineRequest(req: Request, appHost: string | null, primaryUrl: string): Request {
  const cookie = req.headers.get("cookie");
  const kept =
    cookie &&
    cookie
      .split(";")
      .filter((p) => !CACHE_COOKIE.test(p.split("=")[0]!.trim()))
      .join(";");
  if (kept === cookie && !appHost) return req;
  const headers = new Headers(req.headers);
  if (kept) headers.set("cookie", kept);
  else headers.delete("cookie");
  let url = req.url;
  if (appHost) {
    const u = new URL(req.url);
    url = `${new URL(primaryUrl).protocol}//${appHost}${u.pathname}${u.search}`;
    headers.set("host", appHost);
  }
  return new Request(url, { method: req.method, headers, body: req.body, redirect: "manual", ...(req.body ? { duplex: "half" } : {}) } as RequestInit);
}

/** Where a project's own server code finds and creates users (TIFFIN_AUTH_KEY). Internal calls only. */
export const SERVER_PATH = "/api/auth/tiffin/server/";

/**
 * A project's server key (TIFFIN_AUTH_KEY): "tak_<project>_<mac>", an HMAC
 * of the project's name under the project's engine secret, so only the box
 * (which holds the secret) can make it and each project's differs. The Go
 * module derives the same (internal/mod/auth ServerKey) for the project's apps.
 */
export function serverKey(project: string, secret: string): string {
  return `tak_${project}_${createHmac("sha256", secret).update(`tiffin-auth server key:${project}`).digest("hex").slice(0, 48)}`;
}

const sameKey = (a: string, b: string) => {
  const x = Buffer.from(a);
  const y = Buffer.from(b);
  return x.length === y.length && timingSafeEqual(x, y);
};

const SERVER_CALLS: Record<string, "tiffinFindUser" | "tiffinCreateUser"> = { "find-user": "tiffinFindUser", "create-user": "tiffinCreateUser" };

/**
 * POST /api/auth/tiffin/server/{find-user,create-user}: the project's own
 * server code, on the box. Three things hold it to that project:
 *
 *  - Never through the edge: a request whose Host is an app host (how the
 *    edge sends browsers here) or the dashboard host gets a 404, key or not.
 *  - x-tiffin-auth-host names the project, as on every internal call; it
 *    alone proves nothing (any app on the box can send any host).
 *  - x-tiffin-auth-key must be that project's TIFFIN_AUTH_KEY. Only the
 *    project's apps get it, so another project's app can't read or make
 *    this project's users.
 *
 * The endpoints are Better Auth server-only endpoints: this is the only way to
 * them.
 */
async function serverCall(reg: Registry, req: Request, url: URL): Promise<Response> {
  const host = req.headers.get("host")?.split(":")[0]?.toLowerCase();
  if (reg.projectForHost(host) || (host && host === reg.proxyHost())) return problem(404, "not_found", "Not found.");
  const call = SERVER_CALLS[url.pathname.slice(SERVER_PATH.length)];
  if (!call) return problem(404, "not_found", "No such server call: use find-user or create-user.");
  const pin = req.headers.get("x-tiffin-auth-host");
  const project = reg.projectForHost(pin);
  const inst = project ? reg.get(project) : undefined;
  if (!project || !inst || !sameKey(req.headers.get("x-tiffin-auth-key") ?? "", serverKey(project, inst.config.secret))) {
    return problem(401, "unauthenticated", "Send the project's TIFFIN_AUTH_KEY in x-tiffin-auth-key and its TIFFIN_AUTH_HOST in x-tiffin-auth-host.");
  }
  if (req.method !== "POST") return problem(405, "method_not_allowed", "Use POST.");
  const body = (await req.json().catch(() => null)) as Record<string, unknown> | null;
  if (!body || typeof body !== "object" || typeof body.email !== "string") return problem(422, "validation", "The body is JSON with an email.");
  const facts: RequestFacts = { host: pin!.split(":")[0]!.toLowerCase() };
  try {
    const api = inst.auth.api as unknown as Record<typeof call, (a: { body: unknown }) => Promise<unknown>>;
    return Response.json(await requestFacts.run(facts, () => api[call]({ body })), { headers: { "cache-control": "no-store" } });
  } catch (err) {
    const e = err as { statusCode?: number; body?: { code?: string; message?: string } };
    if (typeof e.statusCode === "number" && e.statusCode >= 400 && e.statusCode < 500) {
      return problem(e.statusCode === 400 ? 422 : e.statusCode, e.body?.code ?? "validation", e.body?.message ?? "Bad request.");
    }
    console.error(JSON.stringify({ level: "error", msg: "server call failed", project, call, err: String(err) }));
    return problem(500, "internal", "The auth engine hit an error. Details are in the auth engine log on the box (journalctl -u tiffin-auth).");
  }
}

export function publicHandler(reg: Registry) {
  return async function handle(req: Request): Promise<Response> {
    reg.maybeReload();
    const url = new URL(req.url);
    if (url.pathname === "/healthz") return Response.json({ ok: true });
    if (!url.pathname.startsWith("/api/auth")) return problem(404, "not_found", "The auth engine only serves /api/auth.");
    if (url.pathname.startsWith(SERVER_PATH)) return serverCall(reg, req, url);
    const proxyHost = reg.proxyHost();
    if (proxyHost && req.headers.get("host")?.split(":")[0]?.toLowerCase() === proxyHost) return proxyCallback(reg, req, url);
    let host = req.headers.get("host");
    let project = reg.projectForHost(host);
    let internal: string | null = null;
    if (!project) {
      internal = req.headers.get("x-tiffin-host");
      project = reg.projectForHost(internal);
      // The SDK pins internal calls to its app's own project (TIFFIN_AUTH_HOST):
      // x-tiffin-host comes from the request the app got, which another app
      // on the box can send it directly with any Host.
      const pin = req.headers.get("x-tiffin-auth-host");
      if (pin) {
        const pinned = reg.projectForHost(pin);
        if (!pinned) return problem(404, "unknown_host", "No auth-enabled project serves the host in x-tiffin-auth-host.");
        if (pinned !== project) {
          project = pinned;
          internal = pin;
        }
      }
      host = internal;
    }
    if (!project) return problem(404, "unknown_host", "No auth-enabled project serves this host. Turn on services.auth in tiffin.config.ts and apply.");
    const inst = reg.get(project);
    if (!inst) return problem(404, "unknown_host", "No auth-enabled project serves this host.");
    if (!internal && url.pathname.startsWith(TEST_PATH) && req.method === "GET") return testSignIn(handle, inst.config, req, url);

    const facts: RequestFacts = { host: host!.split(":")[0]!.toLowerCase() };
    const key = req.headers.get("x-api-key");
    if (key) {
      // Learn who the key belongs to and its role cap before Better Auth runs,
      // so org hooks can hold the key to (at most) its sponsor's role.
      try {
        const r = (await (inst.auth.api as unknown as { verifyApiKey: (a: unknown) => Promise<unknown> }).verifyApiKey({ body: { key } } as never)) as {
          valid: boolean;
          key: { id: string; referenceId?: string; userId?: string; metadata?: Record<string, unknown> | null } | null;
        };
        if (r?.valid && r.key) {
          const userId = r.key.referenceId ?? r.key.userId ?? "";
          const banned = await inst.pool.query(`SELECT banned, "banExpires" FROM "${SCHEMA}"."user" WHERE id = $1`, [userId]);
          const row = banned.rows[0] as { banned?: boolean; banExpires?: Date | null } | undefined;
          if (row?.banned && (!row.banExpires || new Date(row.banExpires) > new Date())) {
            return problem(403, "BANNED", "This account is suspended.");
          }
          const cap = r.key.metadata?.maxRole;
          facts.apiKey = { id: r.key.id, userId, maxRole: typeof cap === "string" ? cap : null };
        }
      } catch {
        // Invalid keys are rejected by Better Auth itself with its own error.
      }
    }
    try {
      const forEngine = engineRequest(req, internal, inst.config.primaryUrl);
      const res = await requestFacts.run(facts, () => inst.auth.handler(forEngine));
      if (facts.newUserId) void reportSignup(inst.config.analytics, req, facts.host!, facts.newUserId);
      // A sign-in coming back through the app's sign-in host goes on to
      // another host of the same app, never anywhere else.
      const loc = res.status >= 300 && res.status < 400 ? res.headers.get("location") : null;
      if (loc && /\/api\/auth\/(callback\/[a-z]+\/oauth-proxy|oauth-proxy-callback)\b/.test(loc)) {
        let to: URL | null = null;
        try {
          to = new URL(loc, inst.config.primaryUrl);
        } catch {
          /* refused below */
        }
        if (!to || !inst.config.hosts.includes(to.hostname.toLowerCase())) {
          console.error(JSON.stringify({ level: "warn", msg: "sign-in redirect to another host refused", project, to: loc.split("?")[0] }));
          return text(400, "This sign-in can't go back to that address. Go back to the app and sign in again.");
        }
      }
      return res;
    } catch (err) {
      console.error(JSON.stringify({ level: "error", msg: "auth request failed", project, path: url.pathname, err: String(err) }));
      return problem(500, "internal", "The auth engine hit an error. Details are in the auth engine log on the box (journalctl -u tiffin-auth).");
    }
  };
}

const text = (status: number, body: string) =>
  new Response(body, { status, headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store", "x-content-type-options": "nosniff" } });

/**
 * The box's one callback URL (box-wide keys), on the dashboard host. Only
 * the callback of a provider with box-wide keys and the error page are
 * served; no cookie goes in or out; every redirect must lead to an app host
 * of a project on the box-wide keys for that provider.
 */
async function proxyCallback(reg: Registry, req: Request, url: URL): Promise<Response> {
  if (url.pathname === "/api/auth/error" && req.method === "GET") {
    const code = (url.searchParams.get("error") ?? "unknown").replace(/[^A-Za-z0-9_]/g, "").slice(0, 60) || "unknown";
    return text(400, `Sign-in didn't finish (${code}). Go back to the app and try again.`);
  }
  const provider = proxyCallbackProvider(url.pathname);
  const inst = reg.proxy();
  if (!provider || !inst || (req.method !== "GET" && req.method !== "POST")) return text(404, "Not found.");
  if (!inst.config.social[provider]) return text(404, `This box has no box-wide ${provider} keys.`);
  const headers = new Headers(req.headers);
  headers.delete("cookie");
  const clean = new Request(`${inst.config.url.replace(/\/+$/, "")}${url.pathname}${url.search}`, {
    method: req.method,
    headers,
    body: req.body,
    redirect: "manual",
    ...(req.body ? { duplex: "half" } : {}),
  } as RequestInit);
  let res: Response;
  try {
    res = await inst.auth.handler(clean);
  } catch (err) {
    console.error(JSON.stringify({ level: "error", msg: "sign-in callback failed", provider, err: String(err) }));
    return text(500, "Sign-in hit an error on the box. Go back to the app and try again.");
  }
  const location = res.headers.get("location");
  if (res.status >= 300 && res.status < 400 && location) {
    const allowed = proxyRedirectAllowed(location, provider, inst.config.url, (h) => {
      const project = reg.projectForHost(h);
      return project ? reg.projectConfig(project) : undefined;
    });
    if (!allowed) {
      console.error(JSON.stringify({ level: "warn", msg: "sign-in callback redirect refused", provider, to: location.split("?")[0] }));
      return text(400, "This sign-in can't go back to that address. Go back to the app and sign in again.");
    }
    return new Response(null, { status: res.status, headers: { location, "cache-control": "no-store", "referrer-policy": "no-referrer" } });
  }
  // Errors come back as JSON or text; never HTML on the dashboard's origin.
  const body = await res.text();
  let message = body;
  try {
    message = (JSON.parse(body) as { message?: string }).message ?? body;
  } catch {
    /* plain text */
  }
  return text(res.status >= 400 ? res.status : 400, message.slice(0, 500));
}

const TEST_PATH = "/api/auth/tiffin/test-sign-in";

/**
 * "Test sign-in" from the dashboard: a link that starts a sign-in with one
 * provider on this host, then says in plain text who signed in. The same
 * as pressing the app's own button, so it signs the visitor in to the app.
 */
async function testSignIn(handle: (r: Request) => Promise<Response>, c: ProjectConfig, req: Request, url: URL): Promise<Response> {
  const provider = url.searchParams.get("provider") ?? "";
  if (!isSocial(provider) || !c.methods.includes(provider)) return text(404, "That sign-in provider isn't turned on for this app.");
  const name = providerName(provider, c.social[provider]);
  const proto = req.headers.get("x-forwarded-proto") === "http" ? "http" : "https";
  const origin = `${proto}://${req.headers.get("host")}`;
  if (url.pathname === `${TEST_PATH}/done`) {
    const err = url.searchParams.get("error");
    if (err) return text(400, `Sign-in with ${name} didn't work (${err.replace(/[^A-Za-z0-9_]/g, "").slice(0, 60)}). Check the keys and the redirect URI in ${name}'s console.`);
    const s = await handle(new Request(`${origin}/api/auth/get-session`, { headers: req.headers }));
    const body = (await s.json().catch(() => null)) as { user?: { email?: string } } | null;
    if (!body?.user?.email) return text(400, `Sign-in with ${name} didn't finish: no session. Try again.`);
    return text(200, `Signed in to ${c.appName} with ${name} as ${body.user.email}. It works. You can close this tab.`);
  }
  if (url.pathname !== TEST_PATH) return text(404, "Not found.");
  if (!c.social[provider]) return text(503, `${name} has no keys yet: set them in the dashboard first.`);
  const done = `${TEST_PATH}/done?provider=${provider}`;
  const headers = new Headers(req.headers);
  headers.set("content-type", "application/json");
  headers.set("origin", origin);
  const res = await handle(
    new Request(`${origin}/api/auth/sign-in/social`, { method: "POST", headers, body: JSON.stringify({ provider, callbackURL: done, errorCallbackURL: done }) }),
  );
  const out = (await res.json().catch(() => null)) as { url?: string; message?: string } | null;
  if (!res.ok || !out?.url) return text(res.status >= 400 ? res.status : 400, out?.message ?? `Couldn't start a sign-in with ${name}.`);
  const go = new Headers({ location: out.url, "cache-control": "no-store" });
  for (const ck of res.headers.getSetCookie()) go.append("set-cookie", ck);
  return new Response(null, { status: 302, headers: go });
}
