// Emails about a customer's box. Plain words, plain text, no tracking.
import { send } from "@shiptiffin/sdk/email";
import { boxDomain, dashboardUrl } from "./names";

export const SITE = (process.env.SITE_URL || "https://shiptiffin.com").replace(/\/+$/, "");
export const ACCOUNT = `${SITE}/account`;

export type Mail = { to: string; subject: string; text: string };

type Box = { email: string; name: string | null };

const day = (d: Date) => d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" });
const named = (b: Box) => (b.name ? `${b.name} (${boxDomain(b.name)})` : "your box");
const sign = "\n\nShipTiffin\nhello@shiptiffin.com";

export const mails = {
  paid: (b: Box): Mail => ({
    to: b.email,
    subject: "Payment received: next, connect Hetzner",
    text: `Thanks, your payment went through.\n\nNext, connect your Hetzner project and pick a name and size for your box:\n${SITE}/start${sign}`,
  }),
  ready: (b: Box): Mail => ({
    to: b.email,
    subject: `Your box ${b.name} is ready`,
    text: `Your box is up at ${b.name ? dashboardUrl(b.name) : ""}, with a valid HTTPS certificate.\n\nOpen it from your account: ${ACCOUNT}\nThe first "Open your dashboard" signs you in with a one-time link the box made at setup (it works once, within 24 hours). Add a passkey in the dashboard then, so you can sign in on your own: after that we can't sign you in.\n\nWe removed our setup key from your server and closed SSH. The server is in your Hetzner project and yours to keep.${sign}`,
  }),
  setup_failed: (b: Box, error: string): Mail => ({
    to: b.email,
    subject: `Setting up ${b.name ?? "your box"} didn't work`,
    text: `Setting up ${named(b)} stopped${error ? `: ${error}` : ""}.\n\nWe removed its address and deleted what this attempt made in your Hetzner project (only what carries this box's shiptiffin-box label). Your Hetzner key was forgotten.\n\nTo try again, paste your key at ${SITE}/start. If it keeps failing, reply to this email.${sign}`,
  }),
  duplicate_refunded: (b: Box): Mail => ({
    to: b.email,
    subject: "We refunded a second payment for the same box",
    text: `Two payments came in for ${named(b)} (two checkout pages, probably). A box needs one subscription, so we cancelled the second one and refunded it in full. Stripe shows the refund within a few days.${sign}`,
  }),
  refunded: (b: Box): Mail => ({
    to: b.email,
    subject: `Refunded: ${b.name ?? "your box"}`,
    text: `As asked, we refunded your first payment for ${named(b)} in full and ended the subscription. Your server and apps keep running in your Hetzner account; delete the server there to stop Hetzner billing you. The shiptiffin.app address stays for 30 days.${sign}`,
  }),
  extras_paused: (b: Box, until: Date): Mail => ({
    to: b.email,
    subject: `Your ShipTiffin subscription for ${b.name ?? "your box"} has ended`,
    text: `The subscription for ${named(b)} is no longer active.\n\nYour server and every app on it keep running, untouched. What stops: automatic Tiffin updates, our monitoring emails, and support.\n\nThe address ${b.name ? boxDomain(b.name) : ""} keeps working until ${day(until)}. After that we remove it; point a domain of your own at the box before then (tiffin domain set, or Settings › Your box › Domain).\n\nTo pick it up again: ${ACCOUNT}${sign}`,
  }),
  extras_resumed: (b: Box): Mail => ({
    to: b.email,
    subject: `${b.name ?? "Your box"}: managed again`,
    text: `The subscription for ${named(b)} is active again: updates, monitoring and the shiptiffin.app address are back on.${sign}`,
  }),
  payment_failed: (b: Box): Mail => ({
    to: b.email,
    subject: "A payment for your box didn't go through",
    text: `We couldn't take this month's payment for ${named(b)}. Stripe will try again over the next two weeks.\n\nYour server and apps keep running whatever happens. Update your card here: ${ACCOUNT} (Billing).${sign}`,
  }),
  dns_soon: (b: Box, on: Date): Mail => ({
    to: b.email,
    subject: `${b.name ? boxDomain(b.name) : "Your address"} goes on ${day(on)}`,
    text: `A reminder: the subscription for ${named(b)} ended, so its shiptiffin.app address stops on ${day(on)}. Your server and apps keep running.\n\nBefore then, point a domain of your own at the box (Settings › Your box › Domain in its dashboard), or pick the subscription up again: ${ACCOUNT}${sign}`,
  }),
  dns_removed: (b: Box): Mail => ({
    to: b.email,
    subject: `${b.name ? boxDomain(b.name) : "Your address"} has been removed`,
    text: `As announced, we removed the shiptiffin.app address of ${named(b)}. The server and apps still run at their own IP address and any domain you set.\n\nPick the subscription up again and the address comes back: ${ACCOUNT}${sign}`,
  }),
  down: (b: Box, since: Date): Mail => ({
    to: b.email,
    subject: `${b.name ?? "Your box"} isn't answering`,
    text: `${b.name ? dashboardUrl(b.name) : "Your box"} hasn't answered our checks since ${since.toUTCString()}.\n\nCheck the server in the Hetzner console (is it running? out of disk?). We'll email again when it's back.${sign}`,
  }),
  up: (b: Box): Mail => ({
    to: b.email,
    subject: `${b.name ?? "Your box"} is answering again`,
    text: `${b.name ? dashboardUrl(b.name) : "Your box"} is answering our checks again.${sign}`,
  }),
  silent: (b: Box, last: Date | null): Mail => ({
    to: b.email,
    subject: `${b.name ?? "Your box"} hasn't checked in`,
    text: `${named(b)} checks in with us every six hours. Its last check-in was ${last ? last.toUTCString() : "never"}.\n\nIs the server running? Look in the Hetzner console. After 72 hours without a check-in we take its shiptiffin.app address off the server's IP (if the server was deleted, Hetzner may give that IP to someone else); it comes back at the next check-in.${sign}`,
  }),
  parked: (b: Box): Mail => ({
    to: b.email,
    subject: `We parked ${b.name ? boxDomain(b.name) : "your box's address"}`,
    text: `${named(b)} hasn't checked in for 72 hours, so we took its shiptiffin.app address off the server's IP (if the server was deleted, Hetzner may give that IP to someone else).\n\nIf the server is still yours, start it in the Hetzner console: the address comes back within a few hours of its next check-in.${sign}`,
  }),
  killed: (b: Box, reason: string): Mail => ({
    to: b.email,
    subject: `We turned off ${b.name ? boxDomain(b.name) : "your shiptiffin.app address"}`,
    text: `After an abuse report we removed the address ${b.name ? boxDomain(b.name) : ""}.\n\nReason: ${reason}\n\nYour server and apps are untouched and still reachable at their own IP and domains. Reply to this email if you think this is a mistake.${sign}`,
  }),
};

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
export function render(kind: string, box: Box & { last_heartbeat_at?: Date | null; extras_paused_at?: Date | null; kill_reason?: string | null }, params: Record<string, any>, now = new Date()): Mail | null {
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
