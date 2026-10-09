// The two emails the sign-up list sends: the confirmation (with a remove
// link) and the owner's note. React Email components (emails/list.tsx), in
// the same paper, ink and brass as every ShipTiffin email. No tracking.
import { createElement } from "react";
import { Confirm, OwnerNote } from "../emails/list";
import { renderEmail } from "../emails/render";
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

export async function confirmEmail(
  to: string,
  name: string | null,
  l: { confirm: string; remove: string; unsubscribe: string },
): Promise<Mail> {
  const hello = name ? `Hello ${name.split(" ")[0]},` : "Hello,";
  const { html, text } = await renderEmail(createElement(Confirm, { hello, links: l }));
  return {
    to,
    subject: "Confirm your email for ShipTiffin",
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

/** A note to the owner when someone confirms. Reply goes to the person. */
export async function ownerEmail(to: string, r: InviteRequest): Promise<Mail> {
  const said = (v: string | null | undefined) => v ?? "(not said)";
  const who = r.name ? `${r.name} <${r.email}>` : r.email;
  const rows = [
    { label: "Builds", value: said(label(ROLE_CHOICES, r.role)) },
    { label: "Would host first", value: said(r.hostFirst) },
    { label: "Projects", value: said(label(PROJECT_CHOICES, r.projects)) },
    { label: "Uses today", value: said(list(TOOL_CHOICES, r.tools)) },
    { label: "Spends a month", value: said(label(SPEND_CHOICES, r.spend)) },
    { label: "AI agents", value: said(list(AGENT_CHOICES, r.agents)) },
    ...(["github", "x", "linkedin", "site"] as const)
      .filter((k) => r[k])
      .map((k) => ({ label: k === "site" ? "Website" : k === "x" ? "X" : k === "github" ? "GitHub" : "LinkedIn", value: String(r[k]) })),
    { label: "Note", value: r.note ?? "(none)" },
  ];
  const where = `Asked ${r.createdAt.toISOString().slice(0, 16).replace("T", " ")} UTC. Row ${r.id} of the invite_requests table in the website project (Database in the dashboard): set status to invited and invited_at when you send their sign-up link.`;
  const { html, text } = await renderEmail(createElement(OwnerNote, { who, rows, where }));
  return { to, subject: `Sign-up list: ${r.name ?? r.email}`, text, html, replyTo: r.email };
}
