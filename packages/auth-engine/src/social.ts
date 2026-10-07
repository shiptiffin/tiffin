// Sign-in providers (Google, GitHub, Apple...) and the box's one callback URL.
//
// A provider's keys are the project's own or the box-wide ones. With the
// box-wide keys ("proxied"), the provider is registered with ONE redirect
// URI, on the dashboard host, for every project: Better Auth's OAuth proxy
// plugin sends the sign-in out with that redirect URI, the proxy instance on
// the dashboard host (createProxyInstance) exchanges the code and redirects
// to the app host the sign-in started on with the profile encrypted under
// the proxy secret, and the project's instance makes the account and the
// session in its own database. The secret never leaves the engine.
//
// A provider on the app's own keys works the same way with the app's own
// sign-in host in place of the dashboard host: one redirect URI per app,
// whatever its other hosts and previews (oauthProxies, `appUrl`).
//
// Three guards on top of the plugin:
//   - the proxy instance only serves /api/auth/callback/<provider> (and its
//     plain-text error page), never sets cookies, and every redirect it gives
//     must lead to an app host whose project uses the box-wide keys for that
//     provider (server.ts, proxyRedirectAllowed);
//   - the project's instance only accepts a proxied profile in the browser
//     that started that sign-in: its signed state cookie must match
//     (bindProxyState), which the plugin alone skips;
//   - each instance's trustedOrigins are the project's own app origins, so
//     a sign-in can't be pointed at any other host.
import { hkdfSync } from "node:crypto";
import { betterAuth } from "better-auth";
import type { BetterAuthOptions, BetterAuthPlugin } from "better-auth";
import { APIError, createAuthMiddleware } from "better-auth/api";
import { symmetricDecrypt } from "better-auth/crypto";
import { genericOAuth, oAuthProxy } from "better-auth/plugins";
import { isSocial, SOCIAL, type OAuthApp, type ProxyConfig, type Social } from "./config";

export const PROVIDER_NAMES: Record<Social, string> = {
  google: "Google",
  github: "GitHub",
  apple: "Apple",
  microsoft: "Microsoft",
  discord: "Discord",
  facebook: "Facebook",
  twitter: "X",
  linkedin: "LinkedIn",
  gitlab: "GitLab",
  slack: "Slack",
  twitch: "Twitch",
  oidc: "Single sign-on",
};

/** What a project's sign-in button for a provider is called (OpenID Connect: its label). */
export function providerName(id: Social, app?: OAuthApp): string {
  return id === "oidc" && app?.label ? app.label : PROVIDER_NAMES[id];
}

/** Providers whose verified email may link to an existing account with the same address. */
export const TRUSTED_FOR_LINKING = ["google", "github", "apple"];

/** Apple posts its callback (response_mode=form_post) from this origin. */
export const APPLE_ORIGIN = "https://appleid.apple.com";

type Socials = Partial<Record<Social, OAuthApp>>;

/** Better Auth's socialProviders for the built-in providers among `social`. */
export function socialProviders(social: Socials, only?: Set<string>): NonNullable<BetterAuthOptions["socialProviders"]> {
  const out: Record<string, unknown> = {};
  for (const id of SOCIAL) {
    const a = social[id];
    if (!a || id === "oidc" || (only && !only.has(id))) continue;
    const base = { clientId: a.clientId, clientSecret: a.clientSecret };
    switch (id) {
      case "google":
        out.google = { ...base, prompt: "select_account" };
        break;
      case "microsoft":
        // common: work, school and personal Microsoft accounts.
        out.microsoft = { ...base, tenantId: a.tenantId || "common", prompt: "select_account" };
        break;
      case "gitlab":
        out.gitlab = a.issuer ? { ...base, issuer: a.issuer } : base;
        break;
      default:
        out[id] = base;
    }
  }
  return out as NonNullable<BetterAuthOptions["socialProviders"]>;
}

/** The generic OpenID Connect provider ("oidc"), when set: Okta, Auth0, Keycloak, Entra, company SSO. */
export function oidcPlugin(a: OAuthApp | undefined): BetterAuthPlugin | null {
  if (!a?.issuer) return null;
  return genericOAuth({
    config: [
      {
        providerId: "oidc",
        name: a.label || PROVIDER_NAMES.oidc,
        discoveryUrl: `${a.issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`,
        clientId: a.clientId,
        clientSecret: a.clientSecret,
        scopes: ["openid", "email", "profile"],
        pkce: true,
      },
    ],
  }) as unknown as BetterAuthPlugin;
}

// ---------------------------------------------------------------- the proxy, app side

/** HKDF key for one OAuth proxy purpose, the same as Better Auth's derivePurposeKey for a string secret. */
export function purposeKey(secret: string, purpose: string): string {
  const k = hkdfSync("sha256", Buffer.from(secret, "utf8"), Buffer.from("better-auth:oauth-encryption:v1", "utf8"), Buffer.from(`better-auth:${purpose}:v1`, "utf8"), 32);
  return Buffer.from(k).toString("hex");
}

const isSignInStart = (path?: string) => !!path && (path.startsWith("/sign-in/social") || path === "/link-social");
const isProxyCompletion = (path?: string) => path === "/callback/:id/oauth-proxy" || path === "/oauth-proxy-callback";

export type ProxyRoutes = { url?: string; appUrl?: string; secret: string };

/**
 * Better Auth's OAuth proxy, per provider: those on the box-wide keys go out
 * through the box's callback URL (`url`), those on the app's own keys
 * through the app's sign-in host (`appUrl`). Both use one proxy secret, so
 * one completion endpoint and one callback hook serve both. A provider in
 * neither set signs in on the host it started on, unproxied.
 */
export function oauthProxies(o: ProxyRoutes, viaBox: Set<string>, viaApp: Set<string>): BetterAuthPlugin {
  type Ctx = { path?: string; body?: { provider?: unknown } };
  type Hook = { matcher: (ctx: Ctx) => boolean; handler: unknown };
  type Hooks = { before?: Hook[]; after?: Hook[] };
  const make = (productionURL: string) => oAuthProxy({ productionURL, secret: o.secret, maxAge: 60 }) as unknown as BetterAuthPlugin;
  const signIn = (h: Hook) => h.matcher({ path: "/sign-in/social" });
  const base = make((o.url ?? o.appUrl)!);
  const baseHooks = base.hooks as Hooks | undefined;
  const before: Hook[] = (baseHooks?.before ?? []).filter((h) => !signIn(h));
  const after: Hook[] = (baseHooks?.after ?? []).filter((h) => !signIn(h));
  for (const [url, set] of [
    [o.url, viaBox],
    [o.appUrl, viaApp],
  ] as const) {
    if (!url || !set.size) continue;
    const hooks = make(url).hooks as Hooks | undefined;
    const scoped = (hs: Hook[] | undefined) =>
      (hs ?? []).filter(signIn).map((h) => ({ ...h, matcher: (ctx: Ctx) => h.matcher(ctx) && set.has(String(ctx.body?.provider ?? "")) }));
    before.unshift(...scoped(hooks?.before));
    after.unshift(...scoped(hooks?.after));
  }
  return { ...base, hooks: { before, after } as never };
}

const failTo = (ctx: { context: { baseURL: string }; redirect: (u: string) => unknown }, code: string) => {
  const u = new URL(`${ctx.context.baseURL}/error`);
  u.searchParams.set("error", code);
  return ctx.redirect(u.toString());
};

/**
 * A proxied profile is only accepted in the browser that started the
 * sign-in: the state inside it must equal the project's signed state
 * cookie. (The plugin skips that check, so without this a profile could
 * complete someone else's sign-in, or link someone's account to another.)
 */
export function bindProxyState(o: { secret: string }): BetterAuthPlugin {
  return {
    id: "tiffin-proxy-state",
    hooks: {
      before: [
        {
          matcher: (ctx) => isProxyCompletion(ctx.path),
          handler: createAuthMiddleware(async (ctx) => {
            const profile = (ctx.query as { profile?: unknown } | undefined)?.profile;
            if (typeof profile !== "string" || !profile) return; // the plugin answers missing_profile
            let state: unknown;
            try {
              const plain = await symmetricDecrypt({ key: purposeKey(o.secret, "oauth-proxy-profile"), data: profile });
              state = (JSON.parse(plain) as { state?: unknown }).state;
            } catch {
              throw failTo(ctx as never, "invalid_profile");
            }
            const cookie = ctx.context.createAuthCookie("state");
            const mine = await ctx.getSignedCookie(cookie.name, ctx.context.secret);
            if (!mine || typeof state !== "string" || mine !== state) throw failTo(ctx as never, "state_mismatch");
            ctx.setCookie(cookie.name, "", { ...cookie.attributes, maxAge: 0 });
          }),
        },
      ],
    },
  } satisfies BetterAuthPlugin;
}

// ---------------------------------------------------------------- the proxy, callback side

export type ProxyInstance = { config: ProxyConfig; auth: { handler: (r: Request) => Promise<Response> } };

const PROXY_PATH = /^\/api\/auth\/callback\/([a-z]+)$/;

/** The provider a proxy-host request is the callback of, if it is one. */
export function proxyCallbackProvider(pathname: string): Social | null {
  const m = PROXY_PATH.exec(pathname);
  return m && isSocial(m[1]!) ? m[1] : null;
}

/**
 * The callback endpoint for every box-wide provider. It keeps nothing: the
 * plugin's callback hook exchanges the code with the state the app's
 * instance packed, and redirects to that app. Anything else is refused.
 */
export function createProxyInstance(cfg: ProxyConfig): ProxyInstance {
  const oidc = oidcPlugin(cfg.social.oidc);
  const plugins: BetterAuthPlugin[] = [oAuthProxy({ productionURL: cfg.url, secret: cfg.secret, maxAge: 60 }) as unknown as BetterAuthPlugin];
  if (oidc) plugins.push(oidc);
  plugins.push({
    id: "tiffin-proxy-only",
    hooks: {
      before: [
        {
          // Runs after the plugin's own callback hook, which redirects when
          // the state is one an app packed: reaching here means it wasn't.
          matcher: () => true,
          handler: createAuthMiddleware(async () => {
            throw new APIError("BAD_REQUEST", {
              code: "SIGN_IN_LINK_INVALID",
              message: "This sign-in link is broken or has expired. Go back to the app and sign in again.",
            });
          }),
        },
      ],
    },
  } satisfies BetterAuthPlugin);
  const options: BetterAuthOptions = {
    appName: "Sign-in",
    secret: cfg.secret,
    baseURL: cfg.url,
    basePath: "/api/auth",
    trustedOrigins: [cfg.url, APPLE_ORIGIN],
    socialProviders: socialProviders(cfg.social),
    telemetry: { enabled: false },
    logger: {
      level: "warn",
      log: (level, message) => console.log(JSON.stringify({ level, msg: message, project: "(sign-in callback)", source: "better-auth" })),
    },
    rateLimit: { enabled: true, storage: "memory", window: 60, max: 120 },
    advanced: { cookiePrefix: "tiffin-callback", useSecureCookies: !cfg.url.startsWith("http://") },
    plugins,
  };
  return { config: cfg, auth: betterAuth(options) };
}

/**
 * Whether a redirect from the proxy instance may go out: to an app host
 * whose project uses the box-wide keys for this provider, or to the proxy's
 * own error page. Anything else (another host, a project on its own keys) is
 * refused, so the proxy can't carry a profile anywhere it shouldn't.
 */
export function proxyRedirectAllowed(
  location: string,
  provider: Social,
  proxyURL: string,
  projectFor: (host: string) => { social: Socials; origins: string[] } | undefined,
): boolean {
  let u: URL;
  try {
    u = new URL(location, proxyURL);
  } catch {
    return false;
  }
  const proxy = new URL(proxyURL);
  if (u.origin === proxy.origin) return u.pathname === "/api/auth/error";
  if (u.protocol !== "https:" && proxy.protocol === "https:") return false;
  const p = projectFor(u.hostname.toLowerCase());
  if (!p || !p.social[provider]?.proxied) return false;
  return p.origins.some((o) => {
    try {
      return new URL(o).origin === u.origin;
    } catch {
      return false;
    }
  });
}
