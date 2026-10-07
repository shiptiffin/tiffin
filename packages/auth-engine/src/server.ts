// The public side: the edge sends /api/auth/* for every web app host of an
// auth-enabled project here, keeping the Host header. Host picks the project.
// Apps calling from inside the box can name the host in x-tiffin-host.
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

export function publicHandler(reg: Registry) {
  return async function handle(req: Request): Promise<Response> {
    reg.maybeReload();
    const url = new URL(req.url);
    if (url.pathname === "/healthz") return Response.json({ ok: true });
    if (!url.pathname.startsWith("/api/auth")) return problem(404, "not_found", "The auth engine only serves /api/auth.");
    const proxyHost = reg.proxyHost();
    if (proxyHost && req.headers.get("host")?.split(":")[0]?.toLowerCase() === proxyHost) return proxyCallback(reg, req, url);
    let host = req.headers.get("host");
    let project = reg.projectForHost(host);
    let internal: string | null = null;
    if (!project) {
      internal = req.headers.get("x-tiffin-host");
      project = reg.projectForHost(internal);
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
