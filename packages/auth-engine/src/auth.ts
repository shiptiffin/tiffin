// Builds one project's Better Auth instance from its config. Every project on
// the box gets its own instance (own secret, own database, own settings)
// inside the one engine process.
import { betterAuth } from "better-auth";
import type { BetterAuthOptions, BetterAuthPlugin } from "better-auth";
import { APIError, createAuthEndpoint, createAuthMiddleware, getSessionFromCtx } from "better-auth/api";
import { emailOTP, jwt, magicLink, organization, twoFactor } from "better-auth/plugins";
import { apiKey } from "@better-auth/api-key";
import { passkey } from "@better-auth/passkey";
import { PostgresDialect } from "kysely";
import pg from "pg";
import { z } from "zod";
import { altcha } from "./altcha";
import type { ProjectConfig } from "./config";
import { currentFacts } from "./context";
import { effectiveRole, inviteLinks } from "./invite-links";
import { send, templates } from "./mail";
import { ac, rank, roles, weaker } from "./roles";

export const SCHEMA = "auth";

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

/** The Better Auth options for a project; also what migrations are computed from. */
export function buildOptions(project: string, c: ProjectConfig, pool: pg.Pool): BetterAuthOptions {
  const methods = new Set(c.methods);
  const holder: { adapter?: Adapter } = {};
  const mail = (m: Parameters<typeof send>[3]) => send(project, c.smtpUrl, `${c.appName} <${c.emailFrom}>`, m);
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
    },
    endpoints: {
      tiffinConfig: createAuthEndpoint("/tiffin/config", { method: "GET" }, async (ctx) =>
        ctx.json({
          appName: c.appName,
          methods: c.methods,
          organizations: c.organizations,
          captcha: c.captcha,
          social: {
            google: methods.has("google") ? { configured: !!c.social.google } : null,
            github: methods.has("github") ? { configured: !!c.social.github } : null,
          },
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
          let org: { id: string; name: string; slug: string; role: string | null; memberRole: string | null } | null = null;
          if (orgId && c.organizations) {
            const adapter = ctx.context.adapter as unknown as Adapter;
            const o = await adapter.findOne<{ id: string; name: string; slug: string }>({ model: "organization", where: [{ field: "id", value: orgId }] });
            const m = await adapter.findOne<{ role: string }>({
              model: "member",
              where: [
                { field: "userId", value: s.user.id },
                { field: "organizationId", value: orgId },
              ],
            });
            if (o && m) {
              const cap = facts.apiKey?.maxRole ?? "owner";
              org = { id: o.id, name: o.name, slug: o.slug, memberRole: m.role, role: weaker(m.role, cap) };
            }
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
          // Sign in with Google/GitHub when the project has no OAuth app yet: say how to fix it.
          matcher: (ctx) => ctx.path === "/sign-in/social" || ctx.path === "/link-social",
          handler: createAuthMiddleware(async (ctx) => {
            const p = (ctx.body as { provider?: string } | undefined)?.provider;
            if (p !== "google" && p !== "github") return;
            const name = p === "google" ? "Google" : "GitHub";
            const env = p.toUpperCase();
            if (!methods.has(p)) {
              throw new APIError("BAD_REQUEST", { code: "METHOD_DISABLED", message: `Sign in with ${name} isn't turned on for this app. Add "${p}" to services.auth.methods in tiffin.config.ts.` });
            }
            if (!c.social[p]) {
              throw new APIError("SERVICE_UNAVAILABLE", {
                code: "SOCIAL_NOT_CONFIGURED",
                message: `Sign in with ${name} isn't set up yet. The app's owner needs to create a ${name} OAuth app with the callback URL ${originOf(ctx.request)}/api/auth/callback/${p}, then set the ${env}_CLIENT_ID and ${env}_CLIENT_SECRET secrets on this project.`,
              });
            }
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
        sendMagicLink: async ({ email, url }) => mail(templates.magicLink(c.appName, email, url)),
      }),
    );
  }
  if (methods.has("otp")) {
    plugins.push(
      emailOTP({
        otpLength: 6,
        expiresIn: 300,
        allowedAttempts: 5,
        sendVerificationOTP: async ({ email, otp, type }) => mail(templates.otp(c.appName, email, otp, type)),
      }),
    );
  }
  if (methods.has("passkey")) {
    plugins.push(
      passkey({
        rpID: c.hosts[0],
        rpName: c.appName,
        origin: c.origins.length ? c.origins : [c.primaryUrl.replace(/\/+$/, "")],
      }) as unknown as BetterAuthPlugin,
    );
  }
  plugins.push(twoFactor({ issuer: c.appName }));
  if (c.organizations) {
    plugins.push(
      organization({
        ac,
        roles,
        creatorRole: "owner",
        invitationExpiresIn: 48 * 3600,
        cancelPendingInvitationsOnReInvite: true,
        requireEmailVerificationOnInvitation: true,
        sendInvitationEmail: async (d, request) => {
          const url = `${originOf(request)}${c.acceptInvitePath}?invitation=${encodeURIComponent(d.id)}`;
          await mail(templates.invite(c.appName, d.email, url, d.inviter.user.name || d.inviter.user.email, d.organization.name, d.role));
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

  const hosts = c.hosts.flatMap((h) => [h, `${h}:*`]);
  const social: NonNullable<BetterAuthOptions["socialProviders"]> = {};
  if (methods.has("google") && c.social.google) social.google = { ...c.social.google, prompt: "select_account" };
  if (methods.has("github") && c.social.github) social.github = { ...c.social.github };

  return {
    appName: c.appName,
    secret: c.secret,
    basePath: "/api/auth",
    baseURL: { allowedHosts: hosts, fallback: c.primaryUrl, protocol: c.primaryUrl.startsWith("http://") ? "http" : "https" },
    trustedOrigins: c.origins,
    database: { dialect: new PostgresDialect({ pool }), type: "postgres", schemaName: SCHEMA, transaction: true } as never,
    telemetry: { enabled: false },
    logger: {
      level: "warn",
      log: (level, message) => console.log(JSON.stringify({ level, msg: message, project, source: "better-auth" })),
    },
    rateLimit: { enabled: c.rateLimit, storage: "memory" },
    advanced: {
      cookiePrefix: "tiffin",
      useSecureCookies: !c.primaryUrl.startsWith("http://"),
      ipAddress: { ipAddressHeaders: ["x-forwarded-for"] },
    },
    session: { expiresIn: 30 * 86_400, updateAge: 86_400 },
    user: {
      additionalFields: {
        banned: { type: "boolean", required: false, defaultValue: false, input: false },
        banReason: { type: "string", required: false, input: false },
        banExpires: { type: "date", required: false, input: false },
      },
    },
    account: { accountLinking: { enabled: true, trustedProviders: ["google", "github"] } },
    emailAndPassword: {
      enabled: methods.has("email"),
      requireEmailVerification: true,
      minPasswordLength: 8,
      maxPasswordLength: 256,
      resetPasswordTokenExpiresIn: 3600,
      revokeSessionsOnPasswordReset: true,
      sendResetPassword: async ({ user, url }) => mail(templates.reset(c.appName, user.email, url)),
    },
    emailVerification: {
      sendOnSignUp: true,
      sendOnSignIn: true,
      autoSignInAfterVerification: true,
      expiresIn: 3600,
      sendVerificationEmail: async ({ user, url }) => mail(templates.verify(c.appName, user.email, url)),
    },
    socialProviders: social,
    databaseHooks: {
      user: {
        create: {
          after: async (user) => {
            if (!c.organizations || !holder.adapter) return;
            // Everyone is an org: each person gets a personal one to own things in.
            const now = new Date();
            const org = await holder.adapter.create<{ id: string }>({
              model: "organization",
              data: {
                name: "Personal",
                slug: `personal-${user.id.toLowerCase().replace(/[^a-z0-9]/g, "").slice(0, 24)}`,
                createdAt: now,
                metadata: JSON.stringify({ personal: true }),
              },
            });
            await holder.adapter.create({ model: "member", data: { organizationId: org.id, userId: user.id, role: "owner", createdAt: now } });
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
            const ms = await holder.adapter.findMany<{ organizationId: string; createdAt: Date }>({
              model: "member",
              where: [{ field: "userId", value: session.userId }],
              sortBy: { field: "createdAt", direction: "asc" },
              limit: 1,
            });
            if (ms[0]) return { data: { ...session, activeOrganizationId: ms[0].organizationId } };
          },
        },
      },
    },
    plugins,
  };
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
