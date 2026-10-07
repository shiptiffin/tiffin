// The engine's config file. The Go auth module writes it (0600) and asks the
// engine to reload; nothing else writes it. One file describes every
// auth-enabled project on the box.
import { readFileSync, statSync } from "node:fs";
import { z } from "zod";

/** Sign-in services: Better Auth's provider IDs ("oidc" is the generic OpenID Connect one). */
export const SOCIAL = ["google", "github", "apple", "microsoft", "discord", "facebook", "twitter", "linkedin", "gitlab", "slack", "twitch", "oidc"] as const;
export type Social = (typeof SOCIAL)[number];

export const METHODS = ["email", "magic-link", "otp", "passkey", ...SOCIAL] as const;
export type Method = (typeof METHODS)[number];

export const isSocial = (m: string): m is Social => (SOCIAL as readonly string[]).includes(m);

const oauthApp = z.object({
  clientId: z.string().min(1),
  clientSecret: z.string().min(1),
  /** Microsoft: directory (tenant) ID, or common (default), organizations, consumers. */
  tenantId: z.string().optional(),
  /** GitLab: a self-managed instance's URL. OpenID Connect: the issuer (its discovery document is read). */
  issuer: z.string().url().optional(),
  /** OpenID Connect: the button's name. */
  label: z.string().optional(),
  /** The box-wide keys: the provider calls back to the box's one callback URL (see oauthProxy). */
  proxied: z.boolean().default(false),
});
export type OAuthApp = z.infer<typeof oauthApp>;

const socialApps = z.partialRecord(z.enum(SOCIAL), oauthApp).default({});

export const projectSchema = z.object({
  /** Signs cookies and encrypts JWKS private keys. 32+ random bytes, base64. */
  secret: z.string().min(32),
  /** The project's own Postgres; auth lives in its `tiffin_auth` schema. */
  databaseUrl: z.string().min(1),
  /** smtp://host:port (the box's relay or dev inbox). Empty: emails are logged, not sent. */
  smtpUrl: z.string().default(""),
  /** Sender for auth emails. */
  emailFrom: z.string().min(3),
  /** Shown in emails and the authenticator app. */
  appName: z.string().min(1),
  /**
   * How the app's emails look. logoUrl: the project's icon, a square PNG over https
   * (shown at 32x32). accent: the button colour, set only by auth.emailAccent in the
   * manifest; absent, the button is the dashboard's brass.
   */
  emailBrand: z
    .object({
      logoUrl: z.string().url().startsWith("https://").optional(),
      accent: z.string().regex(/^#[0-9a-fA-F]{6}$/).optional(),
    })
    .optional(),
  /** Every host a web app of the project serves (no scheme, no port). */
  hosts: z.array(z.string().min(1)).min(1),
  /** Public URL used when a request's host can't be used (emails sent from internal calls). */
  primaryUrl: z.string().url(),
  /** Origins allowed to call the endpoint (the app routes, with scheme and port). */
  origins: z.array(z.string()).default([]),
  methods: z.array(z.enum(METHODS)).min(1),
  organizations: z.boolean().default(true),
  /** OAuth apps from the project's secrets. Absent: that button explains how to set it up. */
  social: socialApps,
  /** Set when a provider uses the box-wide keys: sign-ins go out through the box's one callback URL. */
  /**
   * Sign-ins through one callback URL. url: the box's (dashboard host), for providers on the box-wide keys.
   * appUrl: the app's sign-in host, for providers on the project's own keys (sign-ins on its other hosts come back through it).
   */
  oauthProxy: z.object({ url: z.string().url().optional(), appUrl: z.string().url().optional(), secret: z.string().min(32) }).optional(),
  /** Proof-of-work captcha on sign-up and sign-in. Default on. */
  captcha: z.boolean().default(true),
  /** Better Auth's built-in rate limiter. Default on. */
  rateLimit: z.boolean().default(true),
  /** Where invitation emails point: <origin><path>?invitation=<id>. */
  acceptInvitePath: z.string().default("/accept-invite"),
  /** New users confirm their email before signing in. The box decides (auth.emailVerification, or on once a relay sends real mail). */
  requireEmailVerification: z.boolean().default(true),
  /**
   * Production mail can't reach people yet (no relay): email + password sign-up, magic links, one-time codes,
   * resets and verification mail are refused, except on previewHosts (their mail lands in the dev inbox).
   */
  emailBlocked: z.boolean().default(false),
  previewHosts: z.array(z.string()).default([]),
});

export type ProjectConfig = z.infer<typeof projectSchema>;

export const configSchema = z.object({
  version: z.literal(1),
  /** Extra addresses to serve on besides --listen (the runtime's bridge IP, so app containers reach the engine). */
  listen: z.array(z.string()).default([]),
  projects: z.record(z.string().regex(/^[a-z][a-z0-9-]{0,39}$/), projectSchema),
  /**
   * The box's one callback URL for box-wide sign-in keys: requests to
   * https://<host>/api/auth/callback/<provider> (the dashboard host; the box
   * forwards them) finish the provider's side and hand the sign-in to the app.
   */
  proxy: z
    .object({ url: z.string().url(), host: z.string().min(1), secret: z.string().min(32), social: socialApps })
    .optional(),
});

export type ProxyConfig = NonNullable<z.infer<typeof configSchema>["proxy"]>;

export type EngineConfig = z.infer<typeof configSchema>;

export function parseConfig(raw: unknown): EngineConfig {
  const r = configSchema.safeParse(raw);
  if (!r.success) {
    const first = r.error.issues[0];
    throw new Error(`auth config invalid at ${first?.path.join(".") || "(root)"}: ${first?.message}`);
  }
  return r.data;
}

export function readConfig(path: string): { config: EngineConfig; mtimeMs: number } {
  let mtimeMs = 0;
  try {
    mtimeMs = statSync(path).mtimeMs;
  } catch {
    return { config: { version: 1, listen: [], projects: {} }, mtimeMs: 0 };
  }
  return { config: parseConfig(JSON.parse(readFileSync(path, "utf8"))), mtimeMs };
}

/** A stable fingerprint of one project's config, to know when to rebuild its instance. */
export function fingerprint(p: ProjectConfig): string {
  return new Bun.CryptoHasher("sha256").update(JSON.stringify(p)).digest("hex");
}
