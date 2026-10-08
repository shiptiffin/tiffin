// The two emails the early-access list sends: the confirmation (with a
// remove link) and the owner's note. Plain text first, and simple HTML in the
// box's own paper, ink and brass (packages/emails/src/ui/brand.ts). No images,
// no tracking.
import type { Signup } from "./early-access";
import { HOST_CHOICES, PROJECT_CHOICES } from "./form";

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

export function confirmEmail(to: string, l: { confirm: string; remove: string; unsubscribe: string }): Mail {
  const subject = "Confirm your place on the ShipTiffin list";
  const text = [
    "Hello,",
    "",
    "Thanks for asking about ShipTiffin. Confirm this is your address and you're on the early-access list:",
    "",
    l.confirm,
    "",
    "We're letting people in a few at a time. When it's your turn, we'll email you an invite with the founding price. Until then, we won't write.",
    "",
    "Didn't ask for this? Ignore this email and you won't hear from us again. Or remove your address now:",
    l.remove,
    "",
    "ShipTiffin · hello@shiptiffin.com · https://shiptiffin.com",
  ].join("\n");
  const html = page(
    "One click and you're on the list.",
    p("Hello,") +
      p("Thanks for asking about ShipTiffin. Confirm this is your address and you&rsquo;re on the early-access list.") +
      button(l.confirm, "Confirm my email") +
      p(
        "We&rsquo;re letting people in a few at a time. When it&rsquo;s your turn, we&rsquo;ll email you an invite with the founding price. Until then, we won&rsquo;t write.",
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

const label = (list: readonly { value: string; label: string }[], v: string | null) =>
  list.find((c) => c.value === v)?.label ?? v;

/** A plain note to the owner when someone confirms. Reply goes to the person. */
export function ownerEmail(to: string, s: Signup): Mail {
  const lines = [
    `${s.email} confirmed their place on the early-access list.`,
    "",
    `Would host: ${s.hosting ?? "(not said)"}`,
    `Projects: ${label(PROJECT_CHOICES, s.projects) ?? "(not said)"}`,
    `Uses today: ${s.currentHosts.length ? s.currentHosts.map((h) => label(HOST_CHOICES, h)).join(", ") : "(not said)"}`,
    `Note: ${s.note ?? "(none)"}`,
    "",
    `Signed up ${s.createdAt.toISOString().slice(0, 16).replace("T", " ")} UTC. Everyone is in the early_access table of the website project (Database in the dashboard).`,
  ];
  return { to, subject: `Early access: ${s.email}`, text: lines.join("\n"), replyTo: s.email };
}
