// Connect with Google for managed boxes. A managed box has no Google OAuth
// app of its own, so its "Connect with Google" (Search Console) goes through
// ShipTiffin's: Google only sends people back to addresses registered in
// advance, and only the app's secret can trade a code or renew a token.
//
//   box  POST /api/box/google/start   {state, challenge}      -> {url}
//   Google -> GET /api/box/google/callback?code&state         -> 303 to the box's own callback
//   box  POST /api/box/google/token   {grant: "code" | "refresh", ...}
//
// Nothing is stored here. The state Google carries is signed and names the
// box; the PKCE verifier for Google is derived from it. The grant goes back
// to the box through the browser as a code only this site can open, good
// for two minutes, and only for the box that started (its licence) with the
// verifier whose hash it sent at the start (the box's own PKCE). The box
// keeps its refresh token, encrypted, and asks here for each hour's access
// token, since renewing needs the secret.
//
// Secrets (the website project): GOOGLE_RELAY_CLIENT_ID and
// GOOGLE_RELAY_CLIENT_SECRET, a Google OAuth app with the redirect URI
// <site>/api/box/google/callback. The signing and sealing keys are derived
// from the secret, so there is nothing else to set (and changing the secret
// only ends sign-ins in flight).
import { createCipheriv, createDecipheriv, createHash, createHmac, hkdfSync, randomBytes, timingSafeEqual } from "node:crypto";

export const SCOPE_SEARCH = "https://www.googleapis.com/auth/webmasters";
export const SCOPE_VERIFY = "https://www.googleapis.com/auth/siteverification";
export const SCOPES = `openid email ${SCOPE_SEARCH} ${SCOPE_VERIFY}`;
export const GOOGLE_AUTH = "https://accounts.google.com/o/oauth2/v2/auth";
export const GOOGLE_TOKEN = "https://oauth2.googleapis.com/token";
/** How long someone may take at Google, and how long the box has to collect the grant. */
export const STATE_TTL = 10 * 60;
export const HANDOFF_TTL = 2 * 60;

export type RelayApp = { clientId: string; clientSecret: string; redirect: string };

/** The relay's Google app, or null when its secrets aren't set. */
export function relayApp(env: Record<string, string | undefined> = process.env): RelayApp | null {
  const clientId = env.GOOGLE_RELAY_CLIENT_ID?.trim();
  const clientSecret = env.GOOGLE_RELAY_CLIENT_SECRET?.trim();
  if (!clientId || !clientSecret) return null;
  const site = (env.SITE_URL || "https://shiptiffin.com").replace(/\/+$/, "");
  return { clientId, clientSecret, redirect: `${site}/api/box/google/callback` };
}

const key = (app: RelayApp, use: string) => Buffer.from(hkdfSync("sha256", app.clientSecret, "tiffin-google-relay", use, 32));
const b64 = (b: Buffer | string) => Buffer.from(b).toString("base64url");
const nowS = (now: Date) => Math.floor(now.getTime() / 1000);

/** What the box sends to start: its own state and the S256 hash of its verifier. */
export function validStart(state: unknown, challenge: unknown): state is string {
  return typeof state === "string" && /^[A-Za-z0-9._~-]{16,128}$/.test(state) && typeof challenge === "string" && /^[A-Za-z0-9_-]{43}$/.test(challenge);
}

/** The box's own callback: where the browser goes back to. Its dashboard, by the licence's domain. */
export function boxCallback(domain: string): string | null {
  if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(domain) || domain.length > 200) return null;
  return `https://dashboard.${domain}/api/auth/callback/google`;
}

export type RelayState = { b: string; d: string; s: string; c: string; e: number };

/** The state Google carries: who started, signed. */
export function signState(app: RelayApp, st: Omit<RelayState, "e">, now = new Date()): string {
  const p = b64(JSON.stringify({ ...st, e: nowS(now) + STATE_TTL } satisfies RelayState));
  return `${p}.${b64(createHmac("sha256", key(app, "state")).update(p).digest())}`;
}

export function openState(app: RelayApp, state: string, now = new Date()): RelayState | null {
  const i = state.indexOf(".");
  if (i <= 0 || state.length > 2000) return null;
  const p = state.slice(0, i);
  const want = createHmac("sha256", key(app, "state")).update(p).digest();
  const got = Buffer.from(state.slice(i + 1), "base64url");
  if (got.length !== want.length || !timingSafeEqual(got, want)) return null;
  try {
    const st = JSON.parse(Buffer.from(p, "base64url").toString("utf8")) as RelayState;
    if (typeof st.b !== "string" || typeof st.d !== "string" || typeof st.s !== "string" || typeof st.c !== "string" || !(st.e > nowS(now))) return null;
    return st;
  } catch {
    return null;
  }
}

/** The PKCE verifier for Google, made from the state so nothing is kept between the two requests. */
export const googleVerifier = (app: RelayApp, state: string) => b64(createHmac("sha256", key(app, "pkce")).update(state).digest());

/** Google's consent page for a box. */
export function consentURL(app: RelayApp, state: string): string {
  const q = new URLSearchParams({
    client_id: app.clientId,
    redirect_uri: app.redirect,
    response_type: "code",
    scope: SCOPES,
    state,
    code_challenge: b64(createHash("sha256").update(googleVerifier(app, state)).digest()),
    code_challenge_method: "S256",
    // offline + consent: a refresh token every time, not only the first time the account meets the app.
    access_type: "offline",
    prompt: "consent",
  });
  return `${GOOGLE_AUTH}?${q}`;
}

export type Handoff = { b: string; c: string; e: number; r: string; sc: string; em: string };

/** The grant, as a code only this site can open (AES-256-GCM: nonce, ciphertext, tag). */
export function sealHandoff(app: RelayApp, h: Omit<Handoff, "e">, now = new Date()): string {
  const iv = randomBytes(12);
  const c = createCipheriv("aes-256-gcm", key(app, "handoff"), iv);
  const body = Buffer.concat([c.update(JSON.stringify({ ...h, e: nowS(now) + HANDOFF_TTL } satisfies Handoff)), c.final()]);
  return b64(Buffer.concat([iv, body, c.getAuthTag()]));
}

/**
 * Opens a code for the box that presents it: its licence's box, the verifier
 * whose hash it sent at the start, within two minutes.
 */
export function openHandoff(app: RelayApp, code: string, box: string, verifier: string, now = new Date()): Handoff | null {
  if (code.length > 4000 || verifier.length > 200) return null;
  const raw = Buffer.from(code, "base64url");
  if (raw.length < 12 + 16 + 2) return null;
  try {
    const d = createDecipheriv("aes-256-gcm", key(app, "handoff"), raw.subarray(0, 12));
    d.setAuthTag(raw.subarray(raw.length - 16));
    const h = JSON.parse(Buffer.concat([d.update(raw.subarray(12, raw.length - 16)), d.final()]).toString("utf8")) as Handoff;
    const want = Buffer.from(h.c, "base64url");
    const got = createHash("sha256").update(verifier).digest();
    if (h.b !== box || !(h.e > nowS(now)) || want.length !== got.length || !timingSafeEqual(want, got)) return null;
    return h;
  } catch {
    return null;
  }
}

export type GoogleToken = { access_token?: string; expires_in?: number; refresh_token?: string; scope?: string; id_token?: string; error?: string; error_description?: string };

/** Posts a grant to Google's token endpoint with the app's secret. */
export async function googleToken(app: RelayApp, form: Record<string, string>, fetcher: typeof fetch = fetch): Promise<{ status: number; body: GoogleToken }> {
  const res = await fetcher(GOOGLE_TOKEN, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ ...form, client_id: app.clientId, client_secret: app.clientSecret }),
    signal: AbortSignal.timeout(15_000),
  });
  let body: GoogleToken = {};
  try {
    body = (await res.json()) as GoogleToken;
  } catch {}
  return { status: res.status, body };
}

/** The account's address from Google's ID token (straight from Google over TLS; only shown, so not verified). */
export function idTokenEmail(idToken: string | undefined): string {
  const part = idToken?.split(".")[1];
  if (!part) return "";
  try {
    const e = (JSON.parse(Buffer.from(part, "base64url").toString("utf8")) as { email?: unknown }).email;
    return typeof e === "string" ? e.slice(0, 320) : "";
  } catch {
    return "";
  }
}

/** Where the callback sends the browser: the box's own callback, with a code or an error, and the box's state. */
export function backToBox(st: Pick<RelayState, "d" | "s">, answer: { code: string } | { error: string }): string | null {
  const to = boxCallback(st.d);
  if (!to) return null;
  return `${to}?${new URLSearchParams({ state: st.s, ...answer })}`;
}
