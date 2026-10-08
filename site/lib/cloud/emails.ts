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
    text: `Your box is up at ${b.name ? dashboardUrl(b.name) : ""}.\n\nOpen it from your account: ${ACCOUNT}\nThe first time, add a passkey in the dashboard so you can sign in on your own.\n\nWe removed our setup key from your server and closed SSH. The server is in your Hetzner project and yours to keep.${sign}`,
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
    text: `${named(b)} checks in with us once a day. Its last check-in was ${last ? last.toUTCString() : "never"}.\n\nIf it answers on the web, this is harmless (the check-in may be blocked). If not, look at the server in the Hetzner console.${sign}`,
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

/** Sends one email; failures are logged, never thrown (a webhook must not fail over mail). */
export async function deliver(m: Mail): Promise<boolean> {
  if (!process.env.SMTP_URL) {
    console.warn("cloud email not sent (no SMTP_URL):", m.subject);
    return false;
  }
  try {
    const r = await send({ ...m, from: from(), replyTo: "hello@shiptiffin.com" });
    return r.accepted.length > 0;
  } catch (e) {
    console.error("cloud email", m.subject, e instanceof Error ? e.message : e);
    return false;
  }
}
