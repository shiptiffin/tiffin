"use client";
// The auth client every component shares: Better Auth's client with the
// plugins the box's engine serves, pointed at /api/auth on this app's host.
import { apiKeyClient } from "@better-auth/api-key/client";
import { passkeyClient } from "@better-auth/passkey/client";
import { emailOTPClient, magicLinkClient, organizationClient, twoFactorClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";
import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { solveChallenge } from "altcha-lib";
import { deriveKey } from "altcha-lib/algorithms/web/sha";
import type { Challenge } from "altcha-lib";

// Named from its parts (not inferred), so the published .d.ts stays small and
// portable. organizationClient is generic: pin its options to {}.
type Plugins = [
  ReturnType<typeof organizationClient<{}>>,
  ReturnType<typeof magicLinkClient>,
  ReturnType<typeof emailOTPClient>,
  ReturnType<typeof twoFactorClient>,
  ReturnType<typeof passkeyClient>,
  ReturnType<typeof apiKeyClient>,
];

/** Better Auth's React client with the plugins the box's engine serves. */
export type TiffinAuthClient = ReturnType<typeof createAuthClient<{ plugins: Plugins }>>;

export function createTiffinAuth(opts: { baseURL?: string } = {}): TiffinAuthClient {
  return createAuthClient({
    baseURL: opts.baseURL ?? (typeof window !== "undefined" ? window.location.origin : undefined),
    basePath: "/api/auth",
    plugins: [organizationClient(), magicLinkClient(), emailOTPClient(), twoFactorClient(), passkeyClient(), apiKeyClient()],
  });
}

/** What the engine says this app supports (GET /api/auth/tiffin/config). */
export type AuthConfig = {
  appName: string;
  methods: string[];
  organizations: boolean;
  captcha: boolean;
  social: { google: { configured: boolean } | null; github: { configured: boolean } | null };
};

type Ctx = { client: TiffinAuthClient; baseURL: string; config: AuthConfig | null; configError: string | null };
const AuthContext = createContext<Ctx | null>(null);

let defaultClient: TiffinAuthClient | null = null;
const configCache = new Map<string, Promise<AuthConfig>>();

function loadConfig(baseURL: string): Promise<AuthConfig> {
  let p = configCache.get(baseURL);
  if (!p) {
    p = fetch(`${baseURL}/api/auth/tiffin/config`, { credentials: "include" }).then(async (r) => {
      if (!r.ok) throw new Error(`The auth endpoint answered ${r.status}. Is services.auth on in tiffin.config.ts?`);
      return (await r.json()) as AuthConfig;
    });
    p.catch(() => configCache.delete(baseURL));
    configCache.set(baseURL, p);
  }
  return p;
}

/**
 * Optional: share one client (and point components at another origin).
 * Without it, components talk to /api/auth on the current origin.
 */
export function TiffinAuthProvider({ baseURL, client, children }: { baseURL?: string; client?: TiffinAuthClient; children: ReactNode }) {
  const value = useAuthState(baseURL, client);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

function useAuthState(baseURL?: string, client?: TiffinAuthClient): Ctx {
  const url = (baseURL ?? (typeof window !== "undefined" ? window.location.origin : "")).replace(/\/+$/, "");
  const c = useMemo(() => {
    if (client) return client;
    if (baseURL) return createTiffinAuth({ baseURL: url });
    defaultClient ??= createTiffinAuth({ baseURL: url });
    return defaultClient;
  }, [client, baseURL, url]);
  const [config, setConfig] = useState<AuthConfig | null>(null);
  const [configError, setError] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    loadConfig(url).then(
      (v) => live && setConfig(v),
      (e) => live && setError(String(e?.message ?? e)),
    );
    return () => {
      live = false;
    };
  }, [url]);
  return { client: c, baseURL: url, config, configError };
}

/** The shared client and the app's auth config. */
export function useTiffinAuth(): Ctx {
  const ctx = useContext(AuthContext);
  const own = useAuthState();
  return ctx ?? own;
}

/**
 * Solves the engine's proof-of-work challenge in the background as soon as a
 * form mounts, so by the time someone presses the button it's usually done.
 * Each solution is good for one request.
 */
export function useCaptcha(baseURL: string, enabled: boolean) {
  const next = useRef<Promise<string> | null>(null);
  const solve = () =>
    (async () => {
      const r = await fetch(`${baseURL}/api/auth/altcha/challenge`, { credentials: "include" });
      if (!r.ok) throw new Error("Couldn't load the bot check. Check your connection and try again.");
      const challenge = (await r.json()) as Challenge;
      const solution = await solveChallenge({ challenge, deriveKey, timeout: 60_000 });
      if (!solution) throw new Error("The bot check took too long. Try again.");
      return btoa(JSON.stringify({ challenge, solution }));
    })();
  useEffect(() => {
    if (enabled && !next.current) {
      next.current = solve();
      next.current.catch(() => {
        next.current = null;
      });
    }
  }, [enabled, baseURL]);
  /** Headers for one protected request; starts solving the next one. */
  return async (): Promise<Record<string, string>> => {
    if (!enabled) return {};
    const p = next.current ?? solve();
    next.current = solve();
    next.current.catch(() => {
      next.current = null;
    });
    return { "x-captcha-response": await p };
  };
}

/**
 * The bot check for your own sign-in and sign-up forms that post to a Server
 * Action (signIn / signUp from tiffin-sdk/next/auth): a hidden `captcha`
 * field, solved in the background once the form mounts. Submitting before
 * it is ready waits for it; each submit gets a fresh one for the next try.
 *
 *   <form action={signInAction}><CaptchaField /> ...</form>
 */
export function CaptchaField({ name = "captcha" }: { name?: string }) {
  const { baseURL, config } = useTiffinAuth();
  const take = useCaptcha(baseURL, config?.captcha ?? false);
  const input = useRef<HTMLInputElement>(null);
  // A submit held until the check is ready (kept while the app's config loads).
  const held = useRef<{ by: HTMLElement | null } | null>(null);
  useEffect(() => {
    const el = input.current;
    const form = el?.form;
    if (!el || !form) return;
    const resubmit = () => {
      const h = held.current;
      held.current = null;
      if (h) form.requestSubmit(h.by ?? undefined);
    };
    if (config && !config.captcha) return resubmit();
    let state: "waiting" | "solving" | "ready" | "failed" = "waiting";
    let pending: Promise<void> = Promise.resolve();
    const fill = () => {
      state = "solving";
      el.value = "";
      pending = take().then(
        (h) => {
          el.value = h["x-captcha-response"] ?? "";
          state = "ready";
        },
        () => {
          state = "failed";
        },
      );
    };
    const whenReady = () => void pending.then(() => state === "ready" && resubmit());
    if (config) {
      fill();
      whenReady();
    }
    const onSubmit = (e: SubmitEvent) => {
      if (state === "ready") {
        setTimeout(fill); // after the form's data is read
        return;
      }
      e.preventDefault();
      e.stopPropagation(); // React handles submits at the root: it never sees this one
      held.current = { by: e.submitter as HTMLElement | null };
      if (state === "failed") fill();
      if (state !== "waiting") whenReady();
    };
    form.addEventListener("submit", onSubmit, true);
    return () => form.removeEventListener("submit", onSubmit, true);
  }, [config, baseURL]);
  return <input ref={input} type="hidden" name={name} defaultValue="" />;
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
    CAPTCHA_EXPIRED: "The bot check expired. Press the button again.",
  };
  if (known[code]) return known[code]!;
  if (err?.status === 429) return "Too many tries in a row. Wait a few seconds and try again.";
  return err?.message || fallback;
}
