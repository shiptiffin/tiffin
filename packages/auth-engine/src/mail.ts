// Auth emails: verification, magic link, one-time code, password reset and
// invitations. Sent over the project's SMTP_URL (the box's relay, or its dev
// inbox until a relay is set up). Every message has a plain-text twin.
import nodemailer from "nodemailer";
import type { Transporter } from "nodemailer";

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
  await transport(smtpUrl).sendMail({
    from,
    to: m.to,
    subject: m.subject,
    text: m.text,
    html: m.html,
    headers: { "X-Tiffin-Project": project, "X-Tiffin-Kind": `auth.${m.kind}` },
  });
}

export function closeTransports() {
  for (const t of transports.values()) t.close();
  transports.clear();
}

// ---- templates ----------------------------------------------------------

const esc = (s: string) =>
  s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c] as string);

// Warm paper, ink and brass, the same tokens as the Tiffin dashboard.
const C = { paper: "#f7f4ee", card: "#fffdf9", rule: "#e6e0d5", ink: "#231e19", ink2: "#5c544a", ink3: "#8a8175", brass: "#9a7431", onBrass: "#fffdf9" };
const SANS = `-apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif`;
const SERIF = `Georgia,"Times New Roman",serif`;

type Block = { heading: string; intro: string; action?: { label: string; url: string }; code?: string; outro?: string };

function layout(app: string, preheader: string, b: Block): string {
  const action = b.action
    ? `<tr><td style="padding:8px 0 4px"><a href="${esc(b.action.url)}" style="display:inline-block;background:${C.ink};color:${C.onBrass};font:600 15px/1 ${SANS};text-decoration:none;padding:14px 22px;border-radius:8px">${esc(b.action.label)}</a></td></tr>
<tr><td style="padding:18px 0 0;font:13px/1.55 ${SANS};color:${C.ink3}">Or paste this link into your browser:<br><a href="${esc(b.action.url)}" style="color:${C.brass};word-break:break-all">${esc(b.action.url)}</a></td></tr>`
    : "";
  const code = b.code
    ? `<tr><td style="padding:6px 0 4px"><div style="display:inline-block;font:600 30px/1 'SF Mono',Menlo,Consolas,monospace;letter-spacing:8px;color:${C.ink};background:${C.paper};border:1px solid ${C.rule};border-radius:10px;padding:16px 18px 16px 26px">${esc(b.code)}</div></td></tr>`
    : "";
  const outro = b.outro ? `<tr><td style="padding:22px 0 0;font:14px/1.6 ${SANS};color:${C.ink2}">${esc(b.outro)}</td></tr>` : "";
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>${esc(b.heading)}</title></head>
<body style="margin:0;padding:0;background:${C.paper}">
<div style="display:none;max-height:0;overflow:hidden;opacity:0">${esc(preheader)}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:${C.paper}"><tr><td align="center" style="padding:40px 16px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:480px">
<tr><td style="padding:0 4px 18px;font:600 13px/1 ${SANS};letter-spacing:.02em;color:${C.ink2}">${esc(app)}</td></tr>
<tr><td style="background:${C.card};border:1px solid ${C.rule};border-radius:14px;padding:32px 32px 30px">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0">
<tr><td style="font:500 26px/1.25 ${SERIF};color:${C.ink};letter-spacing:-.01em;padding:0 0 12px">${esc(b.heading)}</td></tr>
<tr><td style="font:15px/1.6 ${SANS};color:${C.ink2};padding:0 0 22px">${esc(b.intro)}</td></tr>
${code}${action}${outro}
</table></td></tr>
<tr><td style="padding:18px 6px 0;font:12px/1.6 ${SANS};color:${C.ink3}">You got this email because someone used this address on ${esc(app)}. If it wasn't you, you can ignore it; nothing happens until the link or code is used.</td></tr>
</table></td></tr></table></body></html>`;
}

function text(b: Block): string {
  const lines = [b.heading, "", b.intro];
  if (b.code) lines.push("", `    ${b.code}`);
  if (b.action) lines.push("", `${b.action.label}: ${b.action.url}`);
  if (b.outro) lines.push("", b.outro);
  lines.push("", "If this wasn't you, ignore this email. Nothing happens until the link or code is used.");
  return lines.join("\n");
}

function make(kind: string, app: string, to: string, subject: string, b: Block): Mail {
  return { kind, to, subject, html: layout(app, b.intro, b), text: text(b) };
}

export const templates = {
  verify: (app: string, to: string, url: string) =>
    make("verify", app, to, `Confirm your email for ${app}`, {
      heading: "Confirm your email",
      intro: `One click and your ${app} account is ready. The link works for the next hour.`,
      action: { label: "Confirm email", url },
    }),
  magicLink: (app: string, to: string, url: string) =>
    make("magic-link", app, to, `Your sign-in link for ${app}`, {
      heading: `Sign in to ${app}`,
      intro: "Use this link to sign in. It works once, for the next 10 minutes.",
      action: { label: `Sign in to ${app}`, url },
    }),
  otp: (app: string, to: string, otp: string, type: string) =>
    make("otp", app, to, `${otp} is your ${app} code`, {
      heading: type === "forget-password" ? "Your password reset code" : type === "email-verification" ? "Your verification code" : "Your sign-in code",
      intro: `Enter this code in ${app}. It expires in 5 minutes.`,
      code: otp,
    }),
  reset: (app: string, to: string, url: string) =>
    make("reset-password", app, to, `Reset your ${app} password`, {
      heading: "Reset your password",
      intro: "Someone asked to reset the password for this account. If it was you, choose a new one. The link works for the next hour.",
      action: { label: "Choose a new password", url },
      outro: "Your current password keeps working until you change it.",
    }),
  invite: (app: string, to: string, url: string, inviter: string, org: string, role: string) =>
    make("invitation", app, to, `${inviter} invited you to ${org}`, {
      heading: `Join ${org}`,
      intro: `${inviter} invited you to ${org} on ${app} as ${role === "admin" || role === "owner" ? "an" : "a"} ${role}.`,
      action: { label: "Accept invitation", url },
      outro: "The invitation expires in 48 hours.",
    }),
};
