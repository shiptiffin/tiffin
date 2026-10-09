// What the control plane's pages and routes do, server side. Every function
// checks the box belongs to the account before touching it.
import { ENDED, extrasOn, handleEvent, UNMANAGED } from "./billing";
import { foundingCoupon, sealPublic } from "./config";
import * as q from "./db";
import { abuseMail, deliver, SITE } from "./emails";
import { checkToken, EU_LOCATIONS, EU_TYPES, family, RESIZE_TYPES, US_LOCATIONS, US_TYPES } from "./hetzner";
import type { Licence } from "./licence";
import { checkoutUrl } from "./checkout";
import { boxDomain, dashboardUrl, nameProblem, ownerName } from "./names";
import { decide, fromBox, probe, type MonitorBox } from "./monitor";
import { drain, RUNNING_ONLY } from "./outbox";
import { fingerprint, seal, tokenAAD } from "./seal";
import type { Account } from "./session";
import { FOUNDING_LIMIT, StripeError, stripeClient } from "./stripe";

export class ActionError extends Error {
  constructor(
    message: string,
    readonly status = 400,
  ) {
    super(message);
  }
}

export const stripe = () => stripeClient(process.env.STRIPE_SECRET_KEY!.trim());

const onFor = (b: q.BoxRow) => extrasOn(b.plan_status, Boolean(b.first_paid_at));

/** Carries out what the outbox holds that is due: emails, and Stripe's cancel and refund actions. */
export async function drainOutbox() {
  const s = process.env.STRIPE_SECRET_KEY?.trim() ? stripe() : null;
  return drain(q.pgOutbox(), {
    stripe: s
      ? async (row) => {
          const sub = String(row.params.subscription ?? "");
          if (!sub) throw new Error("no subscription");
          if (row.kind === "stripe_cancel") {
            try {
              await s.cancelNow(sub);
            } catch (e) {
              if (!(e instanceof StripeError && (e.status === 404 || /canceled/i.test(e.message)))) throw e;
            }
            return;
          }
          if (row.kind !== "stripe_cancel_refund") throw new Error(`unknown action ${row.kind}`);
          const r = await s.cancelAndRefund(sub, `${row.box_id}:${row.key}`, { box_id: row.box_id, reason: String(row.params.why ?? "") });
          await q.db()`insert into cloud_billing_log (box_id, what, subscription_id, detail) values (${row.box_id}, ${"cancelled and refunded (" + String(row.params.why ?? "") + ")"}, ${sub}, ${q.db().json(r as any)})`;
          if (row.params.why === "moneyback") await q.db()`update cloud_boxes set refunded_at = coalesce(refunded_at, now()) where id = ${row.box_id}`;
        }
      : undefined,
  });
}

// ---- pay ----

export { reusableCheckout } from "./checkout";

/** Box statuses Renew covers: every box we still manage once its subscription ended. */
const RENEWABLE = new Set<q.BoxStatus>(["paid", "provisioning", "cert_pending", "active", "failed"]);

/** Whether a box's ended subscription can be renewed (pure; the account page shows the button by it). */
export function renewable(b: Pick<q.BoxRow, "status" | "plan_status" | "first_paid_at" | "refunded_at">): boolean {
  return RENEWABLE.has(b.status) && ENDED.has(b.plan_status) && Boolean(b.first_paid_at) && !b.refunded_at;
}

/**
 * The Checkout page for a box: a new box, or (renew) one of the account's
 * boxes whose subscription ended, whatever stage it reached (paid and
 * waiting for Hetzner, set up, or a setup that failed). The new
 * subscription carries the same box id and replaces the ended one. One
 * session per box: stored, and handed out again until it expires, so two
 * tabs pay for one subscription; the request is saved before Stripe is
 * called, so a retry after any failure sends the very same request
 * (checkout.ts). Stripe Managed Payments: Stripe (as Link) is the merchant of
 * record, collects and files sales tax and VAT, and picks the payment
 * methods. A box is set up only once its first invoice is paid (billing.ts).
 */
export async function startCheckout(acct: Account, base = SITE, renew?: string): Promise<string> {
  let box: q.BoxRow;
  if (renew) {
    const b = await q.boxFor(renew, acct.id);
    if (!b || !renewable(b)) {
      throw new ActionError(b && !ENDED.has(b.plan_status) && b.plan_status !== "none" ? "This box's subscription hasn't ended: update your card under Billing instead." : "This box can't be renewed.");
    }
    box = b;
  } else box = await q.pendingBox(acct.id, acct.email);
  const s = stripe();
  return checkoutUrl(q.pgCheckout(), s, box.id, async (b) => {
    const price = await s.monthlyPrice();
    const coupon = renew ? null : foundingCoupon();
    const founding = coupon != null && (await q.foundingCount()) < FOUNDING_LIMIT && (await s.couponOpen(coupon));
    let customer = b.stripe_customer_id ?? (await q.customerFor(acct.id));
    // A customer from the other Stripe mode (test before live) is unknown here: start fresh from the email.
    if (customer && !(await s.customerExists(customer))) customer = null;
    const params: Record<string, unknown> = {
      mode: "subscription",
      line_items: [{ price, quantity: 1 }],
      managed_payments: { enabled: true },
      // The terms URL lives in the Stripe account's public details; Checkout refuses this without it.
      consent_collection: { terms_of_service: "required" },
      success_url: renew ? `${base}/account?renewed=${b.id}` : `${base}/start?box=${b.id}&session_id={CHECKOUT_SESSION_ID}`,
      cancel_url: renew ? `${base}/account` : `${base}/start?canceled=1`,
      client_reference_id: b.id,
      metadata: { box_id: b.id },
      subscription_data: { metadata: { box_id: b.id } },
      ...(customer ? { customer } : { customer_email: acct.email }),
      ...(founding ? { discounts: [{ coupon }] } : {}),
    };
    return { params, discounted: founding };
  });
}

/**
 * Back from Checkout: confirm with Stripe at once rather than wait for the
 * webhook. What it queued (the "paid" email) is the caller's to send, with
 * drainOutbox, after the page is answered.
 */
export async function confirmCheckout(acct: Account, boxId: string, sessionId: string): Promise<void> {
  const box = await q.boxFor(boxId, acct.id);
  if (!box || !/^cs_[A-Za-z0-9_]+$/.test(sessionId)) return;
  try {
    const s = stripe();
    const cs = await s.getCheckout(sessionId);
    if ((cs.metadata?.box_id ?? cs.client_reference_id) !== box.id || cs.status !== "complete") return;
    await handleEvent(q.pgBilling(), s, { id: `return:${cs.id}`, type: "checkout.session.completed", created: cs.created ?? Math.floor(Date.now() / 1000), data: { object: cs } });
  } catch (e) {
    console.error("confirm checkout", e instanceof Error ? e.message : e);
  }
}

export async function billingPortal(acct: Account, base = SITE): Promise<string> {
  const customer = await q.portalCustomer(acct.id);
  if (!customer) throw new ActionError("There's no billing account yet: pay for a box first.");
  return (await stripe().portal(customer, `${base}/account`)).url;
}

// ---- connect Hetzner and create ----

const TYPES = new Set<string>([...EU_TYPES, ...US_TYPES]);
const LOCATIONS = new Set<string>([...EU_LOCATIONS, ...US_LOCATIONS]);

async function ownBox(acct: Account, id: string) {
  const box = await q.boxFor(id, acct.id);
  if (!box) throw new ActionError("No such box.", 404);
  return box;
}

/** Checks a pasted key against Hetzner; every call it makes is recorded on the box. */
export async function checkKey(acct: Account, boxId: string, token: string) {
  const box = await ownBox(acct, boxId);
  if (box.status === "deleted") throw new ActionError("This box was deleted.");
  if (box.status === "awaiting_payment" || UNMANAGED.has(box.status)) throw new ActionError("Pay for the box first.");
  return checkToken(token, (c) => q.recordCall(box.id, "check", c));
}

export type CreateInput = { token: string; name: string; serverType: string; location: string };

export async function createBox(acct: Account, boxId: string, input: CreateInput): Promise<void> {
  const box = await ownBox(acct, boxId);
  if (box.status !== "paid" && box.status !== "failed") throw new ActionError(box.status === "awaiting_payment" ? "Pay for the box first." : "This box is already set up.");
  if (box.first_paid_at && ENDED.has(box.plan_status)) throw new ActionError("This box's subscription has ended: renew it in your account first (same box, same name).");
  if (!box.first_paid_at || !onFor(box)) throw new ActionError("The first payment for this box hasn't gone through yet.");
  if (box.dns_state === "killed") throw new ActionError("This box was turned off after an abuse report. Write to hello@shiptiffin.com.");
  const name = String(input.name ?? "").trim().toLowerCase();
  const why = nameProblem(name);
  if (why) throw new ActionError(why);
  if (box.name && box.name !== name) throw new ActionError(`This box is called ${box.name}; its name can't change after a first try.`);
  if (!TYPES.has(input.serverType) || !LOCATIONS.has(input.location)) throw new ActionError("Pick one of the sizes and places listed.");
  const token = String(input.token ?? "").trim();
  if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste your Hetzner token again.");
  if (await q.jobsBusy(box.id)) throw new ActionError("Setup or its clean-up is still running. Try again in a minute.", 409);
  const sealed = seal(sealPublic(), token, tokenAAD(box.id));
  try {
    await q.db().begin(async (tx) => {
      const rows = await tx`update cloud_boxes set name = ${name}, server_type = ${input.serverType}, location = ${input.location},
        token_fingerprint = ${fingerprint(token)}, status = 'provisioning', updated_at = now()
        where id = ${box.id} and status in ('paid', 'failed') returning id`;
      if (rows.length === 0) throw new ActionError("This box changed meanwhile; reload the page.", 409);
      // The box's owner starts with the account's name when it has a real one (the box asks otherwise).
      const owner = ownerName(acct.name, acct.email);
      await q.enqueue(box.id, "provision", { name, serverType: input.serverType, location: input.location, ...(owner ? { ownerName: owner } : {}) }, sealed, tx);
    });
  } catch (e: any) {
    if (e?.code === "23505") throw new ActionError(`${boxDomain(name)} is taken. Try another name.`, 409);
    throw e;
  }
}

/** A request for a new sign-in link that the box hasn't answered within this is given up on (the box's own sign-in page instead). */
const SIGNIN_WAIT_MS = 15 * 60_000;

/**
 * "Open your dashboard". While the hand-off lasts (until the box tells us
 * its owner signed in), the one-time sign-in link the box made: it works
 * once and the box refuses it after 24 hours; we keep it until the box
 * confirms it was used, so a first click that didn't get through can be
 * tried again. Without a valid link we ask the box for a new one (it makes
 * it at its next check-in, within minutes) and say so on the account page.
 * After the hand-off, the box's own sign-in page.
 */
export async function openDashboard(acct: Account, boxId: string, now = new Date()): Promise<string> {
  const box = await ownBox(acct, boxId);
  if (!box.name || box.status !== "active") throw new ActionError("The box isn't ready yet.");
  const url = dashboardUrl(box.name);
  const code = box.signin_code && box.signin_expires_at && box.signin_expires_at > now && /^tfl_[a-z0-9]+$/.test(box.signin_code) ? box.signin_code : null;
  if (code && !box.handoff_closed_at) return `${url}/login#${code}`;
  if (!box.handoff_closed_at && (!box.signin_requested_at || now.getTime() - box.signin_requested_at.getTime() < SIGNIN_WAIT_MS)) {
    if (!box.signin_requested_at) await q.db()`update cloud_boxes set signin_requested_at = now() where id = ${box.id} and handoff_closed_at is null`;
    return `/account?signin=asked#${box.id}`;
  }
  return `${url}/login`;
}

/** Check-ins this often while the hand-off waits on the box (a link to make, a certificate to come). */
export const HANDOFF_SOON_SECONDS = 120;
/** And this often while it waits on the customer (to learn soon that they signed in). */
export const HANDOFF_PENDING_SECONDS = 600;

/**
 * What a counted check-in asks of the box for its hand-off (pure). A fresh
 * sign-in link when the box is ready and the customer asked for one, or we
 * never got one, or the one made at setup was made long before the
 * dashboard was ready (so its 24 hours count from readiness, not from the
 * install). Only while the box says its owner hasn't signed in.
 */
export function handoffAsk(
  box: Pick<q.BoxRow, "status" | "handoff_closed_at" | "signin_requested_at" | "signin_expires_at" | "ready_at">,
  handoff: unknown,
): { signin: boolean; checkInSeconds?: number } {
  if (handoff !== "pending" || box.handoff_closed_at || (box.status !== "active" && box.status !== "cert_pending")) return { signin: false };
  const stale = box.ready_at != null && box.signin_expires_at != null && box.signin_expires_at.getTime() < box.ready_at.getTime() + 23 * 3_600_000;
  const signin = box.status === "active" && (box.signin_requested_at != null || box.signin_expires_at == null || stale);
  return { signin, checkInSeconds: signin || box.status === "cert_pending" || box.signin_requested_at ? HANDOFF_SOON_SECONDS : HANDOFF_PENDING_SECONDS };
}

/** A sign-in link a box sent: its shape, and an expiry within the box's 24 hours. */
export function parseSignin(v: unknown, now = new Date()): { code: string; expires: Date } | null {
  if (!v || typeof v !== "object") return null;
  const { code, expiresAt } = v as Record<string, unknown>;
  const expires = typeof expiresAt === "string" ? new Date(expiresAt) : null;
  if (typeof code !== "string" || !/^tfl_[a-z0-9]{16,64}$/.test(code) || !expires || Number.isNaN(expires.getTime())) return null;
  if (expires <= now || expires.getTime() > now.getTime() + 24 * 3_600_000 + 5 * 60_000) return null;
  return { code, expires };
}

// ---- account actions ----

export type BoxAction =
  | { action: "forget-signin" }
  | { action: "new-signin" }
  | { action: "resize"; serverType: string; token: string }
  | { action: "cancel" }
  | { action: "resume" }
  | { action: "release"; confirm: string }
  | { action: "delete-server"; confirm: string; token: string; deleteData?: boolean };

export async function boxAction(acct: Account, boxId: string, a: BoxAction): Promise<string> {
  const box = await ownBox(acct, boxId);
  const s = q.db();
  if (box.status === "deleted") throw new ActionError("This box was deleted.");
  switch (a.action) {
    case "forget-signin":
      // Ends the hand-off: no link is kept, and none is asked for again.
      await s`update cloud_boxes set signin_code = null, signin_expires_at = null, signin_requested_at = null,
        handoff_closed_at = coalesce(handoff_closed_at, now()) where id = ${box.id}`;
      return "Done. We no longer hold a sign-in link for your box and won't ask it for another; sign in on the box itself.";
    case "new-signin":
      if (box.status !== "active") throw new ActionError("The box isn't ready yet.");
      if (box.handoff_closed_at) throw new ActionError("Your box says you signed in already, so it makes no more links for us: sign in on the box itself (your passkey, or tiffin login on the server).");
      await s`update cloud_boxes set signin_requested_at = now() where id = ${box.id} and handoff_closed_at is null`;
      return "Asked your box for a new one-time sign-in link. It makes it at its next check-in (within about ten minutes); then click Open dashboard.";
    case "resize": {
      if (box.status !== "active") throw new ActionError("Only a running box can be resized.");
      if (!onFor(box)) throw new ActionError("Resizing from your account is part of the subscription. Resize in the Hetzner console instead.");
      if (!(RESIZE_TYPES as readonly string[]).includes(a.serverType) || a.serverType === box.server_type || family(a.serverType) !== family(box.server_type ?? "")) {
        throw new ActionError("Pick another size of the same kind (Hetzner can't move a server between ARM and x86).");
      }
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      const token = String(a.token ?? "").trim();
      if (!/^[A-Za-z0-9]{20,128}$/.test(token)) throw new ActionError("Paste a Hetzner key to resize: we use it for this resize, then forget it.");
      await q.enqueue(box.id, "resize", { serverType: a.serverType }, seal(sealPublic(), token, tokenAAD(box.id)));
      return "Resizing: the box is offline for about 2 minutes.";
    }
    case "cancel":
    case "resume": {
      if (!box.stripe_subscription_id || UNMANAGED.has(box.status)) throw new ActionError("This box has no subscription.");
      await stripe().cancelAtPeriodEnd(box.stripe_subscription_id, a.action === "cancel");
      await s`update cloud_boxes set cancel_at_period_end = ${a.action === "cancel"}, updated_at = now() where id = ${box.id}`;
      return a.action === "cancel"
        ? "Cancelled at the end of this period. Your server and apps keep running after that; updates and the extras stop, and the address stays 30 days."
        : "The subscription continues.";
    }
    case "release":
    case "delete-server": {
      if (!box.name || a.confirm?.trim() !== box.name) throw new ActionError(`Type the box's name (${box.name ?? ""}) to confirm.`);
      if (box.status === "released") throw new ActionError("We no longer manage this box: delete the server in the Hetzner console.");
      // A delete that stopped (its job gave up) can be tried again, with a new key.
      if (box.status === "deleting" && a.action === "release") throw new ActionError("This box is being deleted.");
      if (await q.jobsBusy(box.id)) throw new ActionError("Something is already running for this box.", 409);
      const token = a.action === "delete-server" ? a.token?.trim() : undefined;
      if (a.action === "delete-server" && (!token || !/^[A-Za-z0-9]{20,128}$/.test(token))) throw new ActionError("Paste a Hetzner key to delete the server: we only delete it with your key, now.");
      if (box.stripe_subscription_id && !ENDED.has(box.plan_status)) {
        try {
          await stripe().cancelNow(box.stripe_subscription_id);
        } catch (e) {
          if (!(e instanceof StripeError && e.status === 404)) throw e;
        }
      }
      if (a.action === "delete-server") {
        await s.begin(async (tx) => {
          const rows = await tx`update cloud_boxes set status = 'deleting', updated_at = now() where id = ${box.id} and status <> 'deleted' returning id`;
          if (rows.length === 0) throw new ActionError("This box was deleted.");
          // Emails about a running box that are still waiting go unsent.
          await tx`update cloud_outbox set status = 'dropped', last_error = 'the box is being deleted', done_at = now()
            where box_id = ${box.id} and status = 'queued' and kind = any(${[...RUNNING_ONLY]})`;
          await q.enqueue(box.id, "delete_server", { deleteData: Boolean(a.deleteData) }, seal(sealPublic(), token!, tokenAAD(box.id)), tx);
        });
        return "Deleting: the address first, then the server.";
      }
      await s.begin(async (tx) => {
        await tx`update cloud_boxes set status = 'released', released_at = now(),
          signin_code = null, signin_expires_at = null, offsite_sealed = null, offsite_expires_at = null,
          offsite_purge_after = coalesce(offsite_purge_after, now() + interval '7 days'), updated_at = now() where id = ${box.id}`;
        if (box.dns_state === "live" || box.dns_state === "pending") await q.enqueue(box.id, "dns_remove", { reason: "released", gen: Number(box.generation) }, null, tx);
      });
      return "Done. We no longer manage this box; the server runs on in your Hetzner account. Its off-site backups in our storage are deleted after 7 days.";
    }
  }
}

// ---- box check-in ----

export type HeartbeatAnswer = {
  managed: boolean;
  active: boolean;
  updates: boolean;
  message?: string;
  signin?: boolean;
  checkInSeconds?: number;
  /** Off-site backup credentials, sealed to the box's key: only the box can open them. */
  offsite?: { sealed: string; expiresAt: string };
};

/** A box's off-site key as its check-in sends it: the base64 of 32 bytes (an X25519 public key). */
export function parseBackupKey(v: unknown): string | null {
  if (typeof v !== "string" || !/^[A-Za-z0-9+/]{43}=$/.test(v)) return null;
  return Buffer.from(v, "base64").length === 32 ? v : null;
}

/**
 * The off-site credentials a counted check-in hands over (pure): only while
 * the subscription is active, only sealed to the key the box sent, only
 * before they expire. The provisioner mints and seals them; we pass them on.
 */
export function offsiteAnswer(
  box: Pick<q.BoxRow, "backup_key" | "offsite_sealed" | "offsite_expires_at">,
  active: boolean,
  key: string | null,
  now = new Date(),
): HeartbeatAnswer["offsite"] {
  if (!active || !key || box.backup_key !== key || !box.offsite_sealed || !box.offsite_expires_at || box.offsite_expires_at <= now) return undefined;
  return { sealed: box.offsite_sealed, expiresAt: box.offsite_expires_at.toISOString() };
}

/**
 * What a check-in means (pure). A check-in counts only with the licence of
 * the box's current installation (each setup gets the next generation;
 * earlier ones are revoked), sent from the box's own address (its IPv4, or
 * its IPv6 /64). Only a check-in that counts keeps the address from being
 * parked, and brings back one that went.
 */
export function heartbeatDecision(
  box: Pick<q.BoxRow, "status" | "plan_status" | "first_paid_at" | "extras_paused_at" | "generation" | "ipv4" | "ipv6" | "dns_state" | "killed_at"> | null,
  l: Pick<Licence, "gen">,
  ip: string | null | undefined,
): { answer: HeartbeatAnswer; counts: boolean; refused?: string; restore: boolean } {
  if (!box || UNMANAGED.has(box.status)) return { answer: { managed: false, active: false, updates: true }, counts: false, restore: false };
  if ((l.gen ?? 0) !== Number(box.generation)) {
    return {
      answer: { managed: false, active: false, updates: true, message: "This box's ShipTiffin licence was replaced by a newer setup; this copy is no longer managed." },
      counts: false,
      refused: "licence of an earlier setup",
      restore: false,
    };
  }
  const on = extrasOn(box.plan_status, Boolean(box.first_paid_at)) && !box.extras_paused_at && (box.status === "active" || box.status === "cert_pending");
  const answer: HeartbeatAnswer = {
    managed: true,
    active: on,
    updates: on,
    message: on ? undefined : "Automatic updates are paused: this box's ShipTiffin subscription is not active. Your apps keep running. Renew at shiptiffin.com/account.",
  };
  if (!fromBox(ip, box.ipv4, box.ipv6)) return { answer, counts: false, refused: `sent from ${ip ?? "an unknown address"}, not the box's own`, restore: false };
  return { answer, counts: true, restore: on && !box.killed_at && (box.dns_state === "removed" || box.dns_state === "parked" || box.dns_state === "pending") };
}

export async function heartbeat(
  l: Licence,
  ip: string | null | undefined,
  report: { version?: unknown; failing?: unknown; handoff?: unknown; signin?: unknown; backupKey?: unknown },
  now = new Date(),
): Promise<HeartbeatAnswer> {
  const box = await q.boxById(l.box);
  const d = heartbeatDecision(box, l, ip);
  if (!box) return d.answer;
  if (!d.counts) {
    if (d.refused) await q.db()`update cloud_boxes set heartbeat_refused_at = now(), heartbeat_refused_why = ${d.refused.slice(0, 200)} where id = ${box.id}`;
    return d.answer;
  }
  const failing = Array.isArray(report.failing) ? report.failing.filter((x): x is string => typeof x === "string").slice(0, 30).map((x) => x.slice(0, 40)) : [];
  const version = typeof report.version === "string" ? report.version.slice(0, 40) : null;
  await q.db()`update cloud_boxes set last_heartbeat_at = now(), last_heartbeat_ip = ${ip ?? null}, last_version = ${version}, failing = ${failing},
    heartbeat_alerted_at = null where id = ${box.id}`;
  if (d.restore && !(await q.jobsBusy(box.id))) await q.enqueue(box.id, "dns_set", { reason: "heartbeat", gen: Number(box.generation) }, null);
  if (!d.answer.managed) return d.answer;

  // Off-site backups: a new key (a new server) drops what was sealed to the old one; the provisioner seals fresh ones.
  const key = parseBackupKey(report.backupKey);
  if (key && key !== box.backup_key) {
    await q.db()`update cloud_boxes set backup_key = ${key}, offsite_sealed = null, offsite_expires_at = null where id = ${box.id}`;
    Object.assign(box, { backup_key: key, offsite_sealed: null, offsite_expires_at: null });
  }
  const offsite = offsiteAnswer(box, d.answer.active, key, now);

  // The hand-off: the box says whether its owner signed in, and sends a link we asked for.
  if (report.handoff === "done" && !box.handoff_closed_at) {
    await q.db()`update cloud_boxes set handoff_closed_at = now(), signin_code = null, signin_requested_at = null where id = ${box.id}`;
    box.handoff_closed_at = now;
  }
  const link = report.handoff === "pending" && !box.handoff_closed_at && box.status === "active" ? parseSignin(report.signin, now) : null;
  if (link) {
    await q.db()`update cloud_boxes set signin_code = ${link.code}, signin_expires_at = ${link.expires}, signin_requested_at = null
      where id = ${box.id} and handoff_closed_at is null`;
    Object.assign(box, { signin_code: link.code, signin_expires_at: link.expires, signin_requested_at: null });
  }
  const ask = handoffAsk(box, report.handoff);
  return { ...d.answer, ...(ask.signin ? { signin: true } : {}), ...(ask.checkInSeconds ? { checkInSeconds: ask.checkInSeconds } : {}), ...(offsite ? { offsite } : {}) };
}

// ---- monitor ----

export async function runMonitor(now = new Date()): Promise<{ checked: number; queued: number }> {
  const boxes = await q.db()<Omit<MonitorBox, "warning">[]>`select id, name, email, status, plan_status, first_paid_at, extras_paused_at, dns_state, generation::int as generation,
    last_heartbeat_at, ready_at, health_failures, down_alerted_at, heartbeat_alerted_at, created_at from cloud_boxes where status in ('active', 'cert_pending')`;
  let queued = 0;
  await Promise.all(
    boxes.map(async (row) => {
      // The grace period's warning, keyed by when the extras paused: it counts once the mail server accepted it.
      const b: MonitorBox = { ...row, warning: row.extras_paused_at ? await q.outboxState(row.id, "dns_soon", row.extras_paused_at.toISOString()) : null };
      let d = decide(b, now);
      if (d.retryWarning && b.extras_paused_at) await q.retryOutbox(b.id, "dns_soon", b.extras_paused_at.toISOString());
      if (d.probe && b.name) d = decide(b, now, await probe(`${dashboardUrl(b.name)}/v1/health`));
      const p = d.patch;
      if (Object.keys(p).length) await q.db()`update cloud_boxes set ${q.db()(p as any)} where id = ${b.id}`;
      if (d.removeDns && !(await q.jobsBusy(b.id))) {
        await q.enqueue(b.id, "dns_remove", { reason: d.removeDns.reason, gen: d.removeDns.gen, pausedAt: d.removeDns.pausedAt?.toISOString() }, null);
      }
      const paused = b.extras_paused_at?.toISOString() ?? "";
      for (const kind of d.emails) {
        const key =
          kind === "down" || kind === "up" ? (b.down_alerted_at ?? now).toISOString() : kind === "silent" || kind === "parked" ? String(b.last_heartbeat_at?.toISOString() ?? "never") : paused;
        if (await q.outbox(b.id, kind, key, kind === "down" ? { since: now.toISOString() } : {})) queued++;
      }
    }),
  );
  return { checked: boxes.length, queued };
}

// ---- abuse and admin ----

export async function reportAbuse(target: string, email: string | null, details: string): Promise<void> {
  const t = target.trim().toLowerCase().slice(0, 300);
  const m = /([a-z0-9-]+)\.shiptiffin\.app/.exec(t);
  const [box] = m ? await q.db()`select id from cloud_boxes where name = ${m[1]!}` : [];
  await q.db()`insert into cloud_abuse_reports (target, box_id, reporter_email, details) values (${t}, ${(box?.id as string) ?? null}, ${email?.slice(0, 254) || null}, ${details.slice(0, 4000)})`;
  const notify = process.env.CLOUD_ABUSE_NOTIFY?.trim() || process.env.EARLY_ACCESS_NOTIFY?.trim();
  if (notify) await deliver(await abuseMail(notify, { target: t, box: (box?.id as string) ?? null, from: email ?? null, details }));
}

export type AdminInput = { action: "kill" | "restore" | "report" | "refund" | "resolve"; boxId?: string; reason?: string; reportId?: number; status?: string };

export async function adminAction(a: AdminInput) {
  const s = q.db();
  if (a.action === "report" && a.reportId && (a.status === "acted" || a.status === "dismissed" || a.status === "new")) {
    await s`update cloud_abuse_reports set status = ${a.status} where id = ${a.reportId}`;
    return "Report updated.";
  }
  const box = a.boxId ? await q.boxById(a.boxId) : null;
  if (!box) throw new ActionError("No such box.", 404);
  if (a.action === "kill") {
    const reason = (a.reason ?? "").trim().slice(0, 500) || "abuse report";
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = now(), kill_reason = ${reason}, dns_state = 'killed', updated_at = now() where id = ${box.id}`;
      await q.enqueue(box.id, "dns_remove", { reason: "kill" }, null, tx);
      await q.outbox(box.id, "killed", new Date().toISOString(), { reason }, tx);
    });
    await drainOutbox();
    return `Removing ${box.name ? boxDomain(box.name) : box.id} from DNS.`;
  }
  if (a.action === "restore") {
    // Back to parked: the address returns at the box's next check-in from its own address.
    await s.begin(async (tx) => {
      await tx`update cloud_boxes set killed_at = null, kill_reason = null, dns_state = 'parked', dns_changed_at = now(), updated_at = now() where id = ${box.id}`;
      if (box.status === "active" || box.status === "cert_pending") await q.enqueue(box.id, "dns_set", { reason: "admin_restore", gen: Number(box.generation) }, null, tx);
    });
    return "Restoring: the address comes back once the box checks in from its own address (within about six hours).";
  }
  if (a.action === "resolve") {
    await s`update cloud_boxes set attention = null, attention_at = null, updated_at = now() where id = ${box.id}`;
    return "Marked as seen to.";
  }
  if (a.action === "refund") {
    // The 14-day money-back guarantee: refund the first payment in full and end the subscription. Once per box.
    if (!box.stripe_subscription_id) throw new ActionError("This box has no subscription.");
    const first = await s.begin(async (tx) => {
      const queued = await q.outbox(box.id, "stripe_cancel_refund", "moneyback", { subscription: box.stripe_subscription_id, why: "moneyback" }, tx);
      if (queued) await q.outbox(box.id, "refunded", "", {}, tx);
      return queued;
    });
    await drainOutbox();
    return first ? "Refunding the first payment and ending the subscription. The customer is emailed." : "Already refunded (or under way): see the billing log.";
  }
  throw new ActionError("Unknown action.");
}
