// Auth emails: verification, magic link, one-time code, password reset and
// invitations. Sent over the project's SMTP_URL (the box's relay, or its dev
// inbox until a relay is set up). Every message has a plain-text twin.
import nodemailer from "nodemailer";
import type { Transporter } from "nodemailer";
import * as T from "./templates.gen";

export type Mail = { to: string; subject: string; text: string; html: string; kind: string };
export type SentMail = Mail & { project: string; from: string; at: string };

const transports = new Map<string, Transporter>();

/** The last messages sent, newest last. Read by the admin API and tests. */
export const outbox: SentMail[] = [];
const OUTBOX_MAX = 200;

function transport(url: string): Transporter {
  let t = transports.get(url);
  if (!t) {
    t = nodemailer.createTransport(url);
    transports.set(url, t);
  }
  return t;
}

export async function send(project: string, smtpUrl: string, from: string, m: Mail): Promise<void> {
  outbox.push({ ...m, project, from, at: new Date().toISOString() });
  if (outbox.length > OUTBOX_MAX) outbox.splice(0, outbox.length - OUTBOX_MAX);
  if (!smtpUrl) {
    console.log(JSON.stringify({ level: "warn", msg: "no SMTP_URL for project; email not sent", project, to: m.to, kind: m.kind, text: m.text }));
    return;
  }
  try {
    await transport(smtpUrl).sendMail({
      from,
      to: m.to,
      subject: m.subject,
      text: m.text,
      html: m.html,
      headers: { "X-Tiffin-Project": project, "X-Tiffin-Kind": `auth.${m.kind}` },
    });
  } catch (err) {
    // The mail server refused this address for good (5xx: suppressed after a
    // bounce or complaint, say). The request that sent it still answers as
    // usual, so nobody learns from it whether an address is refused; the
    // box's log says what happened. Temporary failures still fail the request.
    const code = refusal(err);
    if (code === null) throw err;
    console.log(JSON.stringify({ level: "warn", msg: "mail server refused the recipient; email not sent", project, kind: m.kind, to: m.to.replace(/^[^@]*/, "…"), code }));
  }
}

/** The SMTP reply code when the server refused for good (5xx), else null. */
export function refusal(err: unknown): number | null {
  const e = err as { responseCode?: unknown; code?: unknown; rejectedErrors?: Array<{ responseCode?: unknown }> } | null;
  const codes = [e?.responseCode, ...(e?.rejectedErrors ?? []).map((r) => r.responseCode)].filter((c): c is number => typeof c === "number");
  const permanent = codes.find((c) => c >= 500 && c < 600);
  return permanent ?? null;
}

export function closeTransports() {
  for (const t of transports.values()) t.close();
  transports.clear();
}

// ---- templates ----------------------------------------------------------
// The emails are React Email templates in packages/emails, compiled at build
// time to plain functions (templates.gen.ts): no React here, and every value
// is escaped where it lands. They carry the app's name and logo, never
// ShipTiffin's; the button is the dashboard's brass unless the app sets its
// own accent (auth.emailAccent).

/** How an app's emails look: its name and, optionally, its logo and accent. */
export type Brand = { app: string; primaryUrl: string; logoUrl?: string; accent?: string };

/** White or near-black (the dashboard's text on brass), whichever reads better on the accent (WCAG contrast). */
export function accentText(hex: string): string {
  const onWhite = 1.05 / (luminance(hex) + 0.05);
  const onDark = (luminance(hex) + 0.05) / (luminance(T.ON_BRASS) + 0.05);
  return onWhite >= onDark ? "#ffffff" : T.ON_BRASS;
}
function luminance(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  return 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) + 0.0722 * lin(n & 255);
}
function lin(c: number) {
  const x = c / 255;
  return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
}

function brandVars(b: Brand, email: string): T.AppBrand {
  const accent = b.accent && /^#[0-9a-f]{6}$/i.test(b.accent) ? b.accent : T.BRASS;
  let site = "", siteLabel = "";
  try {
    const u = new URL(b.primaryUrl);
    if (u.protocol === "https:") [site, siteLabel] = [u.origin, u.host];
  } catch {}
  return { app: b.app, logoUrl: b.logoUrl ?? "", accent, accentText: accentText(accent), site, siteLabel, email };
}

/** "10 minutes", "1 hour", "48 hours", "2 days". */
export function duration(seconds: number): string {
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  if (seconds % 86_400 === 0 && seconds >= 3 * 86_400) return plural(seconds / 86_400, "day");
  if (seconds % 3600 === 0) return plural(seconds / 3600, "hour");
  return plural(Math.max(1, Math.round(seconds / 60)), "minute");
}

const mk = (kind: string, to: string, e: T.Email): Mail => ({ kind, to, subject: e.subject, text: e.text, html: e.html });

export const templates = {
  verify: (b: Brand, to: string, url: string, ttl = 3600) => mk("verify", to, T.verifyEmail({ ...brandVars(b, to), url, expiresIn: duration(ttl) })),
  magicLink: (b: Brand, to: string, url: string, ttl = 600) => mk("magic-link", to, T.magicLink({ ...brandVars(b, to), url, expiresIn: duration(ttl) })),
  otp: (b: Brand, to: string, code: string, purpose: T.OtpVars["purpose"], ttl = 300) =>
    mk("otp", to, T.otp({ ...brandVars(b, to), code, purpose, expiresIn: duration(ttl) })),
  reset: (b: Brand, to: string, url: string, ttl = 3600) => mk("reset-password", to, T.resetPassword({ ...brandVars(b, to), url, expiresIn: duration(ttl) })),
  invite: (b: Brand, to: string, url: string, inviter: { name?: string; email: string }, org: string, role: string, ttl = 48 * 3600) =>
    mk(
      "invitation",
      to,
      T.orgInvite({
        ...brandVars(b, to),
        url,
        inviter: inviter.name || inviter.email,
        inviterEmail: inviter.name ? inviter.email : "",
        org,
        role,
        expiresIn: duration(ttl),
      }),
    ),
  twoFactor: (b: Brand, to: string, code: string, ttl = 180) => mk("two-factor", to, T.twoFactor({ ...brandVars(b, to), code, expiresIn: duration(ttl) })),
  changeEmail: (b: Brand, to: string, url: string, newEmail: string, ttl = 3600) =>
    mk("change-email", to, T.changeEmail({ ...brandVars(b, to), url, newEmail, expiresIn: duration(ttl) })),
  emailChanged: (b: Brand, to: string, newEmail: string, at: Date, support?: { url: string; label: string }) =>
    mk(
      "email-changed",
      to,
      T.emailChanged({
        ...brandVars(b, to),
        newEmail,
        when: at.toISOString().slice(0, 16).replace("T", " ") + " UTC",
        support: support?.url ?? "",
        supportLabel: support?.label ?? "",
      }),
    ),
};
