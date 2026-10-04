// The engine's config file. The Go auth module writes it (0600) and asks the
// engine to reload; nothing else writes it. One file describes every
// auth-enabled project on the box.
import { readFileSync, statSync } from "node:fs";
import { z } from "zod";

export const METHODS = ["email", "magic-link", "otp", "passkey", "google", "github"] as const;
export type Method = (typeof METHODS)[number];

const oauthApp = z.object({ clientId: z.string().min(1), clientSecret: z.string().min(1) });

export const projectSchema = z.object({
  /** Signs cookies and encrypts JWKS private keys. 32+ random bytes, base64. */
  secret: z.string().min(32),
  /** The project's own Postgres; auth lives in its `auth` schema. */
  databaseUrl: z.string().min(1),
  /** smtp://host:port (the box's relay or dev inbox). Empty: emails are logged, not sent. */
  smtpUrl: z.string().default(""),
  /** Sender for auth emails. */
  emailFrom: z.string().min(3),
  /** Shown in emails and the authenticator app. */
  appName: z.string().min(1),
  /** Every host a web app of the project serves (no scheme, no port). */
  hosts: z.array(z.string().min(1)).min(1),
  /** Public URL used when a request's host can't be used (emails sent from internal calls). */
  primaryUrl: z.string().url(),
  /** Origins allowed to call the endpoint (the app routes, with scheme and port). */
  origins: z.array(z.string()).default([]),
  methods: z.array(z.enum(METHODS)).min(1),
  organizations: z.boolean().default(true),
  /** OAuth apps from the project's secrets. Absent: that button explains how to set it up. */
  social: z
    .object({ google: oauthApp.optional(), github: oauthApp.optional() })
    .default({}),
  /** Proof-of-work captcha on sign-up and sign-in. Default on. */
  captcha: z.boolean().default(true),
  /** Better Auth's built-in rate limiter. Default on. */
  rateLimit: z.boolean().default(true),
  /** Where invitation emails point: <origin><path>?invitation=<id>. */
  acceptInvitePath: z.string().default("/accept-invite"),
  /** New users confirm their email before signing in. The box decides (auth.emailVerification, or on once a relay sends real mail). */
  requireEmailVerification: z.boolean().default(true),
});

export type ProjectConfig = z.infer<typeof projectSchema>;

export const configSchema = z.object({
  version: z.literal(1),
  /** Extra addresses to serve on besides --listen (the runtime's bridge IP, so app containers reach the engine). */
  listen: z.array(z.string()).default([]),
  projects: z.record(z.string().regex(/^[a-z][a-z0-9-]{0,39}$/), projectSchema),
});

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
