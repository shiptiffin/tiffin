// The two emails the sign-up list sends: the confirmation (with a
// remove link) and the owner's note. Plain text first, and simple HTML in the
// box's own paper, ink and brass (packages/emails/src/ui/brand.ts). No images,
// no tracking.
import type { InviteRequest } from "./early-access";
import { AGENT_CHOICES, PROJECT_CHOICES, ROLE_CHOICES, SPEND_CHOICES, TOOL_CHOICES, label } from "./form";

export type Mail = {
  to: string;
  subject: string;
  text: string;
  html?: string;
  replyTo?: string;
  headers?: Record<string, string>;
};

const C = {
  bg: "#f9f6f2",
  card: "#fefdfa",
  rule: "#e8e2d8",
  ink: "#231d18",
  ink2: "#564e48",
  ink3: "#6c6660",
  brass: "#f2b036",
  onBrass: "#25170c",
  link: "#825411",
};

const esc = (s: string) =>
  s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

const font = `-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif`;

function page(preview: string, body: string, foot: string): string {
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light"><title>ShipTiffin</title></head>
<body style="margin:0;padding:0;background:${C.bg};">
<div style="display:none;max-height:0;overflow:hidden;">${esc(preview)}</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:${C.bg};"><tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:520px;background:${C.card};border:1px solid ${C.rule};border-radius:12px;">
<tr><td style="padding:28px 28px 8px;font:600 15px/1.4 ${font};color:${C.ink};">ShipTiffin</td></tr>
<tr><td style="padding:8px 28px 28px;font:15px/1.6 ${font};color:${C.ink2};">${body}</td></tr>
</table>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:520px;"><tr><td style="padding:16px 28px;font:13px/1.55 ${font};color:${C.ink3};">${foot}</td></tr></table>
</td></tr></table>
</body></html>`;
}

const p = (s: string) => `<p style="margin:0 0 14px;">${s}</p>`;

const button = (href: string, label: string) =>
  `<table role="presentation" cellpadding="0" cellspacing="0" style="margin:6px 0 20px;"><tr><td style="border-radius:10px;background:${C.brass};"><a href="${esc(href)}" style="display:inline-block;padding:12px 20px;font:600 15px/1 ${font};color:${C.onBrass};text-decoration:none;border-radius:10px;">${esc(label)}</a></td></tr></table>`;

const a = (href: string, label: string) => `<a href="${esc(href)}" style="color:${C.link};">${esc(label)}</a>`;

export function confirmEmail(
  to: string,
  name: string | null,
  l: { confirm: string; remove: string; unsubscribe: string },
): Mail {
  const subject = "Confirm your email for ShipTiffin";
  const hello = name ? `Hello ${name.split(" ")[0]},` : "Hello,";
  const text = [
    hello,
    "",
    "Thanks for joining the ShipTiffin sign-up list. Confirm this is your address:",
    "",
    l.confirm,
    "",
    "Sign-up opens this week, and we'll send you the link as soon as it does. The first 100 customers pay $12 a month instead of $19, locked for 24 months. Until then, we won't write.",
    "",
    "Didn't ask for this? Ignore this email and you won't hear from us again. Or remove your address now:",
    l.remove,
    "",
    "ShipTiffin · hello@shiptiffin.com · https://shiptiffin.com",
  ].join("\n");
  const html = page(
    "One click and you're on the list.",
    p(esc(hello)) +
      p("Thanks for joining the ShipTiffin sign-up list. Confirm this is your address.") +
      button(l.confirm, "Confirm my email") +
      p(
        "Sign-up opens this week, and we&rsquo;ll send you the link as soon as it does. The first 100 customers pay $12 a month instead of $19, locked for 24 months. Until then, we won&rsquo;t write.",
      ),
    `Didn&rsquo;t ask for this? Ignore this email and you won&rsquo;t hear from us again, or ${a(l.remove, "remove your address")}.<br>ShipTiffin · ${a("mailto:hello@shiptiffin.com", "hello@shiptiffin.com")}`,
  );
  return {
    to,
    subject,
    text,
    html,
    headers: {
      "List-Unsubscribe": `<${l.unsubscribe}>`,
      "List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
    },
  };
}

const list = (choices: readonly { value: string; label: string }[], vs: string[]) =>
  vs.length ? vs.map((v) => label(choices, v)).join(", ") : null;

/** A plain note to the owner when someone confirms. Reply goes to the person. */
export function ownerEmail(to: string, r: InviteRequest): Mail {
  const said = (v: string | null | undefined) => v ?? "(not said)";
  const lines = [
    `${r.name ? `${r.name} <${r.email}>` : r.email} confirmed their place on the sign-up list.`,
    "",
    `Builds: ${said(label(ROLE_CHOICES, r.role))}`,
    `Would host first: ${said(r.hostFirst)}`,
    `Projects: ${said(label(PROJECT_CHOICES, r.projects))}`,
    `Uses today: ${said(list(TOOL_CHOICES, r.tools))}`,
    `Spends a month: ${said(label(SPEND_CHOICES, r.spend))}`,
    `AI agents: ${said(list(AGENT_CHOICES, r.agents))}`,
    ...(["github", "x", "linkedin", "site"] as const).filter((k) => r[k]).map((k) => `${k === "site" ? "Website" : k === "x" ? "X" : k === "github" ? "GitHub" : "LinkedIn"}: ${r[k]}`),
    `Note: ${r.note ?? "(none)"}`,
    "",
    `Asked ${r.createdAt.toISOString().slice(0, 16).replace("T", " ")} UTC. Row ${r.id} of the invite_requests table in the website project (Database in the dashboard): set status to invited and invited_at when you send their sign-up link.`,
  ];
  return { to, subject: `Sign-up list: ${r.name ?? r.email}`, text: lines.join("\n"), replyTo: r.email };
}
