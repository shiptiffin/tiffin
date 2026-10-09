// Emails about a customer's box. React Email components (emails/box.tsx),
// rendered here to HTML and plain text. No tracking: no pixels, no rewritten
// links; the pictures are the same static files for everyone.
import { send } from "@shiptiffin/sdk/email";
import { createElement } from "react";
import { AbuseReport, StuckAction, type Stuck } from "../../emails/admin";
import { boxEmails, type BoxCtx, type Built } from "../../emails/box";
import { renderEmail } from "../../emails/render";

export const SITE = (process.env.SITE_URL || "https://shiptiffin.com").replace(/\/+$/, "");
export const ACCOUNT = `${SITE}/account`;

export type Mail = { to: string; subject: string; text: string; html?: string };

/** The box an email is about. `id` sends "Open your dashboard" through the account. */
type Box = { email: string; name: string | null; id?: string };

const ctx = (b: Box): BoxCtx => ({ name: b.name, id: b.id, site: SITE });

async function build(to: string, e: Built): Promise<Mail> {
  const { html, text } = await renderEmail(e.element);
  return { to, subject: e.subject, text, html };
}

export const mails = {
  paid: (b: Box) => build(b.email, boxEmails.paid(ctx(b))),
  ready: (b: Box) => build(b.email, boxEmails.ready(ctx(b))),
  setup_failed: (b: Box, error: string) => build(b.email, boxEmails.setup_failed(ctx(b), error)),
  attention: (b: Box, why: string) => build(b.email, boxEmails.attention(ctx(b), why)),
  server_off: (b: Box) => build(b.email, boxEmails.server_off(ctx(b))),
  duplicate_refunded: (b: Box) => build(b.email, boxEmails.duplicate_refunded(ctx(b))),
  refunded: (b: Box) => build(b.email, boxEmails.refunded(ctx(b))),
  extras_paused: (b: Box, until: Date) => build(b.email, boxEmails.extras_paused(ctx(b), until)),
  extras_resumed: (b: Box) => build(b.email, boxEmails.extras_resumed(ctx(b))),
  payment_failed: (b: Box) => build(b.email, boxEmails.payment_failed(ctx(b))),
  dns_soon: (b: Box, on: Date) => build(b.email, boxEmails.dns_soon(ctx(b), on)),
  dns_removed: (b: Box) => build(b.email, boxEmails.dns_removed(ctx(b))),
  down: (b: Box, since: Date) => build(b.email, boxEmails.down(ctx(b), since)),
  up: (b: Box) => build(b.email, boxEmails.up(ctx(b))),
  silent: (b: Box, last: Date | null) => build(b.email, boxEmails.silent(ctx(b), last)),
  parked: (b: Box) => build(b.email, boxEmails.parked(ctx(b))),
  killed: (b: Box, reason: string) => build(b.email, boxEmails.killed(ctx(b), reason)),
};

/** For us: a Stripe action the outbox has retried for over an hour. */
export const stuckMail = (to: string, s: Omit<Stuck, "admin">) =>
  build(to, { subject: `Stuck: ${s.kind} for ${s.box}`, element: createElement(StuckAction, { s: { ...s, admin: `${SITE}/admin` } }) });

/** For us: an abuse report came in. */
export const abuseMail = (to: string, r: { target: string; box: string | null; from: string | null; details: string }) =>
  build(to, { subject: `Abuse report: ${r.target}`, element: createElement(AbuseReport, { r: { ...r, admin: `${SITE}/admin` } }) });

/** "ShipTiffin <website@box>": the box's sender for the project, with our name. */
function from(): string | undefined {
  const addr = process.env.EMAIL_FROM?.match(/<([^>]+)>/)?.[1] ?? process.env.EMAIL_FROM;
  return addr ? `ShipTiffin <${addr}>` : undefined;
}

/** Sends one email. The outbox (outbox.ts) retries what fails; nothing here throws. */
export async function deliver(m: Mail): Promise<{ ok: true } | { ok: false; error: string }> {
  if (!process.env.SMTP_URL) return { ok: false, error: "no SMTP_URL" };
  try {
    const r = await send({ ...m, from: from(), replyTo: "hello@shiptiffin.com" });
    return r.accepted.length > 0 ? { ok: true } : { ok: false, error: "the mail server accepted no recipient" };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}

/** The email an outbox row of this kind sends, or null for a kind that isn't an email. */
export async function render(kind: string, box: Box & { last_heartbeat_at?: Date | null; extras_paused_at?: Date | null; kill_reason?: string | null }, params: Record<string, any>, now = new Date()): Promise<Mail | null> {
  const until = params.until ? new Date(params.until) : new Date((box.extras_paused_at ?? now).getTime() + 30 * 86_400_000);
  switch (kind) {
    case "paid":
      return mails.paid(box);
    case "ready":
      return mails.ready(box);
    case "setup_failed":
      return mails.setup_failed(box, String(params.error ?? ""));
    case "extras_paused":
      return mails.extras_paused(box, until);
    case "extras_resumed":
      return mails.extras_resumed(box);
    case "payment_failed":
      return mails.payment_failed(box);
    case "duplicate_refunded":
      return mails.duplicate_refunded(box);
    case "attention":
      return mails.attention(box, String(params.why ?? ""));
    case "server_off":
      return mails.server_off(box);
    case "refunded":
      return mails.refunded(box);
    case "dns_soon":
      return mails.dns_soon(box, until);
    case "dns_removed":
      return mails.dns_removed(box);
    case "down":
      return mails.down(box, params.since ? new Date(params.since) : now);
    case "up":
      return mails.up(box);
    case "silent":
      return mails.silent(box, box.last_heartbeat_at ?? null);
    case "parked":
      return mails.parked(box);
    case "killed":
      return mails.killed(box, String(params.reason ?? box.kill_reason ?? ""));
  }
  return null;
}
