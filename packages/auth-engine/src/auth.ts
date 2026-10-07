// Builds one project's Better Auth instance from its config. Every project on
// the box gets its own instance (own secret, own database, own settings)
// inside the one engine process.
import { betterAuth, getCurrentAdapter } from "better-auth";
import type { BetterAuthOptions, BetterAuthPlugin } from "better-auth";
import { APIError, createAuthEndpoint, createAuthMiddleware, getSessionFromCtx } from "better-auth/api";
import { emailOTP, jwt, magicLink, organization, twoFactor } from "better-auth/plugins";
import { apiKey } from "@better-auth/api-key";
import { passkey } from "@better-auth/passkey";
import { setCookieCache } from "better-auth/cookies";
import { symmetricDecrypt, symmetricEncrypt } from "better-auth/crypto";
import { PostgresDialect } from "kysely";
import pg from "pg";
import { z } from "zod";
import { altcha } from "./altcha";
import { isSocial, SOCIAL, type ProjectConfig } from "./config";
import { currentFacts } from "./context";
import { effectiveRole, inviteLinks } from "./invite-links";
import { send, templates, type Brand } from "./mail";
import { ac, rank, roles, weaker } from "./roles";
import { APPLE_ORIGIN, bindProxyState, oauthProxies, oidcPlugin, providerName, socialProviders, TRUSTED_FOR_LINKING } from "./social";

// Everything named tiffin* in a project database belongs to the box; public and
// the rest belong to the app.
export const SCHEMA = "tiffin_auth";

type Adapter = Parameters<typeof effectiveRole>[0];

/** Paths an API key may never call: account security and joining orgs need a person. */
const KEY_DENY = [
  "/api-key",
  "/passkey",
  "/two-factor",
  "/change-password",
  "/set-password",
  "/change-email",
  "/delete-user",
  "/link-social",
  "/unlink-account",
  "/revoke-session",
  "/revoke-sessions",
  "/revoke-other-sessions",
  "/organization/accept-invitation",
  "/organization/reject-invitation",
  "/organization/leave",
  "/invite-link/accept",
];

/** Org mutations: the permission each needs, checked against the caller's effective role. */
const ORG_ACTIONS: Record<string, Record<string, string[]>> = {
  "/organization/invite-member": { invitation: ["create"] },
  "/organization/cancel-invitation": { invitation: ["cancel"] },
  "/organization/update-member-role": { member: ["update"] },
  "/organization/remove-member": { member: ["delete"] },
  "/organization/update": { organization: ["update"] },
  "/organization/delete": { organization: ["delete"] },
};

function can(role: string | null, perm: Record<string, string[]>): boolean {
  if (!role) return false;
  const r = roles[role as keyof typeof roles];
  return !!r && r.authorize(perm as never).success;
}

const forbid = (code: string, message: string) => new APIError("FORBIDDEN", { code, message });

export type Instance = {
  project: string;
  config: ProjectConfig;
  auth: ReturnType<typeof betterAuth>;
  pool: pg.Pool;
};

type OrgView = { id: string; name: string; slug: string; role: string | null; memberRole: string | null };

/** A user's view of one organization: its name and their role, lowered to `cap`. Null if not a member. */
async function orgView(adapter: Adapter, userId: string, orgId: string, cap: string): Promise<OrgView | null> {
  const [o, m] = await Promise.all([
    adapter.findOne<{ id: string; name: string; slug: string }>({ model: "organization", where: [{ field: "id", value: orgId }] }),
    adapter.findOne<{ role: string }>({
      model: "member",
      where: [
        { field: "userId", value: userId },
        { field: "organizationId", value: orgId },
      ],
    }),
  ]);
  return o && m ? { id: o.id, name: o.name, slug: o.slug, memberRole: m.role, role: weaker(m.role, cap) } : null;
}

/** The adapter of the transaction the caller runs in, if any (so rows it made but hasn't committed are seen). */
const txAdapter = async (fallback: Adapter): Promise<Adapter> => (await getCurrentAdapter(fallback as never)) as unknown as Adapter;

/**
 * Sign-in provider tokens at rest. With account.encryptOAuthTokens, Better
 * Auth encrypts the access and refresh tokens (XChaCha20-Poly1305, keyed by
 * the project's secret) and decrypts them for getAccessToken and
 * refreshToken, but stores the ID token as it came. These do the same for
 * the ID token, with the same key and cipher. Ciphertext is hex (or a
 * "$ba$" envelope with versioned secrets); an ID token is a JWT, never hex.
 */
type SecretConfig = Parameters<typeof symmetricEncrypt>[0]["key"];
const sealed = (t: string) => t.startsWith("$ba$") || (t.length % 2 === 0 && /^[0-9a-f]+$/i.test(t));
async function sealIdToken<T extends { idToken?: string | null }>(key: SecretConfig | undefined, data: T): Promise<T> {
  if (typeof data.idToken !== "string" || !data.idToken || sealed(data.idToken)) return data;
  if (!key) throw new Error("auth engine: no secret to encrypt the ID token with");
  return { ...data, idToken: await symmetricEncrypt({ key, data: data.idToken }) };
}

/** Endpoints that hand an account's provider tokens back (decrypted) to its signed-in owner. */
const TOKEN_PATHS = new Set(["/get-access-token", "/refresh-token"]);

/** The organization a user joined first, if any. */
async function firstOrg(adapter: Adapter, userId: string): Promise<string | undefined> {
  const ms = await adapter.findMany<{ organizationId: string }>({
    model: "member",
    where: [{ field: "userId", value: userId }],
    sortBy: { field: "createdAt", direction: "asc" },
    limit: 1,
  });
  return ms[0]?.organizationId;
}

/** Everyone is an org: each person gets a personal one to own things in. Returns its id. */
async function personalOrg(adapter: Adapter, userId: string): Promise<string> {
  const existing = await firstOrg(adapter, userId);
  if (existing) return existing;
  const now = new Date();
  const org = await adapter.create<{ id: string }>({
    model: "organization",
    data: {
      name: "Personal",
      slug: `personal-${userId.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 24)}`,
      createdAt: now,
      metadata: JSON.stringify({ personal: true }),
    },
  });
  await adapter.create({ model: "member", data: { organizationId: org.id, userId, role: "owner", createdAt: now } });
  return org.id;
}

/**
 * How long the signed session cookie cache (`tiffin.session_data`) lasts.
 * Apps verify it locally (@shiptiffin/sdk/auth) instead of asking the engine, so a
 * revoked session, a ban or a role change reaches app code within this long.
 * The engine itself never reads it (server.ts strips it).
 */
export const SESSION_CACHE_SECONDS = 60;

/**
 * The passkey rpID for a request to `host`: the shortest of the app's hosts
 * that is `host` or a parent of it (so www.example.com and example.com share
 * passkeys), else the primary host.
 */
export function rpIDFor(hosts: string[], host: string | undefined): string {
  const h = host?.split(":")[0]?.toLowerCase();
  let best: string | undefined;
  if (h) for (const a of hosts) if ((h === a || h.endsWith("." + a)) && (!best || a.length < best.length)) best = a;
  return best ?? hosts[0]!;
}

/**
 * Passkeys on every host of the app. WebAuthn ties a passkey to one rpID and
 * Better Auth's plugin takes a fixed one, so each endpoint runs the plugin
 * built for the request's host.
 */
function passkeys(c: ProjectConfig): BetterAuthPlugin {
  const byRP = new Map<string, BetterAuthPlugin>();
  const forHost = (host: string | undefined) => {
    const rpID = rpIDFor(c.hosts, host);
    let p = byRP.get(rpID);
    if (!p) {
      p = passkey({ rpID, rpName: c.appName, origin: c.origins.length ? c.origins : [c.primaryUrl.replace(/\/+$/, "")] }) as unknown as BetterAuthPlugin;
      byRP.set(rpID, p);
    }
    return p;
  };
  const base = forHost(undefined);
  const endpoints: Record<string, unknown> = {};
  for (const [name, ep] of Object.entries(base.endpoints ?? {})) {
    const run = (ctx: unknown) => (forHost(currentFacts().host).endpoints as Record<string, (c: unknown) => unknown>)[name]!(ctx);
    endpoints[name] = Object.assign(run, ep);
  }
  return { ...base, endpoints } as BetterAuthPlugin;
}

type CacheSigner = {
  sign: (ctx: { context: { adapter: unknown } }, payload: Record<string, unknown>, expiresIn: number) => Promise<string>;
  verify: (...a: never[]) => unknown;
};

/**
 * Adds the active organization and the user's role in it to the signed
 * session cookie cache, so apps check roles without asking the engine. Goes
 * after jwt({ sessionCookieCache: true }), which makes the signer.
 */
function sessionCacheOrg(c: ProjectConfig): BetterAuthPlugin {
  return {
    id: "tiffin-session-cache",
    init(ctx) {
      const sc = ctx.sessionConfig as typeof ctx.sessionConfig & { cookieCacheSigner?: CacheSigner };
      const signer = sc.cookieCacheSigner;
      if (!signer) return;
      const sign: CacheSigner["sign"] = async (ectx, payload, expiresIn) => {
        const p = payload as { session: { activeOrganizationId?: string | null }; user: { id: string } };
        const orgId = p.session.activeOrganizationId;
        const organization = c.organizations && orgId ? await orgView(await txAdapter(ectx.context.adapter as Adapter), p.user.id, orgId, "owner") : null;
        return signer.sign(ectx, { ...payload, tiffin: { organization } }, expiresIn);
      };
      return { context: { sessionConfig: { ...sc, cookieCacheSigner: { ...signer, sign } } } } as never;
    },
    hooks: {
      after: [
        {
          // These change the caller's active organization, its name or their
          // role in it without re-signing the cookie: re-sign it.
          matcher: (ctx) => !!ctx.path && ORG_CHANGES.has(ctx.path),
          handler: createAuthMiddleware(async (ctx) => {
            if (currentFacts().apiKey) return;
            const token = await ctx.getSignedCookie(ctx.context.authCookies.sessionToken.name, ctx.context.secret);
            const s = token ? await ctx.context.internalAdapter.findSession(token) : null;
            if (!s) return;
            const dontRemember = await ctx.getSignedCookie(ctx.context.authCookies.dontRememberToken.name, ctx.context.secret);
            await setCookieCache(ctx, s, !!dontRemember);
          }),
        },
      ],
    },
  } satisfies BetterAuthPlugin;
}

/** Endpoints that send mail: refused in production while the box can't send any. */
const MAIL_PATHS = new Set([
  "/sign-up/email",
  "/sign-in/magic-link",
  "/email-otp/send-verification-otp",
  "/email-otp/request-password-reset",
  "/forget-password/email-otp",
  "/email-otp/request-email-change",
  "/request-password-reset",
  "/send-verification-email",
  "/change-email",
]);

export const EMAIL_NOT_SET_UP = "This app can't send email yet: connect a mail service in Settings › Email.";

/** Whether email sign-in is refused for this request: production while mail can't leave the box. Previews keep the dev inbox. */
function emailBlockedHere(c: ProjectConfig): boolean {
  if (!c.emailBlocked) return false;
  const host = currentFacts().host;
  return !(host && c.previewHosts.includes(host));
}

const ORG_CHANGES = new Set([
  "/organization/create",
  "/organization/update",
  "/organization/delete",
  "/organization/leave",
  "/organization/accept-invitation",
  "/organization/remove-member",
  "/organization/update-member-role",
  "/invite-link/accept",
]);

/** The Better Auth options for a project; also what migrations are computed from. */
export function buildOptions(project: string, c: ProjectConfig, pool: pg.Pool): BetterAuthOptions {
  const methods = new Set(c.methods);
  const holder: { adapter?: Adapter; secret?: SecretConfig } = {};
  const mail = (m: Parameters<typeof send>[3]) => send(project, c.smtpUrl, `${c.appName} <${c.emailFrom}>`, m);
  const brand: Brand = { app: c.appName, primaryUrl: c.primaryUrl, ...c.emailBrand };
  const originOf = (request?: Request) => {
    const h = request?.headers.get("host")?.split(":")[0];
    if (request && h && c.hosts.includes(h)) {
      const proto = request.headers.get("x-forwarded-proto") ?? new URL(request.url).protocol.replace(":", "");
      return `${proto === "http" ? "http" : "https"}://${request.headers.get("host")}`;
    }
    return c.primaryUrl.replace(/\/+$/, "");
  };

  const tiffin = {
    id: "tiffin",
    init(ctx) {
      holder.adapter = ctx.adapter as unknown as Adapter;
      holder.secret = ctx.secretConfig as SecretConfig;
    },
    endpoints: {
      tiffinConfig: createAuthEndpoint("/tiffin/config", { method: "GET" }, async (ctx) =>
        ctx.json({
          appName: c.appName,
          methods: c.methods,
          // Email sign-up, links and codes work on this host (false: the box can't send email yet).
          emailReady: !emailBlockedHere(c),
          organizations: c.organizations,
          captcha: c.captcha,
          // For each sign-in provider turned on: whether it has keys (so the button works).
          social: Object.fromEntries(SOCIAL.map((id) => [id, methods.has(id) ? { configured: !!c.social[id] } : null])),
          // The buttons to show, in the order they're listed, with their names.
          providers: SOCIAL.filter((id) => methods.has(id)).map((id) => ({ id, name: providerName(id, c.social[id]), configured: !!c.social[id] })),
        }),
      ),
      tiffinSession: createAuthEndpoint(
        "/tiffin/session",
        { method: "GET", query: z.object({ organizationId: z.string().optional() }).optional() },
        async (ctx) => {
          const s = await getSessionFromCtx(ctx);
          if (!s) return ctx.json(null);
          const facts = currentFacts();
          const orgId = ctx.query?.organizationId ?? (s.session as { activeOrganizationId?: string | null }).activeOrganizationId ?? null;
          let org: OrgView | null = null;
          if (orgId && c.organizations) {
            org = await orgView(ctx.context.adapter as unknown as Adapter, s.user.id, orgId, facts.apiKey?.maxRole ?? "owner");
          }
          const { banned: _b, banReason: _r, banExpires: _e, ...user } = s.user as Record<string, unknown>;
          return ctx.json({
            user,
            session: { id: s.session.id, expiresAt: s.session.expiresAt, activeOrganizationId: (s.session as { activeOrganizationId?: string | null }).activeOrganizationId ?? null },
            organization: org,
            via: facts.apiKey ? "api-key" : "session",
            apiKey: facts.apiKey ? { id: facts.apiKey.id, maxRole: facts.apiKey.maxRole } : null,
          });
        },
      ),
    },
    hooks: {
      before: [
        {
          // Sign in with a provider the project has no keys for yet: say how to fix it.
          matcher: (ctx) => ctx.path === "/sign-in/social" || ctx.path === "/link-social",
          handler: createAuthMiddleware(async (ctx) => {
            const p = (ctx.body as { provider?: string } | undefined)?.provider;
            if (!p || !isSocial(p)) return;
            const name = providerName(p, c.social[p]);
            const env = p.toUpperCase();
            if (!methods.has(p)) {
              throw new APIError("BAD_REQUEST", { code: "METHOD_DISABLED", message: `Sign in with ${name} isn't turned on for this app. Add "${p}" to services.auth.methods in tiffin.config.ts.` });
            }
            if (!c.social[p]) {
              throw new APIError("SERVICE_UNAVAILABLE", {
                code: "SOCIAL_NOT_CONFIGURED",
                message:
                  `Sign in with ${name} isn't set up yet. The box owner can add ${name} keys once for every project in Box settings → Sign-in providers, ` +
                  `or the app's owner can set this project's own ${env}_CLIENT_ID and ${env}_CLIENT_SECRET secrets (callback URL ${originOf(ctx.request)}/api/auth/callback/${p}).`,
              });
            }
          }),
        },
        {
          // No mail service yet: in production, refuse what would send mail,
          // rather than let anyone sign up with an address they don't own.
          matcher: (ctx) => c.emailBlocked && !!ctx.path && MAIL_PATHS.has(ctx.path),
          handler: createAuthMiddleware(async () => {
            if (!emailBlockedHere(c)) return;
            throw new APIError("SERVICE_UNAVAILABLE", { code: "EMAIL_NOT_SET_UP", message: EMAIL_NOT_SET_UP });
          }),
        },
        {
          // API keys act for the person who made them, never above them.
          matcher: () => !!currentFacts().apiKey,
          handler: createAuthMiddleware(async (ctx) => {
            const path = ctx.path;
            if (KEY_DENY.some((p) => path === p || path.startsWith(p + "/"))) {
              throw forbid("API_KEY_NOT_ALLOWED", "API keys can't do this. Sign in as a person to manage keys, passkeys, two-factor, passwords, sessions or memberships.");
            }
          }),
        },
        {
          // Nobody grants, invites with or edits a role above their own.
          matcher: (ctx) => c.organizations && !!ctx.path && ctx.path in ORG_ACTIONS,
          handler: createAuthMiddleware(async (ctx) => {
            const s = await getSessionFromCtx(ctx);
            if (!s) return; // the endpoint itself answers 401
            const adapter = ctx.context.adapter as unknown as Adapter;
            const body = (ctx.body ?? {}) as Record<string, unknown>;
            let orgId = (body.organizationId as string | undefined) ?? (s.session as { activeOrganizationId?: string | null }).activeOrganizationId ?? undefined;
            let target: { role: string; organizationId: string } | null = null;
            if (ctx.path === "/organization/update-member-role" && typeof body.memberId === "string") {
              target = await adapter.findOne({ model: "member", where: [{ field: "id", value: body.memberId }] });
            }
            if (ctx.path === "/organization/remove-member" && typeof body.memberIdOrEmail === "string") {
              const v = body.memberIdOrEmail;
              if (v.includes("@")) {
                const u = await adapter.findOne<{ id: string }>({ model: "user", where: [{ field: "email", value: v.toLowerCase() }] });
                if (u && orgId) {
                  target = await adapter.findOne({ model: "member", where: [{ field: "userId", value: u.id }, { field: "organizationId", value: orgId }] });
                }
              } else {
                target = await adapter.findOne({ model: "member", where: [{ field: "id", value: v }] });
              }
            }
            if (ctx.path === "/organization/cancel-invitation" && typeof body.invitationId === "string") {
              const inv = await adapter.findOne<{ organizationId: string }>({ model: "invitation", where: [{ field: "id", value: body.invitationId }] });
              if (inv) orgId = inv.organizationId;
            }
            if (target && !orgId) orgId = target.organizationId;
            if (!orgId) return; // Better Auth reports the missing organization
            const mine = await effectiveRole(adapter, s.user.id, orgId);
            if (!mine) return; // not a member: Better Auth reports it
            if (!can(mine, ORG_ACTIONS[ctx.path]!)) {
              throw forbid("NOT_ALLOWED", `Your role here (${mine}) can't do that.`);
            }
            const wanted = body.role === undefined ? null : Array.isArray(body.role) ? body.role.join(",") : String(body.role);
            if (wanted !== null && rank(wanted) > rank(mine)) {
              throw forbid("ROLE_ABOVE_YOURS", `You can't give the ${wanted} role: it's above your own (${mine}).`);
            }
            if (target && target.organizationId === orgId && rank(target.role) > rank(mine)) {
              throw forbid("MEMBER_ABOVE_YOU", `You can't change a member whose role (${target.role}) is above yours (${mine}).`);
            }
          }),
        },
      ],
      after: [
        {
          // Better Auth decrypts the access and refresh tokens it returns; the ID token is ours to open.
          matcher: (ctx) => !!ctx.path && TOKEN_PATHS.has(ctx.path),
          handler: createAuthMiddleware(async (ctx) => {
            const r = ctx.context.returned as { idToken?: unknown } | undefined;
            if (!r || r instanceof Error || typeof r !== "object" || typeof r.idToken !== "string" || !sealed(r.idToken)) return;
            return ctx.json({ ...r, idToken: await symmetricDecrypt({ key: holder.secret!, data: r.idToken }) });
          }),
        },
      ],
    },
  } satisfies BetterAuthPlugin;

  const plugins: BetterAuthPlugin[] = [];
  plugins.push(apiKey({ enableMetadata: true, enableSessionForAPIKeys: true, defaultPrefix: "tfk_", rateLimit: { enabled: false } }) as unknown as BetterAuthPlugin);
  plugins.push(tiffin);
  if (c.captcha) plugins.push(altcha({ hmacSecret: c.secret }) as unknown as BetterAuthPlugin);
  if (methods.has("magic-link")) {
    plugins.push(
      magicLink({
        expiresIn: 600,
        sendMagicLink: async ({ email, url }) => mail(templates.magicLink(brand, email, url, 600)),
      }),
    );
  }
  if (methods.has("otp")) {
    plugins.push(
      emailOTP({
        otpLength: 6,
        expiresIn: 300,
        allowedAttempts: 5,
        sendVerificationOTP: async ({ email, otp, type }) => mail(templates.otp(brand, email, otp, type, 300)),
      }),
    );
  }
  if (methods.has("passkey")) plugins.push(passkeys(c));
  plugins.push(
    twoFactor({
      issuer: c.appName,
      // Two-step sign-in by email code (TOTP apps keep working as before).
      otpOptions: { period: 3, sendOTP: async ({ user, otp }) => void (await mail(templates.twoFactor(brand, user.email, otp, 180))) },
    }),
  );
  if (c.organizations) {
    plugins.push(
      organization({
        ac,
        roles,
        creatorRole: "owner",
        invitationExpiresIn: 48 * 3600,
        cancelPendingInvitationsOnReInvite: true,
        requireEmailVerificationOnInvitation: c.requireEmailVerification,
        sendInvitationEmail: async (d, request) => {
          const url = `${originOf(request)}${c.acceptInvitePath}?invitation=${encodeURIComponent(d.id)}`;
          await mail(templates.invite(brand, d.email, url, d.inviter.user, d.organization.name, d.role, 48 * 3600));
        },
      }),
    );
    plugins.push(
      inviteLinks({
        linkURL: (token, request) => `${originOf(request)}${c.acceptInvitePath}?link=${encodeURIComponent(token)}`,
        claimUse: async (id) => {
          const r = await pool.query(
            `UPDATE "${SCHEMA}"."orgInviteLink" SET uses = uses + 1 WHERE id = $1 AND uses < "maxUses" AND "revokedAt" IS NULL AND "expiresAt" > now()`,
            [id],
          );
          return r.rowCount ?? 0;
        },
      }) as unknown as BetterAuthPlugin,
    );
  }
  plugins.push(
    jwt({
      // Also signs the session cookie cache, so apps verify it with the public JWKS.
      sessionCookieCache: true,
      jwks: { keyPairConfig: { alg: "EdDSA", crv: "Ed25519" } },
      jwt: {
        issuer: c.primaryUrl.replace(/\/+$/, ""),
        audience: c.primaryUrl.replace(/\/+$/, ""),
        expirationTime: "15m",
        definePayload: async ({ user, session }) => {
          const orgId = (session as { activeOrganizationId?: string | null }).activeOrganizationId ?? null;
          let role: string | null = null;
          if (orgId && holder.adapter) role = await effectiveRole(holder.adapter, user.id, orgId);
          return { email: user.email, name: user.name, emailVerified: user.emailVerified, org: orgId, role };
        },
      },
    }),
  );
  plugins.push(sessionCacheOrg(c));

  // Sign-in providers: the ones turned on that have keys.
  const enabled = new Set(SOCIAL.filter((id) => methods.has(id) && c.social[id]));
  const social = socialProviders(c.social, enabled);
  const oidc = enabled.has("oidc") ? oidcPlugin(c.social.oidc) : null;
  if (oidc) plugins.push(oidc);
  // Box-wide keys go out through the box's one callback URL.
  // The app's own keys come back through its sign-in host, so the provider needs one redirect URI.
  const proxied = new Set([...enabled].filter((id) => c.social[id]?.proxied));
  const viaApp = new Set(c.oauthProxy?.appUrl ? [...enabled].filter((id) => !c.social[id]?.proxied) : []);
  if (c.oauthProxy && ((proxied.size && c.oauthProxy.url) || viaApp.size)) {
    plugins.push(bindProxyState(c.oauthProxy));
    plugins.push(oauthProxies(c.oauthProxy, c.oauthProxy.url ? proxied : new Set(), viaApp));
  }

  const hosts = c.hosts.flatMap((h) => [h, `${h}:*`]);

  return {
    appName: c.appName,
    secret: c.secret,
    basePath: "/api/auth",
    baseURL: { allowedHosts: hosts, fallback: c.primaryUrl, protocol: c.primaryUrl.startsWith("http://") ? "http" : "https" },
    // The app's own origins only (and Apple's, which posts its callback).
    trustedOrigins: enabled.has("apple") && !proxied.has("apple") ? [...c.origins, APPLE_ORIGIN] : c.origins,
    database: { dialect: new PostgresDialect({ pool }), type: "postgres", schemaName: SCHEMA, transaction: true } as never,
    telemetry: { enabled: false },
    logger: {
      level: "warn",
      log: (level, message) => console.log(JSON.stringify({ level, msg: message, project, source: "better-auth" })),
    },
    rateLimit: {
      enabled: c.rateLimit,
      storage: "memory",
      // Reading the session isn't an attempt at anything, and apps read it
      // server-side for every page their visitors load.
      customRules: { "/tiffin/session": false, "/get-session": false, "/jwks": false },
    },
    advanced: {
      cookiePrefix: "tiffin",
      useSecureCookies: !c.primaryUrl.startsWith("http://"),
      ipAddress: { ipAddressHeaders: ["x-forwarded-for"] },
    },
    session: {
      expiresIn: 30 * 86_400,
      updateAge: 86_400,
      cookieCache: { enabled: true, strategy: "jwt", maxAge: SESSION_CACHE_SECONDS },
    },
    user: {
      // Changing the email: the current address approves the move first.
      changeEmail: {
        enabled: true,
        sendChangeEmailConfirmation: async ({ user, newEmail, url }) => mail(templates.changeEmail(brand, user.email, url, newEmail, 3600)),
      },
      additionalFields: {
        banned: { type: "boolean", required: false, defaultValue: false, input: false },
        banReason: { type: "string", required: false, input: false },
        banExpires: { type: "date", required: false, input: false },
      },
    },
    account: {
      accountLinking: { enabled: true, trustedProviders: TRUSTED_FOR_LINKING },
      // Provider tokens are encrypted with the project's secret before they're stored (ID tokens: databaseHooks.account).
      encryptOAuthTokens: true,
    },
    emailAndPassword: {
      enabled: methods.has("email"),
      requireEmailVerification: c.requireEmailVerification,
      minPasswordLength: 8,
      maxPasswordLength: 256,
      resetPasswordTokenExpiresIn: 3600,
      revokeSessionsOnPasswordReset: true,
      sendResetPassword: async ({ user, url }) => mail(templates.reset(brand, user.email, url, 3600)),
    },
    emailVerification: {
      sendOnSignUp: c.requireEmailVerification,
      sendOnSignIn: c.requireEmailVerification,
      autoSignInAfterVerification: true,
      expiresIn: 3600,
      sendVerificationEmail: async ({ user, url }) => mail(templates.verify(brand, user.email, url, 3600)),
    },
    socialProviders: social,
    databaseHooks: {
      account: {
        create: { before: async (account) => ({ data: await sealIdToken(holder.secret, account) }) },
        update: { before: async (account) => ({ data: await sealIdToken(holder.secret, account) }) },
      },
      user: {
        update: {
          // Keep the old address so it can be told the account moved (after).
          before: async (data, ctx) => {
            if (typeof data.email !== "string") return;
            const was = await emailBefore(ctx, holder.adapter);
            if (was && was.toLowerCase() !== data.email.toLowerCase()) movedFrom.set(data.email.toLowerCase(), was);
          },
          after: async (user) => {
            const key = user.email.toLowerCase();
            const was = movedFrom.get(key);
            if (!was) return;
            movedFrom.delete(key);
            await mail(templates.emailChanged(brand, was, user.email, new Date())).catch((err) =>
              console.error(JSON.stringify({ level: "error", msg: "email-changed notice failed", project, err: String(err) })),
            );
          },
        },
        create: {
          before: async () => {
            currentFacts().newUser = true;
          },
          after: async (user) => {
            if (!c.organizations || !holder.adapter) return;
            await personalOrg(await txAdapter(holder.adapter), user.id);
          },
        },
      },
      session: {
        create: {
          before: async (session) => {
            if (!holder.adapter) return;
            const u = await holder.adapter.findOne<{ banned?: boolean; banExpires?: Date | null }>({ model: "user", where: [{ field: "id", value: session.userId }] });
            if (u?.banned && (!u.banExpires || new Date(u.banExpires) > new Date())) {
              throw forbid("BANNED", "This account is suspended. Contact the app's owner if you think this is a mistake.");
            }
            const s = session as typeof session & { activeOrganizationId?: string | null };
            if (!c.organizations || s.activeOrganizationId) return;
            const adapter = await txAdapter(holder.adapter);
            const first = await firstOrg(adapter, session.userId);
            if (first) return { data: { ...session, activeOrganizationId: first } };
            // Signed in as the account is made (no email confirmation): the
            // personal org would only come once the account is saved, after
            // this session. Make it now, so the session starts in it.
            if (currentFacts().newUser) return { data: { ...session, activeOrganizationId: await personalOrg(adapter, session.userId) } };
          },
        },
      },
    },
    plugins,
  };
}

// Old addresses of accounts whose email is being changed, by the new
// address, between the update's before and after hooks.
const movedFrom = new Map<string, string>();

type HookCtx = { query?: Record<string, unknown>; body?: Record<string, unknown>; context?: { session?: { user?: { email?: string } } | null } } | null;

/**
 * The address an account had before an update changes it: from the change
 * link's token (already checked by Better Auth), the signed-in user, or the
 * user an admin call names.
 */
async function emailBefore(ctx: unknown, adapter?: Adapter): Promise<string | undefined> {
  const c = ctx as HookCtx;
  const token = c?.query?.token;
  if (typeof token === "string" && token.split(".").length === 3) {
    try {
      const payload = JSON.parse(Buffer.from(token.split(".")[1]!, "base64url").toString("utf8")) as { email?: unknown };
      if (typeof payload.email === "string") return payload.email;
    } catch {}
  }
  const own = c?.context?.session?.user?.email;
  if (typeof own === "string") return own;
  const id = c?.body?.userId;
  if (typeof id === "string" && adapter) {
    const u = await adapter.findOne<{ email: string }>({ model: "user", where: [{ field: "id", value: id }] });
    return u?.email;
  }
  return undefined;
}

export function createPool(project: string, databaseUrl: string): pg.Pool {
  const pool = new pg.Pool({
    connectionString: databaseUrl,
    max: 4,
    idleTimeoutMillis: 30_000,
    connectionTimeoutMillis: 10_000,
    options: `-c search_path=${SCHEMA},public`,
    application_name: "tiffin-auth",
  });
  pool.on("error", (err) => console.error(JSON.stringify({ level: "error", msg: "postgres connection error", project, err: String(err) })));
  return pool;
}

/** A project's Better Auth instance on a pool the registry owns. */
export function createInstance(project: string, c: ProjectConfig, pool: pg.Pool): Instance {
  return { project, config: c, auth: betterAuth(buildOptions(project, c, pool)), pool };
}
