// What the box's auth engine says about this app, and its errors in words
// for people. For sign-in, sign-up and sessions in the browser, use Better
// Auth's own client (better-auth/client or better-auth/react) with
// basePath "/api/auth": the box serves it on every app host.

export interface AuthOptions {
  /** Origin of the app; default this page's. */
  baseURL?: string;
  fetch?: typeof fetch;
}

/** What the engine says this app supports (GET /api/auth/tiffin/config). */
export interface AuthConfig {
  appName: string;
  /** "password", "magic-link", "email-otp", "passkey"... */
  methods: string[];
  organizations: boolean;
  /**
   * Email + password sign-up, magic links, one-time codes and resets work
   * here. False in production until the box can send email (they answer
   * EMAIL_NOT_SET_UP): show passkeys and sign-in providers only.
   */
  emailReady: boolean;
  /** Sign-up and sign-in calls need a solved bot check (see prepareCaptcha). */
  captcha: boolean;
  /**
   * For every sign-in service (google, github, apple, microsoft, discord,
   * facebook, twitter, linkedin, gitlab, slack, twitch, oidc): null when it
   * isn't turned on, else whether it has keys yet.
   */
  social: Record<SocialProvider, { configured: boolean } | null>;
  /**
   * The sign-in buttons to show, in order: each service turned on, with its
   * name ("Google", "X", or the OpenID Connect provider's own name, like
   * "Okta"). Sign in with `signIn.social({ provider: id })` from Better
   * Auth's client. A button that isn't configured yet answers
   * SOCIAL_NOT_CONFIGURED with how to set it up: hide it from visitors.
   */
  providers: Array<{ id: SocialProvider; name: string; configured: boolean }>;
}

/** Better Auth's IDs of the sign-in services the box supports ("oidc": any OpenID Connect provider). */
export type SocialProvider = "google" | "github" | "apple" | "microsoft" | "discord" | "facebook" | "twitter" | "linkedin" | "gitlab" | "slack" | "twitch" | "oidc";

export const baseOf = (o: AuthOptions): string =>
  (o.baseURL ?? (typeof window !== "undefined" ? window.location.origin : "")).replace(/\/+$/, "");

const configs = new Map<string, Promise<AuthConfig>>();

/** The app's auth config, fetched once per origin. */
export function authConfig(o: AuthOptions = {}): Promise<AuthConfig> {
  const base = baseOf(o);
  let p = configs.get(base);
  if (!p) {
    p = (o.fetch ?? fetch)(`${base}/api/auth/tiffin/config`, { credentials: "include" }).then(async (r) => {
      if (!r.ok) throw new Error(`The auth endpoint answered ${r.status}. Is services.auth on in tiffin.config.ts?`);
      return (await r.json()) as AuthConfig;
    });
    p.catch(() => configs.delete(base));
    configs.set(base, p);
  }
  return p;
}

/** Turns a Better Auth client error into words for people. */
export function errorText(e: unknown, fallback = "Something went wrong. Try again."): string {
  const err = e as { message?: string; code?: string; status?: number } | null;
  const code = err?.code ?? "";
  const known: Record<string, string> = {
    INVALID_EMAIL_OR_PASSWORD: "That email and password don't match. Try again, or reset your password.",
    EMAIL_NOT_VERIFIED: "Confirm your email first. We just sent you a new link.",
    USER_ALREADY_EXISTS: "There's already an account with this email. Sign in instead.",
    USER_ALREADY_EXISTS_USE_ANOTHER_EMAIL: "There's already an account with this email. Sign in instead.",
    PASSWORD_TOO_SHORT: "Use at least 8 characters for your password.",
    INVALID_OTP: "That code isn't right. Check the email and try again.",
    OTP_EXPIRED: "That code has expired. Ask for a new one.",
    TOO_MANY_ATTEMPTS: "Too many tries. Ask for a new code.",
    INVALID_CODE: "That code isn't right. Try the current one from your app.",
    BANNED: "This account is suspended. Contact the app's owner if you think this is a mistake.",
    CAPTCHA_REQUIRED: "The bot check didn't run. Reload the page and try again.",
    CAPTCHA_EXPIRED: "The bot check expired. Press the button again.",
  };
  if (known[code]) return known[code]!;
  if (err?.status === 429) return "Too many tries in a row. Wait a few seconds and try again.";
  return err?.message || fallback;
}
